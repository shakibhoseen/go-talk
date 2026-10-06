ALTER TABLE conversations 
ADD COLUMN IF NOT EXISTS last_message_id BIGINT DEFAULT 0;

UPDATE conversations c
SET last_message_id = COALESCE((
    SELECT MAX(m.id) FROM messages m WHERE m.conversation_id = c.id
), 0);
