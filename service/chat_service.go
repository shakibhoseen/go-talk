package service

import (
	"context"
	"go-talk/models"
	"go-talk/repository"
)

type ChatService interface {
	GetOrCreateDirectChat(ctx context.Context, currentUserID, targetUserID int) (string, error)
	CreateGroupChat(ctx context.Context, title string, creatorID int, memberIDs []int) (string, error)
	GetMyConversations(ctx context.Context, userID int) ([]models.Conversation, error)
	GetChatMessages(ctx context.Context, convID string, limit int, beforeID int64) ([]models.Message, error)
	AddMemberToGroup(ctx context.Context, convID string, targetUserID int) error
	RemoveMemberFromGroup(ctx context.Context, convID string, targetUserID int) error
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

func (s *chatService) GetChatMessages(ctx context.Context, convID string, limit int, beforeID int64) ([]models.Message, error) {
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

func (s *chatService) AddMemberToGroup(ctx context.Context, convID string, targetUserID int) error {
	return s.chatRepo.AddGroupMember(ctx, convID, targetUserID, "member")
}

func (s *chatService) RemoveMemberFromGroup(ctx context.Context, convID string, targetUserID int) error {
	return s.chatRepo.RemoveGroupMember(ctx, convID, targetUserID)
}
