DROP INDEX IF EXISTS idx_conv_members_delivered;
ALTER TABLE conversation_members DROP COLUMN IF EXISTS last_delivered_message_id;
