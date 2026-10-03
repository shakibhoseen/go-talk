-- Migration: Add missing indexes for performance at scale

-- 1. message_receipts — fastest lookup for MarkMessagesSeenUpto range updates
CREATE INDEX IF NOT EXISTS idx_message_receipts_user_status
    ON message_receipts(user_id, status);

CREATE INDEX IF NOT EXISTS idx_message_receipts_msg_status
    ON message_receipts(message_id, status);

-- 2. conversations(type) — used in GetOrCreateDirectConversation
CREATE INDEX IF NOT EXISTS idx_conversations_type
    ON conversations(type);
