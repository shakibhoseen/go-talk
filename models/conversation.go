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
	AvatarURL           *string          `json:"avatar_url,omitempty"`
	LastMessageContent  *string          `json:"last_message_content,omitempty"`
	LastMessageSenderID *int             `json:"last_message_sender_id,omitempty"`
	LastMessageAt       *time.Time       `json:"last_message_at,omitempty"`
	UnreadCount         int              `json:"unread_count"`
	CreatedAt           time.Time        `json:"created_at"`
}

type ConversationMember struct {
	ConversationID    string    `json:"conversation_id"`
	UserID            int       `json:"user_id"`
	Role              string    `json:"role"` // 'admin', 'member'
	LastReadMessageID int64     `json:"last_read_message_id"`
	JoinedAt          time.Time `json:"joined_at"`
}

type ConversationMemberProfile struct {
	ConversationID string    `json:"conversation_id"`
	UserID         int       `json:"user_id"`
	Role           string    `json:"role"`
	JoinedAt       time.Time `json:"joined_at"`
	Name           string    `json:"name"`
	Email          string    `json:"email"`
	AvatarURL      *string   `json:"avatar_url,omitempty"`
}

type UpdateGroupAvatarRequest struct {
	AvatarURL string `json:"avatar_url"`
}

type AddGroupMemberRequest struct {
	UserID int    `json:"user_id"`
	Role   string `json:"role"` // default "member"
}
