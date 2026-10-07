package websocket

import (
	"context"
	"encoding/json"
	"go-talk/models"
	"go-talk/repository"
	"log"
	"strings"
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
			isFirstConn := len(h.clients[client.UserID]) == 0
			h.clients[client.UserID][client] = true
			h.mu.Unlock()
			log.Printf("User %d connected. Total sessions for user: %d\n", client.UserID, len(h.clients[client.UserID]))
			if isFirstConn {
				go h.BroadcastPresence(client.UserID, true)
			}
			go h.DeliverOfflineMessagesOnConnect(client.UserID)

		case client := <-h.unregister:
			h.mu.Lock()
			var isLastConn bool
			if userConns, ok := h.clients[client.UserID]; ok {
				if _, exists := userConns[client]; exists {
					delete(userConns, client)
					// Guard against double-close: only close if not already closed
					client.closeOnce.Do(func() { close(client.send) })
					if len(userConns) == 0 {
						delete(h.clients, client.UserID)
						isLastConn = true
					}
					log.Printf("User %d disconnected\n", client.UserID)
				}
			}
			h.mu.Unlock()
			if isLastConn {
				go h.BroadcastPresence(client.UserID, false)
			}

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

// IsUserOnline returns true if the user has at least one active connection
func (h *Hub) IsUserOnline(userID int) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	conns, ok := h.clients[userID]
	return ok && len(conns) > 0
}

// GetOnlineUserIDs returns the IDs of all currently connected users
func (h *Hub) GetOnlineUserIDs() []int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]int, 0, len(h.clients))
	for id, conns := range h.clients {
		if len(conns) > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

// BroadcastPresence sends a user_presence event to all connected users
func (h *Hub) BroadcastPresence(userID int, isOnline bool) {
	payload := UserPresencePayload{
		UserID:   userID,
		IsOnline: isOnline,
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return
	}
	envelope, err := json.Marshal(Event{
		Type:    EventUserPresence,
		Payload: bytes,
	})
	if err != nil {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, userConns := range h.clients {
		for client := range userConns {
			select {
			case client.send <- envelope:
			default:
				// Slow client: drop non-critical presence update to prevent stalling
			}
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

		if p.ConversationID == "" {
			return
		}

		var clientMsgID *string
		trimmedClientMsgID := strings.TrimSpace(p.ClientMessageID)
		if trimmedClientMsgID != "" {
			if len(trimmedClientMsgID) > 64 {
				log.Printf("Invalid client_message_id: exceeds max length 64 (len=%d)", len(trimmedClientMsgID))
				return
			}
			clientMsgID = &trimmedClientMsgID
		}

		ctx := context.Background()

		// Authorization: check conversation membership
		isMember, err := h.chatRepo.IsConversationMember(ctx, p.ConversationID, client.UserID)
		if err != nil || !isMember {
			log.Printf("[SECURITY] User %d is not a member of conversation %s; rejecting send_message", client.UserID, p.ConversationID)
			return
		}

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
			ClientMessageID:  clientMsgID,
			MessageType:      models.MessageType(p.MessageType),
			Content:          p.Content,
		}

		// ১. ডেটাবেসে মেসেজ ও লাস্ট মেসেজ সেভ (idempotent)
		isNew, err := h.chatRepo.SaveMessage(ctx, msg)
		if err != nil {
			log.Println("Failed to save message:", err)
			return
		}

		// ২. প্রেরককে 'Sent' স্ট্যাটাস হিসেবে পাঠানো (authoritative echo in both first send & retry)
		h.SendDirect(client.UserID, EventNewMessage, msg)

		// ৩. Duplicate retry হলে মেম্বারদের দ্বিতীয়বার ব্রডকাস্ট পাঠানো স্কিপ
		if !isNew {
			log.Printf("[IDEMPOTENT_RETRY] Message %d already exists for client_message_id=%v; skipping recipient broadcast",
				msg.ID, p.ClientMessageID)
			return
		}

		// ৪. নতুন মেসেজ হলে কনভারসেশনের বাকি মেম্বারদের লাইভ মেসেজ পাঠানো
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

		// 1. Authorization: check conversation membership
		isMember, err := h.chatRepo.IsConversationMember(ctx, ack.ConversationID, client.UserID)
		if err != nil || !isMember {
			log.Printf("[SECURITY] User %d is not a member of conversation %s; rejecting ack_delivered", client.UserID, ack.ConversationID)
			return
		}

		// 2. Validation: message exists in conversation and <= last_message_id; derive authoritative sender ID
		actualSenderID, err := h.chatRepo.GetMessageSenderInConversation(ctx, ack.ConversationID, ack.MessageID)
		if err != nil {
			log.Printf("[VALIDATION] Message %d invalid in conversation %s; rejecting ack_delivered: %v", ack.MessageID, ack.ConversationID, err)
			return
		}

		// 3. Atomically update delivered watermark and message receipt in a single transaction
		advanced, err := h.chatRepo.ProcessAckDelivered(ctx, ack.ConversationID, client.UserID, ack.MessageID)
		if err != nil {
			log.Printf("[ERROR] Failed to process ack_delivered for user %d msg %d: %v", client.UserID, ack.MessageID, err)
			return
		}

		// 4. Check if all members have received up to messageID (only if watermark advanced)
		if !advanced {
			return
		}

		allDelivered, err := h.chatRepo.AreAllMembersDeliveredUpto(ctx, ack.ConversationID, actualSenderID, ack.MessageID)
		if err == nil && allDelivered {
			targetSenderID := actualSenderID
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

		ctx := context.Background()

		// 1. Authorization: check conversation membership
		isMember, err := h.chatRepo.IsConversationMember(ctx, ack.ConversationID, client.UserID)
		if err != nil || !isMember {
			log.Printf("[SECURITY] User %d is not a member of conversation %s; rejecting ack_seen", client.UserID, ack.ConversationID)
			return
		}

		// 2. Validation: message exists in conversation and <= last_message_id; derive authoritative sender ID
		actualSenderID, err := h.chatRepo.GetMessageSenderInConversation(ctx, ack.ConversationID, ack.MessageID)
		if err != nil {
			log.Printf("[VALIDATION] Message %d invalid in conversation %s; rejecting ack_seen: %v", ack.MessageID, ack.ConversationID, err)
			return
		}

		log.Printf(
			"[ACK_SEEN] user=%d conversation=%s message=%d client_sender=%d actual_sender=%d",
			client.UserID,
			ack.ConversationID,
			ack.MessageID,
			ack.SenderID,
			actualSenderID,
		)

		// 3. Atomically update seen status in range and watermarks in a single transaction
		advanced, err := h.chatRepo.ProcessAckSeen(ctx, ack.ConversationID, client.UserID, ack.MessageID)
		if err != nil {
			log.Printf("[ERROR] Failed to process ack_seen for user %d msg %d: %v", client.UserID, ack.MessageID, err)
			return
		}

		// Only broadcast watermark and status update if the watermark actually advanced forward!
		if !advanced {
			log.Printf("[ACK_SEEN_NOOP] Watermark did not advance for user %d msg %d in conv %s", client.UserID, ack.MessageID, ack.ConversationID)
			return
		}

		log.Printf(
			"[WATERMARK] user=%d -> message=%d conversation=%s",
			client.UserID,
			ack.MessageID,
			ack.ConversationID,
		)

		// 4. Broadcast live avatar watermark to members
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

		// 5. WhatsApp check: all members read up to messageID
		allRead, err := h.chatRepo.AreAllMembersReadUpto(ctx, ack.ConversationID, actualSenderID, ack.MessageID)
		if err == nil && allRead {
			targetSenderID := actualSenderID
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
