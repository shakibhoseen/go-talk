package models

import "time"

type MessageType string

const (
	MsgText  MessageType = "text"
	MsgImage MessageType = "image"
	MsgAudio MessageType = "audio"
)

type Message struct {
	ID             int64       `json:"id"`
	ConversationID string      `json:"conversation_id"`
	SenderID       int         `json:"sender_id"`
	SenderName     string      `json:"sender_name,omitempty"`
	SenderAvatar   string      `json:"sender_avatar,omitempty"`
	MessageType    MessageType `json:"message_type"`
	Content        string      `json:"content"`
	CreatedAt      time.Time   `json:"created_at"`
}

// ReadReceiptUser: Jei member read koreche tar minimal profile
type ReadReceiptUser struct {
	UserID    int    `json:"user_id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

// REST API ba Socket Payload-e Message-er sathe read_by list
type MessageWithReceipts struct {
	Message
	ReadBy    []ReadReceiptUser `json:"read_by,omitempty"`
	ReadCount int               `json:"read_count"`
}

// MessageWatermark: Top-level watermarks response map value
type MessageWatermark struct {
	Users []ReadReceiptUser `json:"users"`
	Count int               `json:"count"`
}
