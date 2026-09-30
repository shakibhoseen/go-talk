#!/bin/bash
awk '
/type ChatRepository interface {/ {
    print
    print "\tUpdateGroupAvatar(ctx context.Context, convID string, avatarURL string) error"
    print "\tGetConversationMembers(ctx context.Context, convID string) ([]models.ConversationMemberProfile, error)"
    print "\tGetUserRoleInConversation(ctx context.Context, convID string, userID int) (string, error)"
    next
}
1
' repository/chat_repo.go > tmp_repo.go && mv tmp_repo.go repository/chat_repo.go
