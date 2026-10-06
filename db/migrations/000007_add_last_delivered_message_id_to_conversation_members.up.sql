ALTER TABLE conversation_members 
ADD COLUMN IF NOT EXISTS last_delivered_message_id BIGINT DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_conv_members_delivered 
ON conversation_members(conversation_id, last_delivered_message_id);
