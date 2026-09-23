package service

import (
	"context"
	"go-talk/models"
	"go-talk/repository"
)

type ChatService interface {
	GetOrCreateDirectChat(ctx context.Context, currentUserID, targetUserID int) (string, error)
	GetMyConversations(ctx context.Context, userID int) ([]models.Conversation, error)
	GetChatMessages(ctx context.Context, convID string, limit int, beforeID int64) ([]models.Message, error)
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
