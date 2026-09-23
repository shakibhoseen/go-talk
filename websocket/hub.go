package websocket

import (
	"context"
	"encoding/json"
	"go-talk/models"
	"go-talk/repository"
	"log"
)

type Hub struct {
	// Registered clients: userID -> map of active client connections (for multi-device support)
	clients    map[int]map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	chatRepo   repository.ChatRepository
}

func NewHub(chatRepo repository.ChatRepository) *Hub {
	return &Hub{
		clients:    make(map[int]map[*Client]bool),
		broadcast:  make(chan []byte),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		chatRepo:   chatRepo,
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			if _, ok := h.clients[client.UserID]; !ok {
				h.clients[client.UserID] = make(map[*Client]bool)
			}
			h.clients[client.UserID][client] = true
			log.Printf("User %d connected. Total sessions for user: %d\n", client.UserID, len(h.clients[client.UserID]))

		case client := <-h.unregister:
			if userConns, ok := h.clients[client.UserID]; ok {
				if _, exists := userConns[client]; exists {
					delete(userConns, client)
					close(client.send)
					if len(userConns) == 0 {
						delete(h.clients, client.UserID)
					}
					log.Printf("User %d disconnected\n", client.UserID)
				}
			}

		case message := <-h.broadcast:
			// Global broadcast (used for system maintenance announcements)
			for _, userConns := range h.clients {
				for client := range userConns {
					select {
					case client.send <- message:
					default:
						close(client.send)
						delete(userConns, client)
					}
				}
			}
		}
	}
}

// SendDirect pushes an event to a specific target user (across all their active devices)
func (h *Hub) SendDirect(targetUserID int, eventType EventType, payload any) {
	bytes, err := json.Marshal(payload)
	if err != nil {
		return
	}

	envelope, _ := json.Marshal(Event{
		Type:    eventType,
		Payload: bytes,
	})

	if userConns, ok := h.clients[targetUserID]; ok {
		for client := range userConns {
			select {
			case client.send <- envelope:
			default:
				close(client.send)
				delete(userConns, client)
			}
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

		msg := &models.Message{
			ConversationID: p.ConversationID,
			SenderID:       client.UserID,
			MessageType:    models.MessageType(p.MessageType),
			Content:        p.Content,
		}

		// ১. ডেটাবেসে মেসেজ ও লাস্ট মেসেজ সেভ
		if err := h.chatRepo.SaveMessage(context.Background(), msg); err != nil {
			log.Println("Failed to save message:", err)
			return
		}

		// ২. প্রেরককে 'Sent' স্ট্যাটাস হিসেবে পাঠানো
		h.SendDirect(client.UserID, EventNewMessage, msg)

		// ৩. কনভারসেশনের বাকি মেম্বারদের খুঁজে বের করে লাইভ মেসেজ পাঠানো
		members, err := h.chatRepo.GetConversationMemberIDs(context.Background(), p.ConversationID)
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

		// 1. DB-te delivered status mark kora
		_ = h.chatRepo.MarkMessageDelivered(context.Background(), ack.MessageID, client.UserID)

		// 2. Sender-ke double tick notify kora
		h.SendDirect(ack.SenderID, EventStatusUpdated, map[string]any{
			"conversation_id": ack.ConversationID,
			"message_id":      ack.MessageID,
			"status":          "delivered",
			"by_user":         client.UserID,
		})

	case EventAckSeen:
		var ack AckPayload
		if err := json.Unmarshal(event.Payload, &ack); err != nil {
			return
		}

		// 1. DB-te Range Seen update kora (Single Query Batch)
		_ = h.chatRepo.MarkMessagesSeenUpto(context.Background(), ack.ConversationID, client.UserID, ack.MessageID)

		// 2. Sender-ke blue tick notify kora
		h.SendDirect(ack.SenderID, EventStatusUpdated, map[string]any{
			"conversation_id": ack.ConversationID,
			"upto_message_id": ack.MessageID,
			"status":          "seen",
			"by_user":         client.UserID,
		})

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
