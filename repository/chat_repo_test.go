package repository_test

import (
	"context"
	"database/sql"
	"fmt"
	"go-talk/models"
	"go-talk/repository"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func TestDeltaSyncAndPagination(t *testing.T) {
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

	// 1. Setup test users and conversation
	var u1, u2, u3 int
	now := time.Now().UnixNano()
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Test U1", fmt.Sprintf("u1_%d@test.com", now), "hash").Scan(&u1)
	if err != nil {
		t.Fatalf("Failed to create u1: %v", err)
	}
	defer db.Exec(`DELETE FROM users WHERE id IN ($1, $2, $3)`, u1, u2, u3)

	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Test U2", fmt.Sprintf("u2_%d@test.com", now+1), "hash").Scan(&u2)
	if err != nil {
		t.Fatalf("Failed to create u2: %v", err)
	}

	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Test U3 (Unauthorized)", fmt.Sprintf("u3_%d@test.com", now+2), "hash").Scan(&u3)
	if err != nil {
		t.Fatalf("Failed to create u3: %v", err)
	}

	convID, err := chatRepo.GetOrCreateDirectConversation(ctx, u1, u2)
	if err != nil {
		t.Fatalf("Failed to create direct conv: %v", err)
	}
	defer db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)

	// 2. Insert 5 messages from u1
	var msgIDs []int64
	for i := 1; i <= 5; i++ {
		msg := &models.Message{
			ConversationID: convID,
			SenderID:       u1,
			MessageType:    "text",
			Content:        fmt.Sprintf("Message %d", i),
		}
		if err := chatRepo.SaveMessage(ctx, msg); err != nil {
			t.Fatalf("Failed to save message %d: %v", i, err)
		}
		msgIDs = append(msgIDs, msg.ID)
	}

	// 3. Test Security: User 3 (not a member) must be forbidden
	_, _, err = chatRepo.GetConversationMessages(ctx, convID, u3, 10, 0, 0)
	if err == nil || err.Error() != "forbidden: user is not a member of this conversation" {
		t.Fatalf("Expected forbidden error for non-member u3, got: %v", err)
	}

	// 4. Test normal backward pagination (before_id = 0, initial fetch) -> should be DESC
	initialMsgs, hasMore, err := chatRepo.GetConversationMessages(ctx, convID, u1, 10, 0, 0)
	if err != nil {
		t.Fatalf("Failed initial fetch: %v", err)
	}
	if len(initialMsgs) != 5 {
		t.Fatalf("Expected 5 messages, got %d", len(initialMsgs))
	}
	if initialMsgs[0].ID != msgIDs[4] {
		t.Fatalf("Expected newest message first (DESC) in initial fetch, got %d vs %d", initialMsgs[0].ID, msgIDs[4])
	}
	if hasMore {
		t.Fatalf("Expected hasMore=false for 5 items with limit 10")
	}

	// 5. Test delta sync with since_id (e.g. since_id = msgIDs[1], limit = 2)
	// Should return msgIDs[2], msgIDs[3] in ASC order with hasMore = true
	deltaMsgs, hasMoreDelta, err := chatRepo.GetConversationMessages(ctx, convID, u1, 2, 0, msgIDs[1])
	if err != nil {
		t.Fatalf("Failed delta sync page 1: %v", err)
	}
	if len(deltaMsgs) != 2 {
		t.Fatalf("Expected 2 delta messages, got %d", len(deltaMsgs))
	}
	if !hasMoreDelta {
		t.Fatalf("Expected hasMoreDelta=true")
	}
	if deltaMsgs[0].ID != msgIDs[2] || deltaMsgs[1].ID != msgIDs[3] {
		t.Fatalf("Expected ASC order [msgIDs[2], msgIDs[3]], got [%d, %d]", deltaMsgs[0].ID, deltaMsgs[1].ID)
	}

	// 6. Test delta sync page 2 (since_id = deltaMsgs[1].ID, limit = 2)
	// Should return msgIDs[4] with hasMore = false
	deltaMsgsPage2, hasMoreDelta2, err := chatRepo.GetConversationMessages(ctx, convID, u1, 2, 0, deltaMsgs[1].ID)
	if err != nil {
		t.Fatalf("Failed delta sync page 2: %v", err)
	}
	if len(deltaMsgsPage2) != 1 {
		t.Fatalf("Expected 1 delta message on page 2, got %d", len(deltaMsgsPage2))
	}
	if hasMoreDelta2 {
		t.Fatalf("Expected hasMoreDelta2=false on final page")
	}
	if deltaMsgsPage2[0].ID != msgIDs[4] {
		t.Fatalf("Expected msgIDs[4], got %d", deltaMsgsPage2[0].ID)
	}

	// 7. Test delta sync when up to date (since_id = msgIDs[4])
	// Should return 0 messages, hasMore = false
	emptyMsgs, hasMoreEmpty, err := chatRepo.GetConversationMessages(ctx, convID, u1, 2, 0, msgIDs[4])
	if err != nil {
		t.Fatalf("Failed up-to-date delta check: %v", err)
	}
	if len(emptyMsgs) != 0 {
		t.Fatalf("Expected 0 messages when up to date, got %d", len(emptyMsgs))
	}
	if hasMoreEmpty {
		t.Fatalf("Expected hasMore=false when up to date")
	}
}
