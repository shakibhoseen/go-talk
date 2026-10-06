DROP INDEX IF EXISTS idx_messages_idempotency;

ALTER TABLE messages DROP COLUMN IF EXISTS client_message_id;
