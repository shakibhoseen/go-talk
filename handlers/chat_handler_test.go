package handlers_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"go-talk/handlers"
	"go-talk/models"
	"go-talk/pkg/token"
	"go-talk/repository"
	"go-talk/service"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
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
	defer func() {
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1, $2)`, u1, u2)
	}()

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

func TestWatermarkCompleteUsersReturned(t *testing.T) {
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

	// 1. Create 6 users: Alice (sender) + 5 readers (r1..r5)
	var aliceID int
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Alice", fmt.Sprintf("alice_wm_%d@test.com", now), "hash").Scan(&aliceID)
	if err != nil {
		t.Fatalf("Failed to create Alice: %v", err)
	}

	readerIDs := make([]int, 5)
	for i := 0; i < 5; i++ {
		err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
			fmt.Sprintf("Reader %d", i+1), fmt.Sprintf("reader_%d_%d@test.com", i+1, now), "hash").Scan(&readerIDs[i])
		if err != nil {
			t.Fatalf("Failed to create reader %d: %v", i+1, err)
		}
	}
	allUserIDs := append([]int{aliceID}, readerIDs...)
	defer db.Exec(`DELETE FROM users WHERE id = ANY($1)`, pq.Array(allUserIDs))

	// Generate JWT for Alice
	aliceToken, err := tokenMaker.CreateToken(aliceID, "alice@test.com", 24*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create alice token: %v", err)
	}

	// 2. Create group conversation
	convID, err := chatRepo.CreateGroupConversation(ctx, "Big Group", aliceID, allUserIDs)
	if err != nil {
		t.Fatalf("Failed to create group: %v", err)
	}
	defer db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)

	// 3. Alice sends a message
	cMsg := fmt.Sprintf("msg-wm-%d", now)
	msg := &models.Message{
		ConversationID:  convID,
		SenderID:        aliceID,
		MessageType:     "text",
		Content:         "Watermark test message",
		ClientMessageID: &cMsg,
	}
	if _, err := chatRepo.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("Failed to save message: %v", err)
	}

	// 4. All 5 readers read up to msg.ID
	for _, rID := range readerIDs {
		_ = chatRepo.UpdateLastReadWatermark(ctx, convID, rID, msg.ID)
	}

	// 5. Call GET /conversations/{id}/messages
	req := httptest.NewRequest("GET", fmt.Sprintf("/conversations/%s/messages", convID), nil)
	req.SetPathValue("id", convID)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	rec := httptest.NewRecorder()

	chatHandler.GetMessages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", rec.Code)
	}

	var resp struct {
		Watermarks map[string]models.MessageWatermark `json:"watermarks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	wmKey := fmt.Sprintf("%d", msg.ID)
	wm, ok := resp.Watermarks[wmKey]
	if !ok {
		t.Fatalf("Expected watermark for message %d, but none returned", msg.ID)
	}

	if wm.Count != 6 {
		t.Fatalf("Expected watermark Count = 6 (Alice + 5 readers), got %d", wm.Count)
	}

	// VERIFY: All 6 users returned without truncation (was previously truncated to max 3)
	if len(wm.Users) != 6 {
		t.Fatalf("Fix 3 verification failed: expected 6 complete watermark users, got %d", len(wm.Users))
	}

	returnedUserIDs := make(map[int]bool)
	for _, u := range wm.Users {
		returnedUserIDs[u.UserID] = true
	}
	if !returnedUserIDs[aliceID] {
		t.Fatalf("Expected Alice in watermark users list")
	}
	for _, rID := range readerIDs {
		if !returnedUserIDs[rID] {
			t.Fatalf("Expected reader %d in watermark users list", rID)
		}
	}
}

func TestCreateDirectChatHandler(t *testing.T) {
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

	now := time.Now().UnixNano()

	var u1, u2 int
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Alice", fmt.Sprintf("alice_%d@test.com", now), "hash").Scan(&u1)
	if err != nil {
		t.Fatalf("Failed to create u1: %v", err)
	}
	defer func() {
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1, $2)`, u1, u2)
	}()

	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Bob", fmt.Sprintf("bob_%d@test.com", now+1), "hash").Scan(&u2)
	if err != nil {
		t.Fatalf("Failed to create u2: %v", err)
	}

	aliceToken, err := tokenMaker.CreateToken(u1, "alice@test.com", 24*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create alice token: %v", err)
	}

	var firstConvID string
	defer func() {
		if firstConvID != "" {
			db.Exec(`DELETE FROM conversations WHERE id = $1`, firstConvID)
		}
	}()

	t.Run("Valid target user returns new conversation ID", func(t *testing.T) {
		body := fmt.Sprintf(`{"target_user_id": %d}`, u2)
		req := httptest.NewRequest(http.MethodPost, "/conversations/direct", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		rr := httptest.NewRecorder()

		chatHandler.CreateDirectChat(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
		}

		var resp map[string]any
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		convID, ok := resp["conversation_id"].(string)
		if !ok || convID == "" {
			t.Fatalf("Expected non-empty conversation_id in response, got %v", resp)
		}
		firstConvID = convID
	})

	t.Run("Existing direct conversation returns same conversation ID", func(t *testing.T) {
		body := fmt.Sprintf(`{"target_user_id": %d}`, u2)
		req := httptest.NewRequest(http.MethodPost, "/conversations/direct", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		rr := httptest.NewRecorder()

		chatHandler.CreateDirectChat(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
		}

		var resp map[string]any
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		convID := resp["conversation_id"].(string)
		if convID != firstConvID {
			t.Fatalf("Expected same conversation_id %s, got %s", firstConvID, convID)
		}
	})

	t.Run("Self target returns 400 Bad Request", func(t *testing.T) {
		body := fmt.Sprintf(`{"target_user_id": %d}`, u1)
		req := httptest.NewRequest(http.MethodPost, "/conversations/direct", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		rr := httptest.NewRecorder()

		chatHandler.CreateDirectChat(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("Expected 400 Bad Request for self target, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Invalid target user ID returns 400 Bad Request", func(t *testing.T) {
		body := `{"target_user_id": 0}`
		req := httptest.NewRequest(http.MethodPost, "/conversations/direct", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		rr := httptest.NewRecorder()

		chatHandler.CreateDirectChat(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("Expected 400 Bad Request for target_user_id <= 0, got %d", rr.Code)
		}
	})

	t.Run("Unauthorized request rejected with 401", func(t *testing.T) {
		body := fmt.Sprintf(`{"target_user_id": %d}`, u2)
		req := httptest.NewRequest(http.MethodPost, "/conversations/direct", strings.NewReader(body))
		// No bearer token
		rr := httptest.NewRecorder()

		chatHandler.CreateDirectChat(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("Expected 401 Unauthorized, got %d", rr.Code)
		}
	})
}

func TestCreateGroupChatHandler(t *testing.T) {
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

	now := time.Now().UnixNano()

	var uCreator, uMember1, uMember2 int
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Creator", fmt.Sprintf("creator_%d@test.com", now), "hash").Scan(&uCreator)
	if err != nil {
		t.Fatalf("Failed to create creator: %v", err)
	}

	defer func() {
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1, $2, $3)`, uCreator, uMember1, uMember2)
	}()

	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member 1", fmt.Sprintf("m1_group_%d@test.com", now+1), "hash").Scan(&uMember1)
	if err != nil {
		t.Fatalf("Failed to create member 1: %v", err)
	}

	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member 2", fmt.Sprintf("m2_group_%d@test.com", now+2), "hash").Scan(&uMember2)
	if err != nil {
		t.Fatalf("Failed to create member 2: %v", err)
	}

	creatorToken, err := tokenMaker.CreateToken(uCreator, "creator@test.com", 24*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create creator token: %v", err)
	}

	var createdConvID string
	defer func() {
		if createdConvID != "" {
			_, _ = db.Exec(`DELETE FROM conversations WHERE id = $1`, createdConvID)
		}
	}()

	t.Run("Valid group creation returns 201 Created with conversation ID", func(t *testing.T) {
		body := fmt.Sprintf(`{"title": "Golang Enthusiasts", "member_ids": [%d, %d]}`, uMember1, uMember2)
		req := httptest.NewRequest(http.MethodPost, "/conversations/group", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+creatorToken)
		rr := httptest.NewRecorder()

		chatHandler.CreateGroupChat(rr, req)

		if rr.Code != http.StatusCreated {
			t.Fatalf("Expected 201 Created, got %d: %s", rr.Code, rr.Body.String())
		}

		var resp map[string]any
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		convID, ok := resp["conversation_id"].(string)
		if !ok || convID == "" {
			t.Fatalf("Expected non-empty conversation_id in response, got %v", resp)
		}
		createdConvID = convID

		if resp["title"] != "Golang Enthusiasts" {
			t.Errorf("Expected title 'Golang Enthusiasts', got %v", resp["title"])
		}

		// Verify roles in DB: creator should be 'admin', members should be 'member'
		var creatorRole string
		_ = db.QueryRow(`SELECT role FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`, convID, uCreator).Scan(&creatorRole)
		if creatorRole != "admin" {
			t.Errorf("Expected creator role 'admin', got '%s'", creatorRole)
		}

		var memberRole string
		_ = db.QueryRow(`SELECT role FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`, convID, uMember1).Scan(&memberRole)
		if memberRole != "member" {
			t.Errorf("Expected member role 'member', got '%s'", memberRole)
		}
	})

	t.Run("Empty group title returns 400 Bad Request", func(t *testing.T) {
		body := fmt.Sprintf(`{"title": "   ", "member_ids": [%d]}`, uMember1)
		req := httptest.NewRequest(http.MethodPost, "/conversations/group", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+creatorToken)
		rr := httptest.NewRecorder()

		chatHandler.CreateGroupChat(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("Expected 400 Bad Request for empty title, got %d", rr.Code)
		}
	})

	t.Run("Empty member IDs returns 400 Bad Request", func(t *testing.T) {
		body := `{"title": "Empty Members Group", "member_ids": []}`
		req := httptest.NewRequest(http.MethodPost, "/conversations/group", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+creatorToken)
		rr := httptest.NewRecorder()

		chatHandler.CreateGroupChat(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("Expected 400 Bad Request for empty member_ids, got %d", rr.Code)
		}
	})

	t.Run("Only creator in member IDs returns 400 Bad Request", func(t *testing.T) {
		body := fmt.Sprintf(`{"title": "Solo Group", "member_ids": [%d]}`, uCreator)
		req := httptest.NewRequest(http.MethodPost, "/conversations/group", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+creatorToken)
		rr := httptest.NewRecorder()

		chatHandler.CreateGroupChat(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("Expected 400 Bad Request when only creator is in member_ids, got %d", rr.Code)
		}
	})

	t.Run("Non-existent member ID returns 400 Bad Request", func(t *testing.T) {
		body := `{"title": "Ghost Group", "member_ids": [9999999]}`
		req := httptest.NewRequest(http.MethodPost, "/conversations/group", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+creatorToken)
		rr := httptest.NewRecorder()

		chatHandler.CreateGroupChat(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("Expected 400 Bad Request for non-existent member, got %d", rr.Code)
		}
	})

	t.Run("Unauthenticated request returns 401 Unauthorized", func(t *testing.T) {
		body := fmt.Sprintf(`{"title": "No Auth Group", "member_ids": [%d]}`, uMember1)
		req := httptest.NewRequest(http.MethodPost, "/conversations/group", strings.NewReader(body))
		rr := httptest.NewRecorder()

		chatHandler.CreateGroupChat(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("Expected 401 Unauthorized, got %d", rr.Code)
		}
	})
}

func TestGetConversationMembersHandler(t *testing.T) {
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
	tokenMaker, _ := token.NewJWTMaker("test_secret_key_12345678901234567890123456789012")
	authSvc := service.NewAuthService(userRepo, tokenMaker)
	chatSvc := service.NewChatService(chatRepo)
	chatHandler := handlers.NewChatHandler(chatSvc, authSvc)

	now := time.Now().UnixNano()
	var uAdmin, uMember, uNonMember int
	defer func() {
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1, $2, $3)`, uAdmin, uMember, uNonMember)
	}()

	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash, bio) VALUES ($1, $2, $3, $4) RETURNING id`,
		"Admin User", fmt.Sprintf("admin_gm_%d@test.com", now), "hash", "Admin Bio").Scan(&uAdmin)
	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash, bio) VALUES ($1, $2, $3, $4) RETURNING id`,
		"Member User", fmt.Sprintf("member_gm_%d@test.com", now+1), "hash", "Member Bio").Scan(&uMember)
	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash, bio) VALUES ($1, $2, $3, $4) RETURNING id`,
		"NonMember User", fmt.Sprintf("nonmember_gm_%d@test.com", now+2), "hash", "NonMember Bio").Scan(&uNonMember)

	var convID string
	defer func() {
		if convID != "" {
			_, _ = db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)
		}
	}()

	_ = db.QueryRow(`INSERT INTO conversations (type, title) VALUES ('group', 'Members Test Group') RETURNING id`).Scan(&convID)
	_, _ = db.Exec(`INSERT INTO conversation_members (conversation_id, user_id, role) VALUES ($1, $2, 'admin'), ($1, $3, 'member')`,
		convID, uAdmin, uMember)

	memberToken, _ := tokenMaker.CreateToken(uMember, "m@test.com", time.Hour)
	nonMemberToken, _ := tokenMaker.CreateToken(uNonMember, "nm@test.com", time.Hour)

	t.Run("Authenticated member can get members with full profiles", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/conversations/%s/members", convID), nil)
		req.SetPathValue("id", convID)
		req.Header.Set("Authorization", "Bearer "+memberToken)
		rr := httptest.NewRecorder()

		chatHandler.GetConversationMembers(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
		}

		var members []models.ConversationMemberProfile
		if err := json.NewDecoder(rr.Body).Decode(&members); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if len(members) != 2 {
			t.Fatalf("Expected 2 members, got %d", len(members))
		}

		for _, m := range members {
			if m.UserID == uAdmin {
				if m.Role != "admin" {
					t.Errorf("Expected admin role, got %s", m.Role)
				}
				if m.Bio == nil || *m.Bio != "Admin Bio" {
					t.Errorf("Expected bio 'Admin Bio', got %v", m.Bio)
				}
			}
			if m.UserID == uMember {
				if m.Role != "member" {
					t.Errorf("Expected member role, got %s", m.Role)
				}
				if m.Bio == nil || *m.Bio != "Member Bio" {
					t.Errorf("Expected bio 'Member Bio', got %v", m.Bio)
				}
			}
		}
	})

	t.Run("Non-member request returns 403 Forbidden", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/conversations/%s/members", convID), nil)
		req.SetPathValue("id", convID)
		req.Header.Set("Authorization", "Bearer "+nonMemberToken)
		rr := httptest.NewRecorder()

		chatHandler.GetConversationMembers(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("Expected 403 Forbidden, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Unauthenticated request returns 401 Unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/conversations/%s/members", convID), nil)
		req.SetPathValue("id", convID)
		rr := httptest.NewRecorder()

		chatHandler.GetConversationMembers(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("Expected 401 Unauthorized, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Invalid conversation ID returns 400 Bad Request", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/conversations/invalid-uuid/members", nil)
		req.SetPathValue("id", "invalid-uuid")
		req.Header.Set("Authorization", "Bearer "+memberToken)
		rr := httptest.NewRecorder()

		chatHandler.GetConversationMembers(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("Expected 400 Bad Request, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Nonexistent conversation ID returns 404 Not Found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/conversations/00000000-0000-0000-0000-000000000000/members", nil)
		req.SetPathValue("id", "00000000-0000-0000-0000-000000000000")
		req.Header.Set("Authorization", "Bearer "+memberToken)
		rr := httptest.NewRecorder()

		chatHandler.GetConversationMembers(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("Expected 404 Not Found, got %d: %s", rr.Code, rr.Body.String())
		}
	})
}

func TestAddMemberHandler(t *testing.T) {
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
	tokenMaker, _ := token.NewJWTMaker("test_secret_key_12345678901234567890123456789012")
	authSvc := service.NewAuthService(userRepo, tokenMaker)
	chatSvc := service.NewChatService(chatRepo)
	chatHandler := handlers.NewChatHandler(chatSvc, authSvc)

	now := time.Now().UnixNano()
	var uAdmin, uMember, uNonMember, uNewUser int
	defer func() {
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1, $2, $3, $4)`, uAdmin, uMember, uNonMember, uNewUser)
	}()

	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Admin User", fmt.Sprintf("admin_am_%d@test.com", now), "hash").Scan(&uAdmin)
	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member User", fmt.Sprintf("member_am_%d@test.com", now+1), "hash").Scan(&uMember)
	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"NonMember User", fmt.Sprintf("nonmember_am_%d@test.com", now+2), "hash").Scan(&uNonMember)
	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"New Candidate", fmt.Sprintf("new_cand_%d@test.com", now+3), "hash").Scan(&uNewUser)

	var convID string
	defer func() {
		if convID != "" {
			_, _ = db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)
		}
	}()

	_ = db.QueryRow(`INSERT INTO conversations (type, title) VALUES ('group', 'Add Member Test Group') RETURNING id`).Scan(&convID)
	_, _ = db.Exec(`INSERT INTO conversation_members (conversation_id, user_id, role) VALUES ($1, $2, 'admin'), ($1, $3, 'member')`,
		convID, uAdmin, uMember)

	adminToken, _ := tokenMaker.CreateToken(uAdmin, "a@test.com", time.Hour)
	memberToken, _ := tokenMaker.CreateToken(uMember, "m@test.com", time.Hour)
	nonMemberToken, _ := tokenMaker.CreateToken(uNonMember, "nm@test.com", time.Hour)

	t.Run("Normal member cannot add member (returns 403 Forbidden)", func(t *testing.T) {
		body := fmt.Sprintf(`{"user_id": %d}`, uNewUser)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversations/%s/members", convID), strings.NewReader(body))
		req.SetPathValue("id", convID)
		req.Header.Set("Authorization", "Bearer "+memberToken)
		rr := httptest.NewRecorder()

		chatHandler.AddMember(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("Expected 403 Forbidden, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Non-member requester cannot add member (returns 403 Forbidden)", func(t *testing.T) {
		body := fmt.Sprintf(`{"user_id": %d}`, uNewUser)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversations/%s/members", convID), strings.NewReader(body))
		req.SetPathValue("id", convID)
		req.Header.Set("Authorization", "Bearer "+nonMemberToken)
		rr := httptest.NewRecorder()

		chatHandler.AddMember(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("Expected 403 Forbidden, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Unauthenticated request returns 401 Unauthorized", func(t *testing.T) {
		body := fmt.Sprintf(`{"user_id": %d}`, uNewUser)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversations/%s/members", convID), strings.NewReader(body))
		req.SetPathValue("id", convID)
		rr := httptest.NewRecorder()

		chatHandler.AddMember(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("Expected 401 Unauthorized, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Nonexistent target user returns 400 or 404", func(t *testing.T) {
		body := `{"user_id": 99999999}`
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversations/%s/members", convID), strings.NewReader(body))
		req.SetPathValue("id", convID)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		rr := httptest.NewRecorder()

		chatHandler.AddMember(rr, req)

		if rr.Code != http.StatusBadRequest && rr.Code != http.StatusNotFound {
			t.Fatalf("Expected 400 or 404 for nonexistent target user, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Admin can successfully add new member", func(t *testing.T) {
		body := fmt.Sprintf(`{"user_id": %d}`, uNewUser)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversations/%s/members", convID), strings.NewReader(body))
		req.SetPathValue("id", convID)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		rr := httptest.NewRecorder()

		chatHandler.AddMember(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
		}

		var role string
		err := db.QueryRow(`SELECT role FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`, convID, uNewUser).Scan(&role)
		if err != nil || role != "member" {
			t.Fatalf("Expected role 'member' in DB, got '%s', err: %v", role, err)
		}
	})

	t.Run("Duplicate member returns 409 Conflict (or 400)", func(t *testing.T) {
		body := fmt.Sprintf(`{"user_id": %d}`, uNewUser)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/conversations/%s/members", convID), strings.NewReader(body))
		req.SetPathValue("id", convID)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		rr := httptest.NewRecorder()

		chatHandler.AddMember(rr, req)

		if rr.Code != http.StatusConflict && rr.Code != http.StatusBadRequest {
			t.Fatalf("Expected 409 Conflict or 400 for duplicate member, got %d: %s", rr.Code, rr.Body.String())
		}
	})
}

func TestRemoveMemberHandler(t *testing.T) {
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
	tokenMaker, _ := token.NewJWTMaker("test_secret_key_12345678901234567890123456789012")
	authSvc := service.NewAuthService(userRepo, tokenMaker)
	chatSvc := service.NewChatService(chatRepo)
	chatHandler := handlers.NewChatHandler(chatSvc, authSvc)

	now := time.Now().UnixNano()
	var uAdmin1, uAdmin2, uMember1, uMember2, uNonMember int
	defer func() {
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1, $2, $3, $4, $5)`, uAdmin1, uAdmin2, uMember1, uMember2, uNonMember)
	}()

	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Admin 1", fmt.Sprintf("admin1_rm_%d@test.com", now), "hash").Scan(&uAdmin1)
	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Admin 2", fmt.Sprintf("admin2_rm_%d@test.com", now+1), "hash").Scan(&uAdmin2)
	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member 1", fmt.Sprintf("member1_rm_%d@test.com", now+2), "hash").Scan(&uMember1)
	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member 2", fmt.Sprintf("member2_rm_%d@test.com", now+3), "hash").Scan(&uMember2)
	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"NonMember", fmt.Sprintf("nonmember_rm_%d@test.com", now+4), "hash").Scan(&uNonMember)

	var convID string
	defer func() {
		if convID != "" {
			_, _ = db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)
		}
	}()

	_ = db.QueryRow(`INSERT INTO conversations (type, title) VALUES ('group', 'Remove Member Test Group') RETURNING id`).Scan(&convID)
	_, _ = db.Exec(`INSERT INTO conversation_members (conversation_id, user_id, role) VALUES 
		($1, $2, 'admin'), ($1, $3, 'admin'), ($1, $4, 'member'), ($1, $5, 'member')`,
		convID, uAdmin1, uAdmin2, uMember1, uMember2)

	admin1Token, _ := tokenMaker.CreateToken(uAdmin1, "a1@test.com", time.Hour)
	admin2Token, _ := tokenMaker.CreateToken(uAdmin2, "a2@test.com", time.Hour)
	member1Token, _ := tokenMaker.CreateToken(uMember1, "m1@test.com", time.Hour)
	nonMemberToken, _ := tokenMaker.CreateToken(uNonMember, "nm@test.com", time.Hour)

	t.Run("Normal member cannot remove another member (returns 403 Forbidden)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/conversations/%s/members/%d", convID, uMember2), nil)
		req.SetPathValue("id", convID)
		req.SetPathValue("user_id", fmt.Sprintf("%d", uMember2))
		req.Header.Set("Authorization", "Bearer "+member1Token)
		rr := httptest.NewRecorder()

		chatHandler.RemoveMember(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("Expected 403 Forbidden, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Non-member requester cannot remove member (returns 403 Forbidden)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/conversations/%s/members/%d", convID, uMember2), nil)
		req.SetPathValue("id", convID)
		req.SetPathValue("user_id", fmt.Sprintf("%d", uMember2))
		req.Header.Set("Authorization", "Bearer "+nonMemberToken)
		rr := httptest.NewRecorder()

		chatHandler.RemoveMember(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("Expected 403 Forbidden, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Unauthenticated request returns 401 Unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/conversations/%s/members/%d", convID, uMember2), nil)
		req.SetPathValue("id", convID)
		req.SetPathValue("user_id", fmt.Sprintf("%d", uMember2))
		rr := httptest.NewRecorder()

		chatHandler.RemoveMember(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("Expected 401 Unauthorized, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Remove non-member target returns 404 Not Found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/conversations/%s/members/%d", convID, uNonMember), nil)
		req.SetPathValue("id", convID)
		req.SetPathValue("user_id", fmt.Sprintf("%d", uNonMember))
		req.Header.Set("Authorization", "Bearer "+admin1Token)
		rr := httptest.NewRecorder()

		chatHandler.RemoveMember(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("Expected 404 Not Found, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Normal member can remove self (Leave Group)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/conversations/%s/members/%d", convID, uMember1), nil)
		req.SetPathValue("id", convID)
		req.SetPathValue("user_id", fmt.Sprintf("%d", uMember1))
		req.Header.Set("Authorization", "Bearer "+member1Token)
		rr := httptest.NewRecorder()

		chatHandler.RemoveMember(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
		}

		var exists bool
		_ = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM conversation_members WHERE conversation_id = $1 AND user_id = $2)`, convID, uMember1).Scan(&exists)
		if exists {
			t.Errorf("Expected member 1 to be removed from group")
		}
	})

	t.Run("Admin can remove normal member", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/conversations/%s/members/%d", convID, uMember2), nil)
		req.SetPathValue("id", convID)
		req.SetPathValue("user_id", fmt.Sprintf("%d", uMember2))
		req.Header.Set("Authorization", "Bearer "+admin1Token)
		rr := httptest.NewRecorder()

		chatHandler.RemoveMember(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
		}

		var exists bool
		_ = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM conversation_members WHERE conversation_id = $1 AND user_id = $2)`, convID, uMember2).Scan(&exists)
		if exists {
			t.Errorf("Expected member 2 to be removed from group")
		}
	})

	t.Run("Admin leaves when another admin exists (returns 200 OK)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/conversations/%s/members/%d", convID, uAdmin2), nil)
		req.SetPathValue("id", convID)
		req.SetPathValue("user_id", fmt.Sprintf("%d", uAdmin2))
		req.Header.Set("Authorization", "Bearer "+admin2Token)
		rr := httptest.NewRecorder()

		chatHandler.RemoveMember(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
		}

		var exists bool
		_ = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM conversation_members WHERE conversation_id = $1 AND user_id = $2)`, convID, uAdmin2).Scan(&exists)
		if exists {
			t.Errorf("Expected admin 2 to be removed from group")
		}
	})

	t.Run("Only admin tries to leave returns 403 Forbidden", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/conversations/%s/members/%d", convID, uAdmin1), nil)
		req.SetPathValue("id", convID)
		req.SetPathValue("user_id", fmt.Sprintf("%d", uAdmin1))
		req.Header.Set("Authorization", "Bearer "+admin1Token)
		rr := httptest.NewRecorder()

		chatHandler.RemoveMember(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("Expected 403 Forbidden for only admin leaving, got %d: %s", rr.Code, rr.Body.String())
		}

		var exists bool
		_ = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM conversation_members WHERE conversation_id = $1 AND user_id = $2)`, convID, uAdmin1).Scan(&exists)
		if !exists {
			t.Errorf("Expected only admin 1 to remain in group")
		}
	})

	t.Run("Only-admin protection prevents removing the last remaining admin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/conversations/%s/members/%d", convID, uAdmin1), nil)
		req.SetPathValue("id", convID)
		req.SetPathValue("user_id", fmt.Sprintf("%d", uAdmin1))
		req.Header.Set("Authorization", "Bearer "+admin1Token)
		rr := httptest.NewRecorder()

		chatHandler.RemoveMember(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("Expected 403 Forbidden for removing only admin, got %d: %s", rr.Code, rr.Body.String())
		}
	})
}

func TestUpdateGroupAvatarHandler(t *testing.T) {
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
	tokenMaker, _ := token.NewJWTMaker("test_secret_key_12345678901234567890123456789012")
	authSvc := service.NewAuthService(userRepo, tokenMaker)
	chatSvc := service.NewChatService(chatRepo)
	chatHandler := handlers.NewChatHandler(chatSvc, authSvc)

	now := time.Now().UnixNano()
	var uAdmin, uMember, uNonMember int
	defer func() {
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1, $2, $3)`, uAdmin, uMember, uNonMember)
	}()

	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Admin Avatar", fmt.Sprintf("admin_av_%d@test.com", now), "hash").Scan(&uAdmin)
	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member Avatar", fmt.Sprintf("member_av_%d@test.com", now+1), "hash").Scan(&uMember)
	_ = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"NonMember Avatar", fmt.Sprintf("nonmember_av_%d@test.com", now+2), "hash").Scan(&uNonMember)

	var convID string
	defer func() {
		if convID != "" {
			_, _ = db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)
		}
	}()

	_ = db.QueryRow(`INSERT INTO conversations (type, title) VALUES ('group', 'Avatar Test Group') RETURNING id`).Scan(&convID)
	_, _ = db.Exec(`INSERT INTO conversation_members (conversation_id, user_id, role) VALUES ($1, $2, 'admin'), ($1, $3, 'member')`,
		convID, uAdmin, uMember)

	adminToken, _ := tokenMaker.CreateToken(uAdmin, "a@test.com", time.Hour)
	memberToken, _ := tokenMaker.CreateToken(uMember, "m@test.com", time.Hour)
	nonMemberToken, _ := tokenMaker.CreateToken(uNonMember, "nm@test.com", time.Hour)

	createMultipartReq := func(token string) *http.Request {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, _ := writer.CreateFormFile("avatar", "group_pic.png")
		_, _ = part.Write([]byte("fake png binary data"))
		_ = writer.Close()

		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/conversations/%s/avatar", convID), body)
		req.SetPathValue("id", convID)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return req
	}

	t.Run("Normal member cannot update group avatar (returns 403 Forbidden)", func(t *testing.T) {
		req := createMultipartReq(memberToken)
		rr := httptest.NewRecorder()

		chatHandler.UpdateGroupAvatar(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("Expected 403 Forbidden, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Non-member requester cannot update group avatar (returns 403 Forbidden)", func(t *testing.T) {
		req := createMultipartReq(nonMemberToken)
		rr := httptest.NewRecorder()

		chatHandler.UpdateGroupAvatar(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("Expected 403 Forbidden, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Unauthenticated request returns 401 Unauthorized", func(t *testing.T) {
		req := createMultipartReq("")
		rr := httptest.NewRecorder()

		chatHandler.UpdateGroupAvatar(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("Expected 401 Unauthorized, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Admin can successfully update group avatar", func(t *testing.T) {
		req := createMultipartReq(adminToken)
		rr := httptest.NewRecorder()

		chatHandler.UpdateGroupAvatar(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
		}

		var resp map[string]any
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}
		avatarURL, ok := resp["avatar_url"].(string)
		if !ok || !strings.HasPrefix(avatarURL, "/uploads/groups/") {
			t.Errorf("Expected valid avatar_url starting with /uploads/groups/, got %v", resp["avatar_url"])
		}

		if strings.HasPrefix(avatarURL, "/") {
			_ = os.Remove("." + avatarURL)
		}
	})
}

