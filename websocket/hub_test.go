package websocket_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"go-talk/models"
	"go-talk/repository"
	ws "go-talk/websocket"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func TestHubSendMessageMembershipAuthorization(t *testing.T) {
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		dsn = "postgres://shakib@localhost:5432/gotalkdb?sslmode=disable"
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("Failed to connect to DB: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Skipf("Skipping test; DB unreachable: %v", err)
	}

	chatRepo := repository.NewChatRepository(db)
	userRepo := repository.NewUserRepository(db)
	hub := ws.NewHub(chatRepo, userRepo)
	ctx := context.Background()
	now := time.Now().UnixNano()

	// 1. Create 3 test users: u1 and u2 will be in conv, u3 is an outsider (non-member)
	var u1, u2, u3 int
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member 1", fmt.Sprintf("m1_%d@test.com", now), "hash").Scan(&u1)
	if err != nil {
		t.Fatalf("Failed to create u1: %v", err)
	}
	defer func() {
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1, $2, $3)`, u1, u2, u3)
	}()

	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member 2", fmt.Sprintf("m2_%d@test.com", now+1), "hash").Scan(&u2)
	if err != nil {
		t.Fatalf("Failed to create u2: %v", err)
	}

	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Attacker / Non-member", fmt.Sprintf("m3_%d@test.com", now+2), "hash").Scan(&u3)
	if err != nil {
		t.Fatalf("Failed to create u3: %v", err)
	}

	// 2. Create conversation between u1 and u2
	convID, err := chatRepo.GetOrCreateDirectConversation(ctx, u1, u2)
	if err != nil {
		t.Fatalf("Failed to create conv: %v", err)
	}
	defer db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)

	// 3. Test 1: Valid conversation member (u1) sends a message -> MUST SUCCEED
	memberClient := &ws.Client{
		Hub:       hub,
		UserID:    u1,
		UserName:  "Member 1",
		AvatarURL: "",
	}

	memberPayload, _ := json.Marshal(ws.SendMessagePayload{
		ConversationID:  convID,
		Content:         "Authorized message from u1",
		ClientMessageID: fmt.Sprintf("member-msg-%d", now),
		MessageType:     "text",
	})
	memberEvent, _ := json.Marshal(ws.Event{
		Type:    ws.EventSendMessage,
		Payload: memberPayload,
	})

	hub.RouteIncomingEvent(memberClient, memberEvent)

	// Verify u1's message was persisted in the database
	msgs, _, err := chatRepo.GetConversationMessages(ctx, convID, u1, 10, 0, 0)
	if err != nil {
		t.Fatalf("Failed to fetch messages for u1: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "Authorized message from u1" {
		t.Fatalf("Expected 1 persisted message from u1, found %d", len(msgs))
	}

	// 4. Test 2: Non-member (u3) attempts to inject a message into convID -> MUST BE REJECTED
	attackerClient := &ws.Client{
		Hub:       hub,
		UserID:    u3,
		UserName:  "Attacker",
		AvatarURL: "",
	}

	attackerPayload, _ := json.Marshal(ws.SendMessagePayload{
		ConversationID:  convID,
		Content:         "Malicious injected message from u3",
		ClientMessageID: fmt.Sprintf("attacker-msg-%d", now),
		MessageType:     "text",
	})
	attackerEvent, _ := json.Marshal(ws.Event{
		Type:    ws.EventSendMessage,
		Payload: attackerPayload,
	})

	hub.RouteIncomingEvent(attackerClient, attackerEvent)

	// Verify NO new message was created; total messages remains 1
	var count int
	err = db.QueryRow(`SELECT count(*) FROM messages WHERE conversation_id = $1`, convID).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query messages count: %v", err)
	}
	if count != 1 {
		t.Fatalf("Security failure: expected messages count to remain 1, but found %d (non-member injected a message!)", count)
	}

	// Verify no message exists with sender_id = u3
	var u3MsgCount int
	err = db.QueryRow(`SELECT count(*) FROM messages WHERE conversation_id = $1 AND sender_id = $2`, convID, u3).Scan(&u3MsgCount)
	if err != nil {
		t.Fatalf("Failed to query u3 messages count: %v", err)
	}
	if u3MsgCount != 0 {
		t.Fatalf("Security failure: non-member u3 injected %d messages into conversation %s", u3MsgCount, convID)
	}
}

func TestHubAckDeliveredAndSeenFlows(t *testing.T) {
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		dsn = "postgres://shakib@localhost:5432/gotalkdb?sslmode=disable"
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("Failed to connect to DB: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Skipf("Skipping test; DB unreachable: %v", err)
	}

	chatRepo := repository.NewChatRepository(db)
	userRepo := repository.NewUserRepository(db)
	hub := ws.NewHub(chatRepo, userRepo)
	ctx := context.Background()
	now := time.Now().UnixNano()

	// 1. Create 2 test users
	var u1, u2 int
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Alice", fmt.Sprintf("al_hub_%d@test.com", now), "hash").Scan(&u1)
	if err != nil {
		t.Fatalf("Failed to create u1: %v", err)
	}
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Bob", fmt.Sprintf("bob_hub_%d@test.com", now+1), "hash").Scan(&u2)
	if err != nil {
		t.Fatalf("Failed to create u2: %v", err)
	}
	defer db.Exec(`DELETE FROM users WHERE id IN ($1, $2)`, u1, u2)

	convID, err := chatRepo.GetOrCreateDirectConversation(ctx, u1, u2)
	if err != nil {
		t.Fatalf("Failed to create conv: %v", err)
	}
	defer db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)

	// U1 sends message
	cMsg := fmt.Sprintf("hub-msg-%d", now)
	msg := &models.Message{
		ConversationID:  convID,
		SenderID:        u1,
		MessageType:     "text",
		Content:         "Hello Bob from Alice",
		ClientMessageID: &cMsg,
	}
	if _, err := chatRepo.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("Failed to save msg: %v", err)
	}

	bobClient := &ws.Client{
		Hub:       hub,
		UserID:    u2,
		UserName:  "Bob",
		AvatarURL: "",
	}

	// 2. Bob sends EventAckDelivered via WebSocket
	ackDeliveredPayload, _ := json.Marshal(ws.AckPayload{
		ConversationID: convID,
		MessageID:      msg.ID,
		SenderID:       u1,
	})
	delivEvent, _ := json.Marshal(ws.Event{
		Type:    ws.EventAckDelivered,
		Payload: ackDeliveredPayload,
	})
	hub.RouteIncomingEvent(bobClient, delivEvent)

	var delivWM int64
	_ = db.QueryRow(`SELECT last_delivered_message_id FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`, convID, u2).Scan(&delivWM)
	if delivWM != msg.ID {
		t.Fatalf("Expected Bob's delivered watermark to be %d, got %d", msg.ID, delivWM)
	}

	// 3. Bob sends EventAckSeen via WebSocket
	ackSeenPayload, _ := json.Marshal(ws.AckPayload{
		ConversationID: convID,
		MessageID:      msg.ID,
		SenderID:       u1,
	})
	seenEvent, _ := json.Marshal(ws.Event{
		Type:    ws.EventAckSeen,
		Payload: ackSeenPayload,
	})
	hub.RouteIncomingEvent(bobClient, seenEvent)

	var readWM int64
	_ = db.QueryRow(`SELECT last_read_message_id FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`, convID, u2).Scan(&readWM)
	if readWM != msg.ID {
		t.Fatalf("Expected Bob's read watermark to be %d, got %d", msg.ID, readWM)
	}

	// 4. Bob sends duplicate EventAckSeen -> MUST NOT error or regress
	hub.RouteIncomingEvent(bobClient, seenEvent)
	_ = db.QueryRow(`SELECT last_read_message_id FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`, convID, u2).Scan(&readWM)
	if readWM != msg.ID {
		t.Fatalf("Expected Bob's read watermark to remain %d, got %d", msg.ID, readWM)
	}
}
