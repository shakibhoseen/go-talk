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
	MessageType    MessageType `json:"message_type"`
	Content        string      `json:"content"`
	CreatedAt      time.Time   `json:"created_at"`
}
