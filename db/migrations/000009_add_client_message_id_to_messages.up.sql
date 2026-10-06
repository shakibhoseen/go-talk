ALTER TABLE messages ADD COLUMN IF NOT EXISTS client_message_id VARCHAR(64) NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_idempotency
ON messages (conversation_id, sender_id, client_message_id)
WHERE client_message_id IS NOT NULL;
