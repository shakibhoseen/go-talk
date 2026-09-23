package websocket

import "encoding/json"

type EventType string

const (
	// Chat Messaging Events
	EventSendMessage EventType = "send_message"
	EventNewMessage  EventType = "new_message"

	// WhatsApp 3-State Delivery Status Events
	EventAckDelivered  EventType = "ack_delivered"
	EventAckSeen       EventType = "ack_seen"
	EventStatusUpdated EventType = "status_updated"

	// Real-time Indicators
	EventTyping EventType = "typing"

	// WebRTC 1-to-1 Audio & Video Call Signaling
	EventCallOffer    EventType = "call_offer"
	EventCallAnswer   EventType = "call_answer"
	EventIceCandidate EventType = "ice_candidate"
	EventCallEnd      EventType = "call_end"
)

// Event is the standardized JSON envelope sent over WebSocket
type Event struct {
	Type    EventType       `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Payload for sending text/media messages
type SendMessagePayload struct {
	ConversationID string `json:"conversation_id"`
	Content        string `json:"content"`
	MessageType    string `json:"message_type"` // "text", "image", "audio"
}

// Payload for Delivery / Seen ACK
type AckPayload struct {
	MessageID      int64  `json:"message_id"`
	ConversationID string `json:"conversation_id"`
	SenderID       int    `json:"sender_id"` // Message-ti jar kache theke ashchilo (Jar tick update hobe)
}

// Payload for WebRTC Calling Signals
type CallSignalPayload struct {
	TargetUserID int  `json:"target_user_id"`
	SignalData   any  `json:"signal_data"` // SDP Offer/Answer or ICE Candidate JSON
	IsVideo      bool `json:"is_video"`
}
