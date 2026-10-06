package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go-talk/models"
	"go-talk/repository"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func TestIdempotencyAndClientMessageID(t *testing.T) {
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
		t.Skipf("Skipping integration test; DB unreachable: %v", err)
	}

	chatRepo := repository.NewChatRepository(db)
	ctx := context.Background()

	// 1. Setup test users and conversations
	var u1, u2 int
	now := time.Now().UnixNano()
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Idemp U1", fmt.Sprintf("idemp_u1_%d@test.com", now), "hash").Scan(&u1)
	if err != nil {
		t.Fatalf("Failed to create u1: %v", err)
	}
	defer db.Exec(`DELETE FROM users WHERE id IN ($1, $2)`, u1, u2)

	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Idemp U2", fmt.Sprintf("idemp_u2_%d@test.com", now+1), "hash").Scan(&u2)
	if err != nil {
		t.Fatalf("Failed to create u2: %v", err)
	}

	convA, err := chatRepo.GetOrCreateDirectConversation(ctx, u1, u2)
	if err != nil {
		t.Fatalf("Failed to create direct convA: %v", err)
	}
	defer db.Exec(`DELETE FROM conversations WHERE id = $1`, convA)

	// Create a second conversation for Test 5
	convB, err := chatRepo.CreateGroupConversation(ctx, "Test Group Conv B", u1, []int{u2})
	if err != nil {
		t.Fatalf("Failed to create convB: %v", err)
	}
	defer db.Exec(`DELETE FROM conversations WHERE id = $1`, convB)

	// =========================================================================
	// Test 1 — Normal send: client_message_id = X -> one DB row, isNew=true
	// =========================================================================
	clientMsgID1 := fmt.Sprintf("uuid-t1-%d", now)
	msg1 := &models.Message{
		ConversationID:  convA,
		SenderID:        u1,
		ClientMessageID: &clientMsgID1,
		MessageType:     "text",
		Content:         "Hello from Test 1",
	}

	isNew1, err := chatRepo.SaveMessage(ctx, msg1)
	if err != nil {
		t.Fatalf("Test 1 failed: %v", err)
	}
	if !isNew1 {
		t.Fatalf("Test 1 expected isNew=true, got false")
	}
	if msg1.ID <= 0 {
		t.Fatalf("Test 1 expected valid msg.ID > 0, got %d", msg1.ID)
	}

	var count1 int
	_ = db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id = $1 AND client_message_id = $2`, convA, clientMsgID1).Scan(&count1)
	if count1 != 1 {
		t.Fatalf("Test 1 expected exactly 1 DB row, got %d", count1)
	}

	// =========================================================================
	// Test 2 — Exact retry: same conv, sender, client_msg_id, content -> isNew=false, same ID
	// =========================================================================
	retryMsg1 := &models.Message{
		ConversationID:  convA,
		SenderID:        u1,
		ClientMessageID: &clientMsgID1,
		MessageType:     "text",
		Content:         "Hello from Test 1",
	}

	isNew2, err := chatRepo.SaveMessage(ctx, retryMsg1)
	if err != nil {
		t.Fatalf("Test 2 retry failed: %v", err)
	}
	if isNew2 {
		t.Fatalf("Test 2 expected isNew=false on retry, got true")
	}
	if retryMsg1.ID != msg1.ID {
		t.Fatalf("Test 2 expected returned ID (%d) to equal original ID (%d)", retryMsg1.ID, msg1.ID)
	}

	var count2 int
	_ = db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id = $1 AND client_message_id = $2`, convA, clientMsgID1).Scan(&count2)
	if count2 != 1 {
		t.Fatalf("Test 2 expected still exactly 1 DB row, got %d", count2)
	}

	// =========================================================================
	// Test 3 — Retry after connection loss simulation: insert succeeded, echo lost, retry same ID
	// =========================================================================
	clientMsgIDLoss := fmt.Sprintf("uuid-loss-%d", now)
	msgLoss := &models.Message{
		ConversationID:  convA,
		SenderID:        u1,
		ClientMessageID: &clientMsgIDLoss,
		MessageType:     "text",
		Content:         "Message where echo was lost",
	}

	isNewLoss1, err := chatRepo.SaveMessage(ctx, msgLoss)
	if err != nil || !isNewLoss1 {
		t.Fatalf("Test 3 initial save failed: isNew=%v, err=%v", isNewLoss1, err)
	}

	// Simulate client reconnect and re-sending identical payload:
	reconnectMsgLoss := &models.Message{
		ConversationID:  convA,
		SenderID:        u1,
		ClientMessageID: &clientMsgIDLoss,
		MessageType:     "text",
		Content:         "Message where echo was lost",
	}

	isNewLoss2, err := chatRepo.SaveMessage(ctx, reconnectMsgLoss)
	if err != nil {
		t.Fatalf("Test 3 reconnect retry failed: %v", err)
	}
	if isNewLoss2 {
		t.Fatalf("Test 3 expected isNew=false for reconnected retry, got true")
	}
	if reconnectMsgLoss.ID != msgLoss.ID {
		t.Fatalf("Test 3 expected authoritative original server ID %d, got %d", msgLoss.ID, reconnectMsgLoss.ID)
	}

	// =========================================================================
	// Test 4 — Same client_message_id but different content -> reject/conflict
	// =========================================================================
	clientMsgIDTamper := fmt.Sprintf("uuid-tamper-%d", now)
	origMsg := &models.Message{
		ConversationID:  convA,
		SenderID:        u1,
		ClientMessageID: &clientMsgIDTamper,
		MessageType:     "text",
		Content:         "Original Immutable Content",
	}

	isNewOrig, err := chatRepo.SaveMessage(ctx, origMsg)
	if err != nil || !isNewOrig {
		t.Fatalf("Test 4 initial save failed: isNew=%v, err=%v", isNewOrig, err)
	}

	tamperedMsg := &models.Message{
		ConversationID:  convA,
		SenderID:        u1,
		ClientMessageID: &clientMsgIDTamper,
		MessageType:     "text",
		Content:         "Altered Tampered Content",
	}

	_, err = chatRepo.SaveMessage(ctx, tamperedMsg)
	if err == nil {
		t.Fatalf("Test 4 expected conflict error for altered content, got nil")
	}
	if !errors.Is(err, repository.ErrClientMessageIDConflict) {
		t.Fatalf("Test 4 expected ErrClientMessageIDConflict, got %v", err)
	}

	// Verify original message in DB was NOT modified:
	var dbContent string
	_ = db.QueryRow(`SELECT content FROM messages WHERE id = $1`, origMsg.ID).Scan(&dbContent)
	if dbContent != "Original Immutable Content" {
		t.Fatalf("Test 4 original content modified! Expected 'Original Immutable Content', got '%s'", dbContent)
	}

	// =========================================================================
	// Test 5 — Same client_message_id in another conversation
	// =========================================================================
	// Scope is UNIQUE(conversation_id, sender_id, client_message_id).
	// Using same clientMsgID1 in convB must succeed as a separate row for convB.
	msgInConvB := &models.Message{
		ConversationID:  convB,
		SenderID:        u1,
		ClientMessageID: &clientMsgID1,
		MessageType:     "text",
		Content:         "Same UUID in another conversation",
	}

	isNewConvB, err := chatRepo.SaveMessage(ctx, msgInConvB)
	if err != nil {
		t.Fatalf("Test 5 failed: %v", err)
	}
	if !isNewConvB {
		t.Fatalf("Test 5 expected isNew=true in separate conversation, got false")
	}
	if msgInConvB.ID == msg1.ID {
		t.Fatalf("Test 5 expected unique server ID for convB, got same as convA")
	}

	// =========================================================================
	// Test 6 — Delta sync: response contains same client_message_id
	// =========================================================================
	clientMsgIDDelta := fmt.Sprintf("uuid-delta-%d", now)
	msgDelta := &models.Message{
		ConversationID:  convA,
		SenderID:        u1,
		ClientMessageID: &clientMsgIDDelta,
		MessageType:     "text",
		Content:         "Delta sync with client ID",
	}

	_, err = chatRepo.SaveMessage(ctx, msgDelta)
	if err != nil {
		t.Fatalf("Test 6 save failed: %v", err)
	}

	// Delta sync query for messages since msg1.ID
	deltaMsgs, _, err := chatRepo.GetConversationMessages(ctx, convA, u1, 10, 0, msg1.ID)
	if err != nil {
		t.Fatalf("Test 6 delta sync fetch failed: %v", err)
	}

	found := false
	for _, m := range deltaMsgs {
		if m.ID == msgDelta.ID {
			found = true
			if m.ClientMessageID == nil || *m.ClientMessageID != clientMsgIDDelta {
				t.Fatalf("Test 6 expected client_message_id '%s', got %v", clientMsgIDDelta, m.ClientMessageID)
			}
		}
	}
	if !found {
		t.Fatalf("Test 6 message %d not found in delta sync response", msgDelta.ID)
	}

	// =========================================================================
	// Test 7 — Legacy message: client_message_id = NULL
	// =========================================================================
	legacyMsg := &models.Message{
		ConversationID:  convA,
		SenderID:        u1,
		ClientMessageID: nil, // Legacy client didn't supply client_message_id
		MessageType:     "text",
		Content:         "Legacy message without client_message_id",
	}

	isNewLegacy, err := chatRepo.SaveMessage(ctx, legacyMsg)
	if err != nil || !isNewLegacy {
		t.Fatalf("Test 7 legacy save failed: isNew=%v, err=%v", isNewLegacy, err)
	}

	historyMsgs, _, err := chatRepo.GetConversationMessages(ctx, convA, u1, 20, 0, 0)
	if err != nil {
		t.Fatalf("Test 7 history fetch failed: %v", err)
	}

	foundLegacy := false
	for _, m := range historyMsgs {
		if m.ID == legacyMsg.ID {
			foundLegacy = true
			if m.ClientMessageID != nil {
				t.Fatalf("Test 7 expected client_message_id=nil for legacy message, got %v", *m.ClientMessageID)
			}
		}
	}
	if !foundLegacy {
		t.Fatalf("Test 7 legacy message %d not found in history response", legacyMsg.ID)
	}
}
