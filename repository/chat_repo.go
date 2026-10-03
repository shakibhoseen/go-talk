package repository

import (
	"context"
	"database/sql"
	"errors"
	"go-talk/models"
)

type ChatRepository interface {
	UpdateGroupAvatar(ctx context.Context, convID string, avatarURL string) error
	GetConversationMembers(ctx context.Context, convID string) ([]models.ConversationMemberProfile, error)
	GetUserRoleInConversation(ctx context.Context, convID string, userID int) (string, error)
	// ACID Transaction: Inserts message and atomically updates conversation head
	SaveMessage(ctx context.Context, msg *models.Message) error

	// Inbox loading: Extremely fast query on conversations table
	GetUserConversations(ctx context.Context, userID int) ([]models.Conversation, error)

	// Chat screen inside: Messages with pagination
	GetConversationMessages(ctx context.Context, convID string, limit int, beforeID int64) ([]models.Message, bool, error)

	// Direct chat find or create helper
	GetOrCreateDirectConversation(ctx context.Context, user1, user2 int) (string, error)

	// Notun: Conversation-er sokol member ID ber kora
	GetConversationMemberIDs(ctx context.Context, convID string) ([]int, error)

	MarkMessageDelivered(ctx context.Context, messageID int64, userID int) error
	MarkMessagesSeenUpto(ctx context.Context, convID string, userID int, uptoMessageID int64) error

	CreateGroupConversation(ctx context.Context, title string, creatorID int, memberIDs []int) (string, error)
	AddGroupMember(ctx context.Context, convID string, userID int, role string) error
	RemoveGroupMember(ctx context.Context, convID string, userID int) error

	UpdateLastReadWatermark(ctx context.Context, convID string, userID int, messageID int64) error
	GetGroupReadWatermarks(ctx context.Context, convID string) (map[int64][]models.ReadReceiptUser, error)
}

type chatRepo struct {
	db *sql.DB
}

func NewChatRepository(db *sql.DB) ChatRepository {
	return &chatRepo{db: db}
}

func (r *chatRepo) SaveMessage(ctx context.Context, msg *models.Message) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Insert into messages table
	msgQuery := `INSERT INTO messages (conversation_id, sender_id, message_type, content, created_at)
	             VALUES ($1, $2, $3, $4, NOW())
	             RETURNING id, created_at`
	if err := tx.QueryRowContext(ctx, msgQuery, msg.ConversationID, msg.SenderID, msg.MessageType, msg.Content).
		Scan(&msg.ID, &msg.CreatedAt); err != nil {
		return err
	}

	// 2. Update conversation head cache
	headQuery := `UPDATE conversations 
	              SET last_message_content = $1,
	                  last_message_sender_id = $2,
	                  last_message_at = $3
	              WHERE id = $4`
	res, err := tx.ExecContext(ctx, headQuery, msg.Content, msg.SenderID, msg.CreatedAt, msg.ConversationID)
	if err != nil {
		return err
	}

	// 3. Auto-update sender's own read watermark to the message they just sent!
	watermarkQuery := `UPDATE conversation_members
					   SET last_read_message_id = $1
					   WHERE conversation_id = $2 AND user_id = $3`
	_, err = tx.ExecContext(ctx, watermarkQuery, msg.ID, msg.ConversationID, msg.SenderID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil || rows == 0 {
		return errors.New("conversation head not found to update")
	}

	// 3. Commit transaction
	return tx.Commit()
}

func (r *chatRepo) GetUserConversations(ctx context.Context, userID int) ([]models.Conversation, error) {
	// Chat List query: 50 chats load instantly because data is pre-cached on conversations table
	query := `SELECT 
				c.id, 
				c.type, 
				CASE WHEN c.type = 'direct' THEN u2.name ELSE c.title END as title,
				CASE WHEN c.type = 'direct' THEN u2.avatar_url ELSE c.avatar_url END as avatar_url,
				c.last_message_content, 
				c.last_message_sender_id, 
				c.last_message_at, 
				CASE WHEN c.type = 'direct' THEN u2.id ELSE NULL END as other_user_id,
				c.created_at
	          FROM conversations c
	          INNER JOIN conversation_members cm ON c.id = cm.conversation_id
			  LEFT JOIN conversation_members cm2 ON c.id = cm2.conversation_id AND c.type = 'direct' AND cm2.user_id != $1
			  LEFT JOIN users u2 ON cm2.user_id = u2.id
	          WHERE cm.user_id = $1
	          ORDER BY COALESCE(c.last_message_at, c.created_at) DESC
	          LIMIT 50`

	rows, err := r.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var convs []models.Conversation
	for rows.Next() {
		var c models.Conversation
		if err := rows.Scan(
			&c.ID, &c.Type, &c.Title, &c.AvatarURL,
			&c.LastMessageContent, &c.LastMessageSenderID, &c.LastMessageAt,
			&c.OtherUserID, &c.CreatedAt,
		); err != nil {
			return nil, err
		}
		convs = append(convs, c)
	}

	return convs, rows.Err()
}

func (r *chatRepo) GetConversationMessages(ctx context.Context, convID string, limit int, beforeID int64) ([]models.Message, bool, error) {
	// 1. Default limit set করা (যাতে কেউ একসাথে অনেক ডেটা রিকোয়েস্ট করে সার্ভার ডাউন না করতে পারে)
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	// 2. Pagination Peek: আমরা লিমিটের চেয়ে ১টি মেসেজ বেশি আনব (limit + 1)। 
	// এই অতিরিক্ত মেসেজটি দিয়ে আমরা বুঝতে পারব যে ডেটাবেসে আরও পুরানো মেসেজ (Next Page) আছে কি না।
	fetchLimit := limit + 1

	var query string
	var args []any

	// 3. Query Optimization:
	// আমরা সরাসরি JOIN না করে, আগে sub-query তে LIMIT অ্যাপ্লাই করেছি।
	// এতে করে ডেটাবেসকে শুধু limit (যেমন 21) টা row এর সাথেই users টেবিল JOIN করতে হবে, যা অনেক ফাস্ট!
	if beforeID > 0 {
		query = `
			SELECT sub.id, sub.conversation_id, sub.sender_id, u.name, COALESCE(u.avatar_url, ''), sub.message_type, sub.content, sub.created_at
			FROM (
				SELECT id, conversation_id, sender_id, message_type, content, created_at
				FROM messages
				WHERE conversation_id = $1 AND id < $2
				ORDER BY id DESC
				LIMIT $3
			) sub
			JOIN users u ON sub.sender_id = u.id
			ORDER BY sub.id DESC`
		args = []any{convID, beforeID, fetchLimit}
	} else {
		query = `
			SELECT sub.id, sub.conversation_id, sub.sender_id, u.name, COALESCE(u.avatar_url, ''), sub.message_type, sub.content, sub.created_at
			FROM (
				SELECT id, conversation_id, sender_id, message_type, content, created_at
				FROM messages
				WHERE conversation_id = $1
				ORDER BY id DESC
				LIMIT $2
			) sub
			JOIN users u ON sub.sender_id = u.id
			ORDER BY sub.id DESC`
		args = []any{convID, fetchLimit}
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var msgs []models.Message
	for rows.Next() {
		var m models.Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.SenderName, &m.SenderAvatar, &m.MessageType, &m.Content, &m.CreatedAt); err != nil {
			return nil, false, err
		}
		msgs = append(msgs, m)
	}

	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	// 4. HasMore (পরবর্তী পেজ) ক্যালকুলেশন
	hasMore := false
	if len(msgs) > limit {
		hasMore = true
		// কোয়েরিটি DESC (Descending) অর্ডারে ডেটা দিচ্ছে (সবচেয়ে নতুনটি শুরুতে)।
		// তাই ১টি এক্সট্রা যে ডেটা এসেছিল সেটি আছে অ্যারের একদম শেষে।
		// msgs[:limit] ব্যবহার করে আমরা অ্যারের শেষের সেই এক্সট্রা ডেটাটি বাদ দিয়ে দিচ্ছি।
		msgs = msgs[:limit]
	}
	
	return msgs, hasMore, nil
}

func (r *chatRepo) GetOrCreateDirectConversation(ctx context.Context, user1, user2 int) (string, error) {
	// Check if 1-to-1 conversation already exists
	findQuery := `SELECT cm1.conversation_id
	              FROM conversation_members cm1
	              INNER JOIN conversation_members cm2 ON cm1.conversation_id = cm2.conversation_id
	              INNER JOIN conversations c ON c.id = cm1.conversation_id
	              WHERE cm1.user_id = $1 AND cm2.user_id = $2 AND c.type = 'direct'
	              LIMIT 1`

	var convID string
	err := r.db.QueryRowContext(ctx, findQuery, user1, user2).Scan(&convID)
	if err == nil {
		return convID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	// Create new direct conversation in a transaction
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	if err := tx.QueryRowContext(ctx, `INSERT INTO conversations (type) VALUES ('direct') RETURNING id`).Scan(&convID); err != nil {
		return "", err
	}

	memberInsert := `INSERT INTO conversation_members (conversation_id, user_id) VALUES ($1, $2), ($1, $3)`
	if _, err := tx.ExecContext(ctx, memberInsert, convID, user1, user2); err != nil {
		return "", err
	}

	return convID, tx.Commit()
}

func (r *chatRepo) GetConversationMemberIDs(ctx context.Context, convID string) ([]int, error) {
	query := `SELECT user_id FROM conversation_members WHERE conversation_id = $1`
	rows, err := r.db.QueryContext(ctx, query, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memberIDs []int
	for rows.Next() {
		var uid int
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		memberIDs = append(memberIDs, uid)
	}
	return memberIDs, rows.Err()
}

func (r *chatRepo) MarkMessageDelivered(ctx context.Context, messageID int64, userID int) error {
	query := `INSERT INTO message_receipts (message_id, user_id, status, updated_at)
	          VALUES ($1, $2, 'delivered', NOW())
	          ON CONFLICT (message_id, user_id) 
	          DO UPDATE SET status = 'delivered', updated_at = NOW()
	          WHERE message_receipts.status != 'seen'`
	_, err := r.db.ExecContext(ctx, query, messageID, userID)
	return err
}

func (r *chatRepo) MarkMessagesSeenUpto(ctx context.Context, convID string, userID int, uptoMessageID int64) error {
	// Range ACK: 1 single indexed query-te uptoMessageID porjonto sob message-e 'seen' mark kora
	query := `INSERT INTO message_receipts (message_id, user_id, status, updated_at)
	          SELECT m.id, $1, 'seen', NOW()
	          FROM messages m
	          WHERE m.conversation_id = $2 
	            AND m.id <= $3 
	            AND m.sender_id != $1
	          ON CONFLICT (message_id, user_id) 
	          DO UPDATE SET status = 'seen', updated_at = NOW()`
	_, err := r.db.ExecContext(ctx, query, userID, convID, uptoMessageID)
	return err
}

func (r *chatRepo) CreateGroupConversation(ctx context.Context, title string, creatorID int, memberIDs []int) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// 1. Group conversation toiri kora
	var convID string
	query := `INSERT INTO conversations (type, title) VALUES ('group', $1) RETURNING id`
	if err := tx.QueryRowContext(ctx, query, title).Scan(&convID); err != nil {
		return "", err
	}

	// 2. Creator-ke 'admin' role hisabe insert kora
	adminQuery := `INSERT INTO conversation_members (conversation_id, user_id, role) VALUES ($1, $2, 'admin')`
	if _, err := tx.ExecContext(ctx, adminQuery, convID, creatorID); err != nil {
		return "", err
	}

	// 3. Baki members-ke 'member' role hisabe insert kora
	memberQuery := `INSERT INTO conversation_members (conversation_id, user_id, role) 
	                VALUES ($1, $2, 'member') 
	                ON CONFLICT (conversation_id, user_id) DO NOTHING`
	for _, uid := range memberIDs {
		if uid != creatorID {
			if _, err := tx.ExecContext(ctx, memberQuery, convID, uid); err != nil {
				return "", err
			}
		}
	}

	return convID, tx.Commit()
}

func (r *chatRepo) AddGroupMember(ctx context.Context, convID string, userID int, role string) error {
	if role == "" {
		role = "member"
	}
	query := `INSERT INTO conversation_members (conversation_id, user_id, role) 
	          VALUES ($1, $2, $3) 
	          ON CONFLICT (conversation_id, user_id) DO NOTHING`
	_, err := r.db.ExecContext(ctx, query, convID, userID, role)
	return err
}

func (r *chatRepo) RemoveGroupMember(ctx context.Context, convID string, userID int) error {
	query := `DELETE FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`
	res, err := r.db.ExecContext(ctx, query, convID, userID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("member not found in this conversation")
	}
	return nil
}

// UpdateLastReadWatermark: ইউজারের সর্বশেষ পঠিত মেসেজের পয়েন্টার আপডেট করে
func (r *chatRepo) UpdateLastReadWatermark(ctx context.Context, convID string, userID int, messageID int64) error {
	query := `
		UPDATE conversation_members
		SET last_read_message_id = $1
		WHERE conversation_id = $2 
		  AND user_id = $3 
		  AND last_read_message_id < $1` // পয়েন্টার যাতে পেছনের দিকে না নামে

	_, err := r.db.ExecContext(ctx, query, messageID, convID, userID)
	return err
}

// GetGroupReadWatermarks: কোন মেসেজ আইডিতে কোন কোন ইউজার অবস্থান করছে তা রিটার্ন করে
func (r *chatRepo) GetGroupReadWatermarks(ctx context.Context, convID string) (map[int64][]models.ReadReceiptUser, error) {
	query := `
		SELECT GREATEST(cm.last_read_message_id, COALESCE(MAX(m.id), 0)) AS msg_id,
		       u.id, u.name, COALESCE(u.avatar_url, '')
		FROM conversation_members cm
		JOIN users u ON cm.user_id = u.id
		LEFT JOIN messages m ON m.conversation_id = cm.conversation_id AND m.sender_id = u.id
		WHERE cm.conversation_id = $1
		GROUP BY cm.last_read_message_id, u.id, u.name, u.avatar_url
		HAVING GREATEST(cm.last_read_message_id, COALESCE(MAX(m.id), 0)) > 0`

	rows, err := r.db.QueryContext(ctx, query, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	watermarks := make(map[int64][]models.ReadReceiptUser)
	for rows.Next() {
		var msgID int64
		var user models.ReadReceiptUser
		if err := rows.Scan(&msgID, &user.UserID, &user.Name, &user.AvatarURL); err != nil {
			return nil, err
		}
		watermarks[msgID] = append(watermarks[msgID], user)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return watermarks, nil
}

func (r *chatRepo) UpdateGroupAvatar(ctx context.Context, convID string, avatarURL string) error {
	query := `UPDATE conversations SET avatar_url = $1 WHERE id = $2 AND type = 'group'`
	res, err := r.db.ExecContext(ctx, query, avatarURL, convID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return errors.New("group not found or not a group")
	}
	return nil
}

func (r *chatRepo) GetConversationMembers(ctx context.Context, convID string) ([]models.ConversationMemberProfile, error) {
	query := `
		SELECT cm.conversation_id, cm.user_id, cm.role, cm.joined_at, u.name, u.email, u.avatar_url
		FROM conversation_members cm
		JOIN users u ON cm.user_id = u.id
		WHERE cm.conversation_id = $1
		ORDER BY cm.role ASC, u.name ASC
	`
	rows, err := r.db.QueryContext(ctx, query, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []models.ConversationMemberProfile
	for rows.Next() {
		var m models.ConversationMemberProfile
		if err := rows.Scan(&m.ConversationID, &m.UserID, &m.Role, &m.JoinedAt, &m.Name, &m.Email, &m.AvatarURL); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, nil
}

func (r *chatRepo) GetUserRoleInConversation(ctx context.Context, convID string, userID int) (string, error) {
	query := `SELECT role FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`
	var role string
	err := r.db.QueryRowContext(ctx, query, convID, userID).Scan(&role)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", errors.New("user is not a member of this conversation")
		}
		return "", err
	}
	return role, nil
}
