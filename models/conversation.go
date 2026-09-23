package models

import "time"

type ConversationType string

const (
	DirectChat ConversationType = "direct"
	GroupChat  ConversationType = "group"
)

// Conversation represents the Chat Head / Inbox Item
type Conversation struct {
	ID                  string           `json:"id"`
	Type                ConversationType `json:"type"`
	Title               *string          `json:"title,omitempty"` // Group title
	LastMessageContent  *string          `json:"last_message_content,omitempty"`
	LastMessageSenderID *int             `json:"last_message_sender_id,omitempty"`
	LastMessageAt       *time.Time       `json:"last_message_at,omitempty"`
	UnreadCount         int              `json:"unread_count"`
	CreatedAt           time.Time        `json:"created_at"`
}

type ConversationMember struct {
	ConversationID string    `json:"conversation_id"`
	UserID         int       `json:"user_id"`
	Role           string    `json:"role"` // 'admin', 'member'
	JoinedAt       time.Time `json:"joined_at"`
}
