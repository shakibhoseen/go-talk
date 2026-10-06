package handlers_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"go-talk/handlers"
	"go-talk/models"
	"go-talk/pkg/token"
	"go-talk/repository"
	"go-talk/service"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func TestGetMessagesDeltaSyncHandler(t *testing.T) {
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

	userRepo := repository.NewUserRepository(db)
	chatRepo := repository.NewChatRepository(db)
	tokenMaker, err := token.NewJWTMaker("test_secret_key_12345678901234567890123456789012")
	if err != nil {
		t.Fatalf("Failed to create token maker: %v", err)
	}

	authSvc := service.NewAuthService(userRepo, tokenMaker)
	chatSvc := service.NewChatService(chatRepo)
	chatHandler := handlers.NewChatHandler(chatSvc, authSvc)

	ctx := context.Background()
	now := time.Now().UnixNano()

	var u1, u2 int
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Alice", fmt.Sprintf("alice_%d@test.com", now), "hash").Scan(&u1)
	if err != nil {
		t.Fatalf("Failed to create u1: %v", err)
	}
	defer db.Exec(`DELETE FROM users WHERE id IN ($1, $2)`, u1, u2)

	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Bob", fmt.Sprintf("bob_%d@test.com", now+1), "hash").Scan(&u2)
	if err != nil {
		t.Fatalf("Failed to create u2: %v", err)
	}

	aliceToken, err := tokenMaker.CreateToken(u1, "alice@test.com", 24*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create alice token: %v", err)
	}

	convID, err := chatRepo.GetOrCreateDirectConversation(ctx, u1, u2)
	if err != nil {
		t.Fatalf("Failed to create direct conv: %v", err)
	}
	defer db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)

	// Insert 3 messages from Alice
	var msgIDs []int64
	for i := 1; i <= 3; i++ {
		msg := &models.Message{
			ConversationID: convID,
			SenderID:       u1,
			MessageType:    "text",
			Content:        fmt.Sprintf("Msg %d", i),
		}
		if _, err := chatRepo.SaveMessage(ctx, msg); err != nil {
			t.Fatalf("Failed to save msg %d: %v", i, err)
		}
		msgIDs = append(msgIDs, msg.ID)
	}

	// Bob reads up to msgIDs[1]
	_ = chatRepo.UpdateLastReadWatermark(ctx, convID, u2, msgIDs[1])

	// 1. Test mutually exclusive parameters: before_id + since_id -> 400 Bad Request
	req := httptest.NewRequest("GET", fmt.Sprintf("/conversations/%s/messages?before_id=10&since_id=5", convID), nil)
	req.SetPathValue("id", convID)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	rec := httptest.NewRecorder()

	chatHandler.GetMessages(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400 Bad Request for both before_id and since_id, got %d", rec.Code)
	}

	// 2. Test delta sync with since_id = msgIDs[0], limit = 1
	req = httptest.NewRequest("GET", fmt.Sprintf("/conversations/%s/messages?since_id=%d&limit=1", convID, msgIDs[0]), nil)
	req.SetPathValue("id", convID)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	rec = httptest.NewRecorder()

	chatHandler.GetMessages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Messages    []models.Message                  `json:"messages"`
		Watermarks  map[string]models.MessageWatermark `json:"watermarks"`
		HasMore     bool                              `json:"has_more"`
		NextSinceID int64                             `json:"next_since_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if len(resp.Messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(resp.Messages))
	}
	if resp.Messages[0].ID != msgIDs[1] {
		t.Fatalf("Expected msgIDs[1], got %d", resp.Messages[0].ID)
	}
	if !resp.HasMore {
		t.Fatalf("Expected has_more = true")
	}
	if resp.NextSinceID != msgIDs[1] {
		t.Fatalf("Expected next_since_id = %d, got %d", msgIDs[1], resp.NextSinceID)
	}

	// 3. Test delta sync when messages is empty (since_id = msgIDs[2])
	// Must still return latest member watermarks!
	req = httptest.NewRequest("GET", fmt.Sprintf("/conversations/%s/messages?since_id=%d&limit=10", convID, msgIDs[2]), nil)
	req.SetPathValue("id", convID)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	rec = httptest.NewRecorder()

	chatHandler.GetMessages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", rec.Code)
	}

	var emptyResp struct {
		Messages    []models.Message                  `json:"messages"`
		Watermarks  map[string]models.MessageWatermark `json:"watermarks"`
		HasMore     bool                              `json:"has_more"`
		NextSinceID int64                             `json:"next_since_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &emptyResp); err != nil {
		t.Fatalf("Failed to unmarshal empty response: %v", err)
	}

	if len(emptyResp.Messages) != 0 {
		t.Fatalf("Expected 0 messages, got %d", len(emptyResp.Messages))
	}
	if emptyResp.HasMore {
		t.Fatalf("Expected has_more = false")
	}
	if emptyResp.NextSinceID != msgIDs[2] {
		t.Fatalf("Expected next_since_id = %d, got %d", msgIDs[2], emptyResp.NextSinceID)
	}
	// Verify Bob's watermark is in watermarks map even though messages is empty!
	watermarkKey := fmt.Sprintf("%d", msgIDs[1])
	if wm, ok := emptyResp.Watermarks[watermarkKey]; !ok || wm.Count == 0 {
		t.Fatalf("Expected watermark for %s with count > 0, got %+v", watermarkKey, emptyResp.Watermarks)
	}

	// 4. Test client_message_id in HTTP GET messages response
	clientUUID := "c7b39a48-4fa3-4cb5-8292-80eafe14c772"
	idempMsg := &models.Message{
		ConversationID:  convID,
		SenderID:        u1,
		ClientMessageID: &clientUUID,
		MessageType:     "text",
		Content:         "Idempotency HTTP test",
	}
	if _, err := chatRepo.SaveMessage(ctx, idempMsg); err != nil {
		t.Fatalf("Failed to save idempMsg: %v", err)
	}

	req = httptest.NewRequest("GET", fmt.Sprintf("/conversations/%s/messages?since_id=%d&limit=10", convID, msgIDs[2]), nil)
	req.SetPathValue("id", convID)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	rec = httptest.NewRecorder()

	chatHandler.GetMessages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", rec.Code)
	}

	var idempResp struct {
		Messages []models.Message `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &idempResp); err != nil {
		t.Fatalf("Failed to unmarshal idemp response: %v", err)
	}
	if len(idempResp.Messages) == 0 {
		t.Fatalf("Expected messages, got 0")
	}
	foundIdemp := false
	for _, m := range idempResp.Messages {
		if m.ID == idempMsg.ID {
			foundIdemp = true
			if m.ClientMessageID == nil || *m.ClientMessageID != clientUUID {
				t.Fatalf("Expected client_message_id %s, got %v", clientUUID, m.ClientMessageID)
			}
		}
	}
	if !foundIdemp {
		t.Fatalf("Expected to find idempMsg in response")
	}
}
