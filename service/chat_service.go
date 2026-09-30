package service

import (
	"context"
	"errors"
	"go-talk/models"
	"go-talk/repository"
)

type ChatService interface {
	GetOrCreateDirectChat(ctx context.Context, currentUserID, targetUserID int) (string, error)
	CreateGroupChat(ctx context.Context, title string, creatorID int, memberIDs []int) (string, error)
	GetMyConversations(ctx context.Context, userID int) ([]models.Conversation, error)
	GetChatMessages(ctx context.Context, convID string, limit int, beforeID int64) ([]models.Message, bool, error)
	AddMemberToGroup(ctx context.Context, convID string, targetUserID int, requesterID int) error
	UpdateGroupAvatar(ctx context.Context, convID string, avatarURL string, requesterID int) error
	GetConversationMembers(ctx context.Context, convID string, requesterID int) ([]models.ConversationMemberProfile, error)
	RemoveMemberFromGroup(ctx context.Context, convID string, targetUserID int) error
	UpdateLastReadWatermark(ctx context.Context, convID string, userID int, messageID int64) error
	GetGroupReadWatermarks(ctx context.Context, convID string) (map[int64][]models.ReadReceiptUser, error)
}

type chatService struct {
	chatRepo repository.ChatRepository
}

func NewChatService(chatRepo repository.ChatRepository) ChatService {
	return &chatService{chatRepo: chatRepo}
}

func (s *chatService) GetOrCreateDirectChat(ctx context.Context, currentUserID, targetUserID int) (string, error) {
	return s.chatRepo.GetOrCreateDirectConversation(ctx, currentUserID, targetUserID)
}

func (s *chatService) GetMyConversations(ctx context.Context, userID int) ([]models.Conversation, error) {
	return s.chatRepo.GetUserConversations(ctx, userID)
}

func (s *chatService) GetChatMessages(ctx context.Context, convID string, limit int, beforeID int64) ([]models.Message, bool, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.chatRepo.GetConversationMessages(ctx, convID, limit, beforeID)
}

// Service Struct-e method implement korun:
func (s *chatService) CreateGroupChat(ctx context.Context, title string, creatorID int, memberIDs []int) (string, error) {
	if title == "" {
		title = "New Group"
	}
	return s.chatRepo.CreateGroupConversation(ctx, title, creatorID, memberIDs)
}

func (s *chatService) AddMemberToGroup(ctx context.Context, convID string, targetUserID int, requesterID int) error {
	role, err := s.chatRepo.GetUserRoleInConversation(ctx, convID, requesterID)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errors.New("only admins can add members")
	}
	return s.chatRepo.AddGroupMember(ctx, convID, targetUserID, "member")
}

func (s *chatService) UpdateGroupAvatar(ctx context.Context, convID string, avatarURL string, requesterID int) error {
	role, err := s.chatRepo.GetUserRoleInConversation(ctx, convID, requesterID)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errors.New("only admins can update group avatar")
	}
	return s.chatRepo.UpdateGroupAvatar(ctx, convID, avatarURL)
}

func (s *chatService) GetConversationMembers(ctx context.Context, convID string, requesterID int) ([]models.ConversationMemberProfile, error) {
	_, err := s.chatRepo.GetUserRoleInConversation(ctx, convID, requesterID)
	if err != nil {
		return nil, err
	}
	return s.chatRepo.GetConversationMembers(ctx, convID)
}

func (s *chatService) RemoveMemberFromGroup(ctx context.Context, convID string, targetUserID int) error {
	return s.chatRepo.RemoveGroupMember(ctx, convID, targetUserID)
}

func (s *chatService) UpdateLastReadWatermark(ctx context.Context, convID string, userID int, messageID int64) error {
	return s.chatRepo.UpdateLastReadWatermark(ctx, convID, userID, messageID)
}
func (s *chatService) GetGroupReadWatermarks(ctx context.Context, convID string) (map[int64][]models.ReadReceiptUser, error) {
	return s.chatRepo.GetGroupReadWatermarks(ctx, convID)
}
