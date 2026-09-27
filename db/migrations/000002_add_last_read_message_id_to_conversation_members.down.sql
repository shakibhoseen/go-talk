DROP INDEX IF EXISTS idx_conv_members_read;

ALTER TABLE conversation_members 
DROP COLUMN IF EXISTS last_read_message_id;