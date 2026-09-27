
ALTER TABLE conversation_members 
ADD COLUMN IF NOT EXISTS last_read_message_id BIGINT DEFAULT 0;

-- Lookup fast korar jonno index
CREATE INDEX IF NOT EXISTS idx_conv_members_read 
ON conversation_members(conversation_id, last_read_message_id);