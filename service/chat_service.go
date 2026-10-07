package service

import (
	"context"
	"database/sql"
	"errors"
	"go-talk/models"
	"go-talk/repository"
)

type ChatService interface {
	GetOrCreateDirectChat(ctx context.Context, currentUserID, targetUserID int) (string, error)
	CreateGroupChat(ctx context.Context, title string, creatorID int, memberIDs []int) (string, error)
	GetMyConversations(ctx context.Context, userID int) ([]models.Conversation, error)
	GetChatMessages(ctx context.Context, convID string, currentUserID int, limit int, beforeID int64, sinceID int64) ([]models.Message, bool, error)
	AddMemberToGroup(ctx context.Context, convID string, targetUserID int, requesterID int) error
	UpdateGroupAvatar(ctx context.Context, convID string, avatarURL string, requesterID int) error
	GetConversationMembers(ctx context.Context, convID string, requesterID int) ([]models.ConversationMemberProfile, error)
	RemoveMemberFromGroup(ctx context.Context, convID string, targetUserID int, requesterID int) error
	UpdateLastReadWatermark(ctx context.Context, convID string, userID int, messageID int64) error
	GetGroupReadWatermarks(ctx context.Context, convID string, minMessageID int64) (map[int64][]models.ReadReceiptUser, error)
	SyncUserDelivery(ctx context.Context, userID int) ([]models.DeliverySyncResult, error)
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

func (s *chatService) GetChatMessages(ctx context.Context, convID string, currentUserID int, limit int, beforeID int64, sinceID int64) ([]models.Message, bool, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.chatRepo.GetConversationMessages(ctx, convID, currentUserID, limit, beforeID, sinceID)
}

// Service Struct-e method implement korun:
func (s *chatService) CreateGroupChat(ctx context.Context, title string, creatorID int, memberIDs []int) (string, error) {
	if title == "" {
		title = "New Group"
	}
	return s.chatRepo.CreateGroupConversation(ctx, title, creatorID, memberIDs)
}

func (s *chatService) AddMemberToGroup(ctx context.Context, convID string, targetUserID int, requesterID int) error {
	convType, err := s.chatRepo.GetConversationType(ctx, convID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return repository.ErrConversationNotFound
		}
		return err
	}
	if convType != "group" {
		return repository.ErrNotGroup
	}

	role, err := s.chatRepo.GetUserRoleInConversation(ctx, convID, requesterID)
	if err != nil {
		return err
	}
	if role != "admin" {
		return repository.ErrNotAdmin
	}
	return s.chatRepo.AddGroupMember(ctx, convID, targetUserID, "member")
}

func (s *chatService) UpdateGroupAvatar(ctx context.Context, convID string, avatarURL string, requesterID int) error {
	convType, err := s.chatRepo.GetConversationType(ctx, convID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return repository.ErrConversationNotFound
		}
		return err
	}
	if convType != "group" {
		return repository.ErrNotGroup
	}

	role, err := s.chatRepo.GetUserRoleInConversation(ctx, convID, requesterID)
	if err != nil {
		return err
	}
	if role != "admin" {
		return repository.ErrNotAdmin
	}
	return s.chatRepo.UpdateGroupAvatar(ctx, convID, avatarURL)
}

func (s *chatService) GetConversationMembers(ctx context.Context, convID string, requesterID int) ([]models.ConversationMemberProfile, error) {
	convType, err := s.chatRepo.GetConversationType(ctx, convID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrConversationNotFound
		}
		return nil, err
	}
	if convType != "group" {
		return nil, repository.ErrNotGroup
	}

	_, err = s.chatRepo.GetUserRoleInConversation(ctx, convID, requesterID)
	if err != nil {
		return nil, err
	}
	return s.chatRepo.GetConversationMembers(ctx, convID)
}

func (s *chatService) RemoveMemberFromGroup(ctx context.Context, convID string, targetUserID int, requesterID int) error {
	convType, err := s.chatRepo.GetConversationType(ctx, convID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return repository.ErrConversationNotFound
		}
		return err
	}
	if convType != "group" {
		return repository.ErrNotGroup
	}

	requesterRole, err := s.chatRepo.GetUserRoleInConversation(ctx, convID, requesterID)
	if err != nil {
		return err
	}

	targetRole, err := s.chatRepo.GetUserRoleInConversation(ctx, convID, targetUserID)
	if err != nil {
		if errors.Is(err, repository.ErrNotMember) {
			return repository.ErrMemberNotFound
		}
		return err
	}

	if requesterRole != "admin" {
		// Normal member can only remove themselves (leave group)
		if requesterID != targetUserID {
			return repository.ErrNotAdmin
		}
	} else {
		// Admin is removing someone
		if targetRole == "admin" {
			// Check if removing the last admin
			adminCount, err := s.chatRepo.GetAdminCountInConversation(ctx, convID)
			if err != nil {
				return err
			}
			if adminCount <= 1 {
				return repository.ErrCannotRemoveOnlyAdmin
			}
		}
	}

	return s.chatRepo.RemoveGroupMember(ctx, convID, targetUserID)
}

func (s *chatService) UpdateLastReadWatermark(ctx context.Context, convID string, userID int, messageID int64) error {
	return s.chatRepo.UpdateLastReadWatermark(ctx, convID, userID, messageID)
}
func (s *chatService) GetGroupReadWatermarks(ctx context.Context, convID string, minMessageID int64) (map[int64][]models.ReadReceiptUser, error) {
	return s.chatRepo.GetGroupReadWatermarks(ctx, convID, minMessageID)
}

func (s *chatService) SyncUserDelivery(ctx context.Context, userID int) ([]models.DeliverySyncResult, error) {
	return s.chatRepo.SyncUserDelivery(ctx, userID)
}
