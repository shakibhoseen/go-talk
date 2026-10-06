package websocket

import (
	"context"
	"encoding/json"
	"go-talk/models"
	"go-talk/repository"
	"log"
	"sync"
)

type Hub struct {
	// Registered clients: userID -> map of active client connections (for multi-device support)
	clients    map[int]map[*Client]bool
	mu         sync.RWMutex // Protects h.clients from concurrent read/write panics
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	chatRepo   repository.ChatRepository
	userRepo   repository.UserRepository
}

func NewHub(chatRepo repository.ChatRepository, userRepo repository.UserRepository) *Hub {
	return &Hub{
		clients:    make(map[int]map[*Client]bool),
		broadcast:  make(chan []byte),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		chatRepo:   chatRepo,
		userRepo:   userRepo,
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			if _, ok := h.clients[client.UserID]; !ok {
				h.clients[client.UserID] = make(map[*Client]bool)
			}
			h.clients[client.UserID][client] = true
			h.mu.Unlock()
			log.Printf("User %d connected. Total sessions for user: %d\n", client.UserID, len(h.clients[client.UserID]))
			go h.DeliverOfflineMessagesOnConnect(client.UserID)

		case client := <-h.unregister:
			h.mu.Lock()
			if userConns, ok := h.clients[client.UserID]; ok {
				if _, exists := userConns[client]; exists {
					delete(userConns, client)
					// Guard against double-close: only close if not already closed
					client.closeOnce.Do(func() { close(client.send) })
					if len(userConns) == 0 {
						delete(h.clients, client.UserID)
					}
					log.Printf("User %d disconnected\n", client.UserID)
				}
			}
			h.mu.Unlock()

		case message := <-h.broadcast:
			// Global broadcast (used for system maintenance announcements)
			h.mu.Lock()
			for _, userConns := range h.clients {
				for client := range userConns {
					select {
					case client.send <- message:
					default:
						// Slow client — remove and close safely
						delete(userConns, client)
						client.closeOnce.Do(func() { close(client.send) })
					}
				}
			}
			h.mu.Unlock()
		}
	}
}

// SendDirect pushes an event to a specific target user (across all their active devices).
// Safe to call from any goroutine.
func (h *Hub) SendDirect(targetUserID int, eventType EventType, payload any) {
	bytes, err := json.Marshal(payload)
	if err != nil {
		return
	}

	envelope, _ := json.Marshal(Event{
		Type:    eventType,
		Payload: bytes,
	})

	h.mu.RLock()
	userConns, ok := h.clients[targetUserID]
	if !ok {
		log.Printf(
			"[SEND_DIRECT] user=%d NOT CONNECTED, event=%s",
			targetUserID,
			eventType,
		)
		h.mu.RUnlock()
		return
	}
	log.Printf(
		"[SEND_DIRECT] user=%d event=%s connections=%d",
		targetUserID,
		eventType,
		len(userConns),
	)

	// Snapshot the client set so we can release the read lock before touching channels
	targets := make([]*Client, 0, len(userConns))
	for client := range userConns {
		targets = append(targets, client)
	}
	h.mu.RUnlock()

	for _, client := range targets {
		select {
		case client.send <- envelope:
		default:
			// Slow client — upgrade to write lock to remove it safely
			h.mu.Lock()
			if userConns2, ok2 := h.clients[targetUserID]; ok2 {
				delete(userConns2, client)
				client.closeOnce.Do(func() { close(client.send) })
				if len(userConns2) == 0 {
					delete(h.clients, targetUserID)
				}
			}
			h.mu.Unlock()
		}
	}
}

// RouteIncomingEvent handles incoming client messages and routes to DB + Recipient
func (h *Hub) RouteIncomingEvent(client *Client, raw []byte) {
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		log.Println("Invalid event format:", err)
		return
	}

	switch event.Type {
	case EventSendMessage:
		var p SendMessagePayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return
		}

		ctx := context.Background()

		convType, _ := h.chatRepo.GetConversationType(ctx, p.ConversationID)
		if convType == "" {
			convType = "direct"
		}

		msg := &models.Message{
			ConversationID:   p.ConversationID,
			ConversationType: convType,
			SenderID:         client.UserID,
			SenderName:       client.UserName,
			SenderAvatar:     client.AvatarURL,
			MessageType:      models.MessageType(p.MessageType),
			Content:          p.Content,
		}

		// ১. ডেটাবেসে মেসেজ ও লাস্ট মেসেজ সেভ (সেন্ডারের read ও delivered ওয়াটারমার্ক সহ)
		if err := h.chatRepo.SaveMessage(ctx, msg); err != nil {
			log.Println("Failed to save message:", err)
			return
		}

		// ২. প্রেরককে 'Sent' স্ট্যাটাস হিসেবে পাঠানো (Single Tick)
		h.SendDirect(client.UserID, EventNewMessage, msg)

		// ৩. কনভারসেশনের বাকি মেম্বারদের খুঁজে বের করে লাইভ মেসেজ পাঠানো
		members, err := h.chatRepo.GetConversationMemberIDs(ctx, p.ConversationID)
		if err != nil {
			log.Println("Error fetching members:", err)
			return
		}

		for _, memberID := range members {
			if memberID != client.UserID {
				// অপর প্রান্তের ইউজার কানেক্টেড থাকলে তার সকেটে মেসেজ পুশ হবে
				h.SendDirect(memberID, EventNewMessage, msg)
			}
		}

	case EventAckDelivered:
		var ack AckPayload
		if err := json.Unmarshal(event.Payload, &ack); err != nil {
			return
		}
		if ack.MessageID <= 0 || ack.ConversationID == "" {
			return
		}

		ctx := context.Background()

		// 1. এই ইউজারের delivered watermark এবং receipt টেবিলে আপডেট
		_ = h.chatRepo.UpdateLastDeliveredWatermark(ctx, ack.ConversationID, client.UserID, ack.MessageID)
		_ = h.chatRepo.MarkMessageDelivered(ctx, ack.MessageID, client.UserID)

		// 2. WhatsApp রুল: কনভারসেশনের বাকি সকল মেম্বার মেসেজটি পেয়েছে কি না চেক
		allDelivered, err := h.chatRepo.AreAllMembersDeliveredUpto(ctx, ack.ConversationID, ack.SenderID, ack.MessageID)
		if err == nil && allDelivered {
			targetSenderID := ack.SenderID
			if targetSenderID > 0 {
				h.SendDirect(targetSenderID, EventStatusUpdated, map[string]any{
					"conversation_id": ack.ConversationID,
					"upto_message_id": ack.MessageID,
					"status":          "delivered",
				})
			} else {
				members, _ := h.chatRepo.GetConversationMemberIDs(ctx, ack.ConversationID)
				for _, mID := range members {
					if mID != client.UserID {
						h.SendDirect(mID, EventStatusUpdated, map[string]any{
							"conversation_id": ack.ConversationID,
							"upto_message_id": ack.MessageID,
							"status":          "delivered",
						})
					}
				}
			}
		}

	case EventAckSeen:
		var ack AckPayload
		if err := json.Unmarshal(event.Payload, &ack); err != nil {
			return
		}
		if ack.MessageID <= 0 || ack.ConversationID == "" {
			return
		}
		log.Printf(
			"[ACK_SEEN] user=%d conversation=%s message=%d sender=%d",
			client.UserID,
			ack.ConversationID,
			ack.MessageID,
			ack.SenderID,
		)

		ctx := context.Background()

		// ১. Messages টেবিলে রেঞ্জ সিন স্ট্যাটাস এবং ওয়াটারমার্ক (read + delivered) আপডেট
		_ = h.chatRepo.MarkMessagesSeenUpto(ctx, ack.ConversationID, client.UserID, ack.MessageID)
		_ = h.chatRepo.UpdateLastReadWatermark(ctx, ack.ConversationID, client.UserID, ack.MessageID)

		log.Printf(
			"[WATERMARK] user=%d -> message=%d conversation=%s",
			client.UserID,
			ack.MessageID,
			ack.ConversationID,
		)

		// ২. মেম্বারদের লাইভ বাবল ওয়াটারমার্ক ব্রডকাস্ট (কার কার পড়া শেষ তা ছোট Avatar দিয়ে দেখানোর জন্য)
		members, err := h.chatRepo.GetConversationMemberIDs(ctx, ack.ConversationID)
		if err == nil {
			watermarkPayload := map[string]any{
				"conversation_id":      ack.ConversationID,
				"user_id":              client.UserID,
				"user_name":            client.UserName,  // cached at connect time
				"user_avatar":          client.AvatarURL, // cached at connect time
				"last_read_message_id": ack.MessageID,
			}

			for _, memberID := range members {
				if memberID != client.UserID {
					h.SendDirect(memberID, EventMemberReadWatermark, watermarkPayload)
				}
			}
		}

		// ৩. WhatsApp রুল: কনভারসেশনের বাকি সকল মেম্বার সিন করেছে কি না চেক (সবাই সিন করলেই Double Blue Tick)
		allRead, err := h.chatRepo.AreAllMembersReadUpto(ctx, ack.ConversationID, ack.SenderID, ack.MessageID)
		if err == nil && allRead {
			targetSenderID := ack.SenderID
			if targetSenderID > 0 {
				h.SendDirect(targetSenderID, EventStatusUpdated, map[string]any{
					"conversation_id": ack.ConversationID,
					"upto_message_id": ack.MessageID,
					"status":          "seen",
				})
			} else if members != nil {
				for _, mID := range members {
					if mID != client.UserID {
						h.SendDirect(mID, EventStatusUpdated, map[string]any{
							"conversation_id": ack.ConversationID,
							"upto_message_id": ack.MessageID,
							"status":          "seen",
						})
					}
				}
			}
		}

	case EventCallOffer, EventCallAnswer, EventIceCandidate, EventCallEnd:
		// WebRTC Signaling Forwarding
		var callPayload CallSignalPayload
		if err := json.Unmarshal(event.Payload, &callPayload); err != nil {
			return
		}
		h.SendDirect(callPayload.TargetUserID, event.Type, map[string]any{
			"from_user_id": client.UserID,
			"signal_data":  callPayload.SignalData,
			"is_video":     callPayload.IsVideo,
		})
	}
}

// DeliverOfflineMessagesOnConnect automatically marks offline messages as delivered
// when a user comes online and establishes a WebSocket connection.
func (h *Hub) DeliverOfflineMessagesOnConnect(userID int) {
	ctx := context.Background()
	results, err := h.chatRepo.SyncUserDelivery(ctx, userID)
	if err != nil || len(results) == 0 {
		return
	}

	for _, res := range results {
		log.Printf("[OFFLINE_DELIVERY_SYNC] user=%d conv=%s upto=%d sender=%d", userID, res.ConversationID, res.UptoMessageID, res.SenderID)
		if res.SenderID > 0 {
			h.SendDirect(res.SenderID, EventStatusUpdated, map[string]any{
				"conversation_id": res.ConversationID,
				"upto_message_id": res.UptoMessageID,
				"status":          "delivered",
			})
		} else {
			members, err := h.chatRepo.GetConversationMemberIDs(ctx, res.ConversationID)
			if err == nil {
				for _, mID := range members {
					if mID != userID {
						h.SendDirect(mID, EventStatusUpdated, map[string]any{
							"conversation_id": res.ConversationID,
							"upto_message_id": res.UptoMessageID,
							"status":          "delivered",
						})
					}
				}
			}
		}
	}
}
