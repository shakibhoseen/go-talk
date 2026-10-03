#!/bin/bash
awk '
/AddMemberToGroup\(ctx context.Context, convID string, targetUserID int\) error/ {
    print "\tUpdateGroupAvatar(ctx context.Context, convID string, avatarURL string, requesterID int) error"
    print "\tGetConversationMembers(ctx context.Context, convID string, requesterID int) ([]models.ConversationMemberProfile, error)"
    print "\tAddMemberToGroup(ctx context.Context, convID string, targetUserID int, requesterID int) error"
    next
}
/func \(s \*chatService\) AddMemberToGroup/ {
    print "func (s *chatService) UpdateGroupAvatar(ctx context.Context, convID string, avatarURL string, requesterID int) error {"
    print "\trole, err := s.chatRepo.GetUserRoleInConversation(ctx, convID, requesterID)"
    print "\tif err != nil { return err }"
    print "\tif role != \"admin\" { return errors.New(\"only admins can update group avatar\") }"
    print "\treturn s.chatRepo.UpdateGroupAvatar(ctx, convID, avatarURL)"
    print "}\n"
    print "func (s *chatService) GetConversationMembers(ctx context.Context, convID string, requesterID int) ([]models.ConversationMemberProfile, error) {"
    print "\t_, err := s.chatRepo.GetUserRoleInConversation(ctx, convID, requesterID)"
    print "\tif err != nil { return nil, err }"
    print "\treturn s.chatRepo.GetConversationMembers(ctx, convID)"
    print "}\n"
    print "func (s *chatService) AddMemberToGroup(ctx context.Context, convID string, targetUserID int, requesterID int) error {"
    print "\trole, err := s.chatRepo.GetUserRoleInConversation(ctx, convID, requesterID)"
    print "\tif err != nil { return err }"
    print "\tif role != \"admin\" { return errors.New(\"only admins can add members\") }"
    print "\treturn s.chatRepo.AddGroupMember(ctx, convID, targetUserID, \"member\")"
    print "}"
    skip=1
    next
}
skip && /^}$/ { skip=0; next }
skip { next }
1
' service/chat_service.go > tmp_service.go && mv tmp_service.go service/chat_service.go
