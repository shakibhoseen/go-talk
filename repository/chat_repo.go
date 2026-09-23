package repository

import (
	"context"
	"database/sql"
	"errors"
	"go-talk/models"
)

type ChatRepository interface {
	// ACID Transaction: Inserts message and atomically updates conversation head
	SaveMessage(ctx context.Context, msg *models.Message) error

	// Inbox loading: Extremely fast query on conversations table
	GetUserConversations(ctx context.Context, userID int) ([]models.Conversation, error)

	// Chat screen inside: Messages with pagination
	GetConversationMessages(ctx context.Context, convID string, limit int, beforeID int64) ([]models.Message, error)

	// Direct chat find or create helper
	GetOrCreateDirectConversation(ctx context.Context, user1, user2 int) (string, error)

	// Notun: Conversation-er sokol member ID ber kora
	GetConversationMemberIDs(ctx context.Context, convID string) ([]int, error)

	MarkMessageDelivered(ctx context.Context, messageID int64, userID int) error
	MarkMessagesSeenUpto(ctx context.Context, convID string, userID int, uptoMessageID int64) error

	CreateGroupConversation(ctx context.Context, title string, creatorID int, memberIDs []int) (string, error)
	AddGroupMember(ctx context.Context, convID string, userID int, role string) error
	RemoveGroupMember(ctx context.Context, convID string, userID int) error
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

	rows, err := res.RowsAffected()
	if err != nil || rows == 0 {
		return errors.New("conversation head not found to update")
	}

	// 3. Commit transaction
	return tx.Commit()
}

func (r *chatRepo) GetUserConversations(ctx context.Context, userID int) ([]models.Conversation, error) {
	// Chat List query: 50 chats load instantly because data is pre-cached on conversations table
	query := `SELECT c.id, c.type, c.title, c.last_message_content, c.last_message_sender_id, c.last_message_at, c.created_at
	          FROM conversations c
	          INNER JOIN conversation_members cm ON c.id = cm.conversation_id
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
			&c.ID, &c.Type, &c.Title,
			&c.LastMessageContent, &c.LastMessageSenderID, &c.LastMessageAt,
			&c.CreatedAt,
		); err != nil {
			return nil, err
		}
		convs = append(convs, c)
	}

	return convs, rows.Err()
}

func (r *chatRepo) GetConversationMessages(ctx context.Context, convID string, limit int, beforeID int64) ([]models.Message, error) {
	var query string
	var rows *sql.Rows
	var err error

	if beforeID > 0 {
		query = `SELECT id, conversation_id, sender_id, message_type, content, created_at
		         FROM messages
		         WHERE conversation_id = $1 AND id < $2
		         ORDER BY id DESC LIMIT $3`
		rows, err = r.db.QueryContext(ctx, query, convID, beforeID, limit)
	} else {
		query = `SELECT id, conversation_id, sender_id, message_type, content, created_at
		         FROM messages
		         WHERE conversation_id = $1
		         ORDER BY id DESC LIMIT $2`
		rows, err = r.db.QueryContext(ctx, query, convID, limit)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []models.Message
	for rows.Next() {
		var m models.Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.MessageType, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}

	return msgs, rows.Err()
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
