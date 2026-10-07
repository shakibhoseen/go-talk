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
	defer func() {
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1, $2, $3)`, u1, u2, u3)
	}()

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
		if _, err := chatRepo.SaveMessage(ctx, msg); err != nil {
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

func TestAckSeenAuthorizationAndValidation(t *testing.T) {
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

	var u1, u2, u3 int
	now := time.Now().UnixNano()
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Auth U1", fmt.Sprintf("auth_u1_%d@test.com", now), "hash").Scan(&u1)
	if err != nil {
		t.Fatalf("Failed to create u1: %v", err)
	}
	defer func() {
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1, $2, $3)`, u1, u2, u3)
	}()

	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Auth U2", fmt.Sprintf("auth_u2_%d@test.com", now+1), "hash").Scan(&u2)
	if err != nil {
		t.Fatalf("Failed to create u2: %v", err)
	}

	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Auth U3", fmt.Sprintf("auth_u3_%d@test.com", now+2), "hash").Scan(&u3)
	if err != nil {
		t.Fatalf("Failed to create u3: %v", err)
	}

	convID, err := chatRepo.GetOrCreateDirectConversation(ctx, u1, u2)
	if err != nil {
		t.Fatalf("Failed to create conv: %v", err)
	}
	defer db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)

	// Insert message from u1
	msg := &models.Message{
		ConversationID: convID,
		SenderID:       u1,
		MessageType:    "text",
		Content:        "Auth test message",
	}
	if _, err := chatRepo.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("Failed to save msg: %v", err)
	}

	// 1. Test IsConversationMember
	isMember1, err := chatRepo.IsConversationMember(ctx, convID, u1)
	if err != nil || !isMember1 {
		t.Fatalf("Expected u1 to be member, got %v, err=%v", isMember1, err)
	}
	isMember2, err := chatRepo.IsConversationMember(ctx, convID, u2)
	if err != nil || !isMember2 {
		t.Fatalf("Expected u2 to be member, got %v, err=%v", isMember2, err)
	}
	isMember3, err := chatRepo.IsConversationMember(ctx, convID, u3)
	if err != nil || isMember3 {
		t.Fatalf("Expected non-member u3 to NOT be member, got %v, err=%v", isMember3, err)
	}

	// 2. Test GetMessageSenderInConversation
	senderID, err := chatRepo.GetMessageSenderInConversation(ctx, convID, msg.ID)
	if err != nil || senderID != u1 {
		t.Fatalf("Expected sender %d, got %d, err=%v", u1, senderID, err)
	}

	// 2b. Message from nonexistent message ID
	_, err = chatRepo.GetMessageSenderInConversation(ctx, convID, 999999999)
	if err == nil {
		t.Fatalf("Expected error for nonexistent message, got nil")
	}

	// 2c. Message ID exceeding conversation head
	_, err = chatRepo.GetMessageSenderInConversation(ctx, convID, msg.ID+100)
	if err == nil {
		t.Fatalf("Expected error for message beyond last_message_id, got nil")
	}

	// 3. MarkMessagesSeenUpto: non-member cannot create receipts
	err = chatRepo.MarkMessagesSeenUpto(ctx, convID, u3, msg.ID)
	if err != nil {
		t.Fatalf("MarkMessagesSeenUpto unexpected error: %v", err)
	}
	var count int
	_ = db.QueryRow(`SELECT count(*) FROM message_receipts WHERE message_id = $1 AND user_id = $2`, msg.ID, u3).Scan(&count)
	if count != 0 {
		t.Fatalf("Expected 0 receipts for non-member u3, got %d", count)
	}

	// 4. MarkMessagesSeenUpto: author cannot seen-mark own message
	err = chatRepo.MarkMessagesSeenUpto(ctx, convID, u1, msg.ID)
	if err != nil {
		t.Fatalf("MarkMessagesSeenUpto unexpected error: %v", err)
	}
	_ = db.QueryRow(`SELECT count(*) FROM message_receipts WHERE message_id = $1 AND user_id = $2`, msg.ID, u1).Scan(&count)
	if count != 0 {
		t.Fatalf("Expected 0 receipts for author u1, got %d", count)
	}

	// 5. MarkMessagesSeenUpto: valid recipient u2 marks seen
	err = chatRepo.MarkMessagesSeenUpto(ctx, convID, u2, msg.ID)
	if err != nil {
		t.Fatalf("MarkMessagesSeenUpto failed for u2: %v", err)
	}
	_ = db.QueryRow(`SELECT count(*) FROM message_receipts WHERE message_id = $1 AND user_id = $2 AND status = 'seen'`, msg.ID, u2).Scan(&count)
	if count != 1 {
		t.Fatalf("Expected 1 seen receipt for recipient u2, got %d", count)
	}
}

func TestGroupOfflineDeliverySync(t *testing.T) {
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
	now := time.Now().UnixNano()

	// 1. Create members A, B, C and unrelated user D
	var uA, uB, uC, uD int
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member A", fmt.Sprintf("ua_%d@test.com", now), "hash").Scan(&uA)
	if err != nil {
		t.Fatalf("Failed to create uA: %v", err)
	}
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member B", fmt.Sprintf("ub_%d@test.com", now+1), "hash").Scan(&uB)
	if err != nil {
		t.Fatalf("Failed to create uB: %v", err)
	}
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member C", fmt.Sprintf("uc_%d@test.com", now+2), "hash").Scan(&uC)
	if err != nil {
		t.Fatalf("Failed to create uC: %v", err)
	}
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Unrelated D", fmt.Sprintf("ud_%d@test.com", now+3), "hash").Scan(&uD)
	if err != nil {
		t.Fatalf("Failed to create uD: %v", err)
	}
	defer db.Exec(`DELETE FROM users WHERE id IN ($1, $2, $3, $4)`, uA, uB, uC, uD)

	// 2. Create group with A, B, C
	convID, err := chatRepo.CreateGroupConversation(ctx, "Test Delivery Group", uA, []int{uA, uB, uC})
	if err != nil {
		t.Fatalf("Failed to create group conv: %v", err)
	}
	defer db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)

	clientMsgA := fmt.Sprintf("msg-a-%d", now)
	clientMsgB := fmt.Sprintf("msg-b-%d", now)

	// A sends message 50
	msgA := &models.Message{
		ConversationID:  convID,
		SenderID:        uA,
		MessageType:     "text",
		Content:         "Message from A",
		ClientMessageID: &clientMsgA,
	}
	if _, err := chatRepo.SaveMessage(ctx, msgA); err != nil {
		t.Fatalf("Failed to save msgA: %v", err)
	}

	// B sends message 51
	msgB := &models.Message{
		ConversationID:  convID,
		SenderID:        uB,
		MessageType:     "text",
		Content:         "Message from B",
		ClientMessageID: &clientMsgB,
	}
	if _, err := chatRepo.SaveMessage(ctx, msgB); err != nil {
		t.Fatalf("Failed to save msgB: %v", err)
	}

	// Both A and B are online and delivered up to msgB.ID
	_ = chatRepo.UpdateLastDeliveredWatermark(ctx, convID, uA, msgB.ID)
	_ = chatRepo.UpdateLastDeliveredWatermark(ctx, convID, uB, msgB.ID)

	// C is offline (C's last_delivered_message_id remains 0)
	var cWatermark int64
	_ = db.QueryRow(`SELECT last_delivered_message_id FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`, convID, uC).Scan(&cWatermark)
	if cWatermark != 0 {
		t.Fatalf("Expected C's initial delivered watermark to be 0, got %d", cWatermark)
	}

	// C reconnects: SyncUserDelivery(ctx, uC)
	results, err := chatRepo.SyncUserDelivery(ctx, uC)
	if err != nil {
		t.Fatalf("SyncUserDelivery failed: %v", err)
	}

	// Verify both senders A and B receive delivered notification for their messages!
	// A gets delivered for msgA.ID, B gets delivered for msgB.ID
	deliveredMap := make(map[int]int64)
	for _, r := range results {
		if r.ConversationID == convID {
			deliveredMap[r.SenderID] = r.UptoMessageID
		}
	}

	if uptoA, ok := deliveredMap[uA]; !ok || uptoA != msgA.ID {
		t.Fatalf("Expected delivered status for sender A up to %d, got ok=%v, id=%d", msgA.ID, ok, uptoA)
	}
	if uptoB, ok := deliveredMap[uB]; !ok || uptoB != msgB.ID {
		t.Fatalf("Expected delivered status for sender B up to %d, got ok=%v, id=%d", msgB.ID, ok, uptoB)
	}
	if _, ok := deliveredMap[uD]; ok {
		t.Fatalf("Unrelated user D should NOT be in delivery results")
	}
	if _, ok := deliveredMap[uC]; ok {
		t.Fatalf("Recipient C should NOT be notified of their own delivery")
	}

	// Second run should return 0 results since C is already caught up
	results2, err := chatRepo.SyncUserDelivery(ctx, uC)
	if err != nil {
		t.Fatalf("Second SyncUserDelivery failed: %v", err)
	}
	for _, r := range results2 {
		if r.ConversationID == convID {
			t.Fatalf("Second sync should produce no results for convID, got %+v", r)
		}
	}
}

func TestStaggeredGroupOfflineDeliverySync(t *testing.T) {
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
	now := time.Now().UnixNano()

	// 1. Create 4 group members: A (sender), B (sender), C (reconnecting), D (initially remains offline)
	var uA, uB, uC, uD int
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member A", fmt.Sprintf("stag_a_%d@test.com", now), "hash").Scan(&uA)
	if err != nil {
		t.Fatalf("Failed to create uA: %v", err)
	}
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member B", fmt.Sprintf("stag_b_%d@test.com", now+1), "hash").Scan(&uB)
	if err != nil {
		t.Fatalf("Failed to create uB: %v", err)
	}
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member C", fmt.Sprintf("stag_c_%d@test.com", now+2), "hash").Scan(&uC)
	if err != nil {
		t.Fatalf("Failed to create uC: %v", err)
	}
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Member D", fmt.Sprintf("stag_d_%d@test.com", now+3), "hash").Scan(&uD)
	if err != nil {
		t.Fatalf("Failed to create uD: %v", err)
	}
	defer db.Exec(`DELETE FROM users WHERE id IN ($1, $2, $3, $4)`, uA, uB, uC, uD)

	// 2. Create 4-member group conversation
	convID, err := chatRepo.CreateGroupConversation(ctx, "Staggered Delivery Group", uA, []int{uA, uB, uC, uD})
	if err != nil {
		t.Fatalf("Failed to create group conv: %v", err)
	}
	defer db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)

	clientMsgA := fmt.Sprintf("stag-msg-a-%d", now)
	clientMsgB := fmt.Sprintf("stag-msg-b-%d", now)

	// A sends Message 50
	msgA := &models.Message{
		ConversationID:  convID,
		SenderID:        uA,
		MessageType:     "text",
		Content:         "Message from A",
		ClientMessageID: &clientMsgA,
	}
	if _, err := chatRepo.SaveMessage(ctx, msgA); err != nil {
		t.Fatalf("Failed to save msgA: %v", err)
	}

	// B sends Message 51
	msgB := &models.Message{
		ConversationID:  convID,
		SenderID:        uB,
		MessageType:     "text",
		Content:         "Message from B",
		ClientMessageID: &clientMsgB,
	}
	if _, err := chatRepo.SaveMessage(ctx, msgB); err != nil {
		t.Fatalf("Failed to save msgB: %v", err)
	}

	// Senders A and B are online and delivered up to msgB.ID
	_ = chatRepo.UpdateLastDeliveredWatermark(ctx, convID, uA, msgB.ID)
	_ = chatRepo.UpdateLastDeliveredWatermark(ctx, convID, uB, msgB.ID)

	// C and D are initially offline (delivery watermarks are 0)
	var cWM, dWM int64
	_ = db.QueryRow(`SELECT last_delivered_message_id FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`, convID, uC).Scan(&cWM)
	_ = db.QueryRow(`SELECT last_delivered_message_id FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`, convID, uD).Scan(&dWM)
	if cWM != 0 || dWM != 0 {
		t.Fatalf("Expected initial delivered watermarks to be 0, got C=%d, D=%d", cWM, dWM)
	}

	// 3. Step 1: Member C reconnects, but Member D REMAINS OFFLINE
	resultsC, err := chatRepo.SyncUserDelivery(ctx, uC)
	if err != nil {
		t.Fatalf("SyncUserDelivery for C failed: %v", err)
	}

	// PREVENT PREMATURE SENDER NOTIFICATION:
	// Because Member D is still offline (watermark 0), neither msgA nor msgB has been delivered
	// to ALL members. Therefore, SyncUserDelivery for C must NOT produce any delivery results!
	for _, r := range resultsC {
		if r.ConversationID == convID {
			t.Fatalf("Premature notification! Neither message should be delivered-to-all while D is offline, but got: %+v", r)
		}
	}

	// Verify C's watermark DID advance to msgB.ID in DB
	_ = db.QueryRow(`SELECT last_delivered_message_id FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`, convID, uC).Scan(&cWM)
	if cWM != msgB.ID {
		t.Fatalf("Expected C's watermark to advance to %d, got %d", msgB.ID, cWM)
	}

	// 4. Step 2: Member D now reconnects and catches up
	resultsD, err := chatRepo.SyncUserDelivery(ctx, uD)
	if err != nil {
		t.Fatalf("SyncUserDelivery for D failed: %v", err)
	}

	// NOW that both C and D are caught up, ALL members have received the messages!
	// Both A and B must receive their respective delivered notifications.
	deliveredMap := make(map[int]int64)
	for _, r := range resultsD {
		if r.ConversationID == convID {
			deliveredMap[r.SenderID] = r.UptoMessageID
		}
	}

	if uptoA, ok := deliveredMap[uA]; !ok || uptoA != msgA.ID {
		t.Fatalf("Expected delivered status for sender A up to %d, got ok=%v, id=%d", msgA.ID, ok, uptoA)
	}
	if uptoB, ok := deliveredMap[uB]; !ok || uptoB != msgB.ID {
		t.Fatalf("Expected delivered status for sender B up to %d, got ok=%v, id=%d", msgB.ID, ok, uptoB)
	}
	if _, ok := deliveredMap[uC]; ok {
		t.Fatalf("Recipient C should NOT be notified as a sender")
	}
	if _, ok := deliveredMap[uD]; ok {
		t.Fatalf("Recipient D should NOT receive self-delivery notification")
	}

	// 5. Verify idempotency: subsequent sync produces zero duplicate results
	resultsD2, err := chatRepo.SyncUserDelivery(ctx, uD)
	if err != nil {
		t.Fatalf("Repeat SyncUserDelivery for D failed: %v", err)
	}
	for _, r := range resultsD2 {
		if r.ConversationID == convID {
			t.Fatalf("Idempotency violation: repeat sync should produce no results, got %+v", r)
		}
	}
}

func TestAckDatabaseConsistency(t *testing.T) {
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
	now := time.Now().UnixNano()

	// 1. Setup users
	var u1, u2 int
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Alice", fmt.Sprintf("alice_%d@test.com", now), "hash").Scan(&u1)
	if err != nil {
		t.Fatalf("Failed to create u1: %v", err)
	}
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		"Bob", fmt.Sprintf("bob_%d@test.com", now+1), "hash").Scan(&u2)
	if err != nil {
		t.Fatalf("Failed to create u2: %v", err)
	}
	defer db.Exec(`DELETE FROM users WHERE id IN ($1, $2)`, u1, u2)

	convID, err := chatRepo.GetOrCreateDirectConversation(ctx, u1, u2)
	if err != nil {
		t.Fatalf("Failed to create conv: %v", err)
	}
	defer db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)

	clientMsg1 := fmt.Sprintf("cmsg1_%d", now)
	clientMsg2 := fmt.Sprintf("cmsg2_%d", now)
	clientMsg3 := fmt.Sprintf("cmsg3_%d", now)

	msg1 := &models.Message{ConversationID: convID, SenderID: u1, MessageType: "text", Content: "Msg 1", ClientMessageID: &clientMsg1}
	msg2 := &models.Message{ConversationID: convID, SenderID: u1, MessageType: "text", Content: "Msg 2", ClientMessageID: &clientMsg2}
	msg3 := &models.Message{ConversationID: convID, SenderID: u1, MessageType: "text", Content: "Msg 3", ClientMessageID: &clientMsg3}

	_, _ = chatRepo.SaveMessage(ctx, msg1)
	_, _ = chatRepo.SaveMessage(ctx, msg2)
	_, _ = chatRepo.SaveMessage(ctx, msg3)

	// Test 1: Normal Delivered ACK (Bob delivers msg2)
	advDelivered, err := chatRepo.ProcessAckDelivered(ctx, convID, u2, msg2.ID)
	if err != nil {
		t.Fatalf("ProcessAckDelivered failed: %v", err)
	}
	if !advDelivered {
		t.Fatalf("Expected watermark to advance on first delivered ack")
	}

	// Verify DB state
	var delWatermark int64
	_ = db.QueryRow(`SELECT last_delivered_message_id FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`, convID, u2).Scan(&delWatermark)
	if delWatermark != msg2.ID {
		t.Fatalf("Expected last_delivered_message_id = %d, got %d", msg2.ID, delWatermark)
	}
	var receiptStatus string
	_ = db.QueryRow(`SELECT status FROM message_receipts WHERE message_id = $1 AND user_id = $2`, msg2.ID, u2).Scan(&receiptStatus)
	if receiptStatus != "delivered" {
		t.Fatalf("Expected receipt status = delivered, got %s", receiptStatus)
	}

	// Test 2: Duplicate Delivered ACK
	advDeliveredDup, err := chatRepo.ProcessAckDelivered(ctx, convID, u2, msg2.ID)
	if err != nil || advDeliveredDup {
		t.Fatalf("Expected adv=false on duplicate delivered ack, got adv=%v, err=%v", advDeliveredDup, err)
	}

	// Test 3: Normal Seen ACK (Bob reads msg2)
	advSeen, err := chatRepo.ProcessAckSeen(ctx, convID, u2, msg2.ID)
	if err != nil {
		t.Fatalf("ProcessAckSeen failed: %v", err)
	}
	if !advSeen {
		t.Fatalf("Expected watermark to advance on first seen ack")
	}

	var readWatermark int64
	_ = db.QueryRow(`SELECT last_read_message_id FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`, convID, u2).Scan(&readWatermark)
	if readWatermark != msg2.ID {
		t.Fatalf("Expected last_read_message_id = %d, got %d", msg2.ID, readWatermark)
	}
	_ = db.QueryRow(`SELECT status FROM message_receipts WHERE message_id = $1 AND user_id = $2`, msg2.ID, u2).Scan(&receiptStatus)
	if receiptStatus != "seen" {
		t.Fatalf("Expected receipt status = seen, got %s", receiptStatus)
	}

	// Test 4: Duplicate Seen ACK
	advSeenDup, err := chatRepo.ProcessAckSeen(ctx, convID, u2, msg2.ID)
	if err != nil || advSeenDup {
		t.Fatalf("Expected adv=false on duplicate seen ack, got adv=%v, err=%v", advSeenDup, err)
	}

	// Test 5: Older Seen ACK (msg1 < msg2)
	advSeenOld, err := chatRepo.ProcessAckSeen(ctx, convID, u2, msg1.ID)
	if err != nil || advSeenOld {
		t.Fatalf("Expected adv=false on older seen ack, got adv=%v, err=%v", advSeenOld, err)
	}
	_ = db.QueryRow(`SELECT last_read_message_id FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`, convID, u2).Scan(&readWatermark)
	if readWatermark != msg2.ID {
		t.Fatalf("Watermark regressed! Expected %d, got %d", msg2.ID, readWatermark)
	}

	// Test 6: Delivered following Seen must NEVER downgrade status
	advDelivAfterSeen, err := chatRepo.ProcessAckDelivered(ctx, convID, u2, msg2.ID)
	if err != nil || advDelivAfterSeen {
		t.Fatalf("Expected adv=false on delivered ack after seen, got adv=%v, err=%v", advDelivAfterSeen, err)
	}
	_ = db.QueryRow(`SELECT status FROM message_receipts WHERE message_id = $1 AND user_id = $2`, msg2.ID, u2).Scan(&receiptStatus)
	if receiptStatus != "seen" {
		t.Fatalf("Status was downgraded from seen to %s!", receiptStatus)
	}
}
