-- 1. Users Table (Authentication & Calling Identity)
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    email VARCHAR(100) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    avatar_url TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- 2. Conversations Table (Chat Head / Inbox View)
CREATE TABLE IF NOT EXISTS conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type VARCHAR(20) NOT NULL DEFAULT 'direct', -- 'direct' or 'group'
    title VARCHAR(150),                          -- Group name (direct chat-e NULL)
    last_message_content TEXT,                   -- Instant inbox preview
    last_message_sender_id INT REFERENCES users(id),
    last_message_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- 3. Conversation Members (Group / Direct Mapping)
CREATE TABLE IF NOT EXISTS conversation_members (
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role VARCHAR(20) NOT NULL DEFAULT 'member', -- 'admin', 'member'
    joined_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (conversation_id, user_id)
);

-- 4. Messages Table
CREATE TABLE IF NOT EXISTS messages (
    id BIGSERIAL PRIMARY KEY,
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    sender_id INT NOT NULL REFERENCES users(id),
    message_type VARCHAR(20) NOT NULL DEFAULT 'text', -- 'text', 'image', 'audio', 'call_log'
    content TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- 5. Message Delivery Receipts (Sent, Delivered, Seen Lifecycle)
CREATE TABLE IF NOT EXISTS message_receipts (
    message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(20) NOT NULL DEFAULT 'delivered', -- 'delivered', 'seen'
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (message_id, user_id)
);

-- Performance Indexes
CREATE INDEX IF NOT EXISTS idx_conversations_last_msg_at ON conversations(last_message_at DESC);
CREATE INDEX IF NOT EXISTS idx_messages_conv_id ON messages(conversation_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_conv_members_user ON conversation_members(user_id);