package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"go-talk/models"
	"go-talk/repository"
	"go-talk/service"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isValidUUID(u string) bool {
	return uuidRegex.MatchString(u)
}

type ChatHandler struct {
	chatSvc service.ChatService
	authSvc service.AuthService
}

func NewChatHandler(chatSvc service.ChatService, authSvc service.AuthService) *ChatHandler {
	return &ChatHandler{
		chatSvc: chatSvc,
		authSvc: authSvc,
	}
}

// Helper: Authorization Bearer header theke userID parse kora
func (h *ChatHandler) extractUserID(r *http.Request) (int, error) {
	authHeader := r.Header.Get("Authorization")
	parts := strings.Split(authHeader, " ")
	token := ""
	if len(parts) == 2 && parts[0] == "Bearer" {
		token = parts[1]
	}
	return h.authSvc.ValidateToken(token)
}

// POST /conversations/direct
func (h *ChatHandler) CreateDirectChat(w http.ResponseWriter, r *http.Request) {
	currentUserID, err := h.extractUserID(r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	var req struct {
		TargetUserID int `json:"target_user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TargetUserID <= 0 {
		http.Error(w, "Invalid target_user_id", http.StatusBadRequest)
		return
	}

	if req.TargetUserID == currentUserID {
		http.Error(w, "Cannot create direct conversation with yourself", http.StatusBadRequest)
		return
	}

	convID, err := h.chatSvc.GetOrCreateDirectChat(r.Context(), currentUserID, req.TargetUserID)
	if err != nil {
		http.Error(w, "Failed to create or get conversation: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"conversation_id": convID,
	})
}

// GET /conversations (WhatsApp Home Chat Head Screen)
func (h *ChatHandler) GetConversations(w http.ResponseWriter, r *http.Request) {
	currentUserID, err := h.extractUserID(r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	convs, err := h.chatSvc.GetMyConversations(r.Context(), currentUserID)
	if err != nil {
		http.Error(w, "Failed to fetch conversations", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"conversations": convs,
	})
}

// GET /conversations/{id}/messages (Chat Screen Inside)
func (h *ChatHandler) GetMessages(w http.ResponseWriter, r *http.Request) {
	currentUserID, err := h.extractUserID(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	convID := r.PathValue("id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	beforeID, _ := strconv.ParseInt(r.URL.Query().Get("before_id"), 10, 64)
	sinceID, _ := strconv.ParseInt(r.URL.Query().Get("since_id"), 10, 64)

	if beforeID > 0 && sinceID > 0 {
		http.Error(w, "Cannot specify both before_id and since_id", http.StatusBadRequest)
		return
	}

	messages, hasMore, err := h.chatSvc.GetChatMessages(r.Context(), convID, currentUserID, limit, beforeID, sinceID)
	if err != nil {
		if err.Error() == "forbidden: user is not a member of this conversation" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if err.Error() == "conversation not found" {
			http.Error(w, "Conversation not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to fetch messages: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var minMessageID int64 = 0
	if sinceID > 0 {
		// Delta sync: fetch all current member watermarks regardless of message range
		minMessageID = 0
	} else if len(messages) > 0 {
		minMessageID = messages[len(messages)-1].ID
	}

	// ২. মেম্বারদের ওয়াটারমার্ক (কার কতটুকু দেখা শেষ) ফেচ করা
	watermarks, err := h.chatSvc.GetGroupReadWatermarks(r.Context(), convID, minMessageID)
	if err != nil {
		// এরর হলে খালি ম্যাপ ধরে প্রসেস করবে যাতে এপিআই ফেইল না করে
		watermarks = make(map[int64][]models.ReadReceiptUser)
	}

	watermarkMap := make(map[string]models.MessageWatermark)

	for msgID, users := range watermarks {
		if msgID <= 0 || len(users) == 0 {
			continue
		}

		watermarkMap[strconv.FormatInt(msgID, 10)] = models.MessageWatermark{
			Users: users,
			Count: len(users),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if sinceID > 0 {
		var nextSinceID int64 = sinceID
		if len(messages) > 0 {
			nextSinceID = messages[len(messages)-1].ID
		}
		json.NewEncoder(w).Encode(map[string]any{
			"messages":      messages,
			"watermarks":    watermarkMap,
			"has_more":      hasMore,
			"next_since_id": nextSinceID,
		})
	} else {
		var nextBeforeID *int64 = nil
		if len(messages) > 0 && hasMore {
			// Chronological (DESC) list-e index len-1 holo shobcheye purono message ID
			oldestId := messages[len(messages)-1].ID
			nextBeforeID = &oldestId
		}
		json.NewEncoder(w).Encode(map[string]any{
			"messages":       messages,
			"watermarks":     watermarkMap,
			"has_more":       hasMore,
			"next_before_id": nextBeforeID,
		})
	}
}

// POST /conversations/group
func (h *ChatHandler) CreateGroupChat(w http.ResponseWriter, r *http.Request) {
	currentUserID, err := h.extractUserID(r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	var req struct {
		Title     string `json:"title"`
		MemberIDs []int  `json:"member_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	trimmedTitle := strings.TrimSpace(req.Title)
	if trimmedTitle == "" {
		http.Error(w, "Group title is required", http.StatusBadRequest)
		return
	}

	var validMemberIDs []int
	for _, id := range req.MemberIDs {
		if id > 0 && id != currentUserID {
			validMemberIDs = append(validMemberIDs, id)
		}
	}

	if len(validMemberIDs) == 0 {
		http.Error(w, "At least one member is required to create a group", http.StatusBadRequest)
		return
	}

	convID, err := h.chatSvc.CreateGroupChat(r.Context(), trimmedTitle, currentUserID, validMemberIDs)
	if err != nil {
		if strings.Contains(err.Error(), "foreign key constraint") {
			http.Error(w, "One or more member IDs do not exist", http.StatusBadRequest)
			return
		}
		http.Error(w, "Failed to create group: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"conversation_id": convID,
		"title":           trimmedTitle,
	})
}

// POST /conversations/{id}/members
func (h *ChatHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	userID, err := h.extractUserID(r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	convID := r.PathValue("id")
	if convID == "" || !isValidUUID(convID) {
		http.Error(w, "Invalid conversation id", http.StatusBadRequest)
		return
	}

	var req struct {
		UserID       int `json:"user_id"`
		TargetUserID int `json:"target_user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	targetUID := req.UserID
	if targetUID == 0 {
		targetUID = req.TargetUserID
	}
	if targetUID <= 0 {
		http.Error(w, "Invalid target user id", http.StatusBadRequest)
		return
	}

	if err := h.chatSvc.AddMemberToGroup(r.Context(), convID, targetUID, userID); err != nil {
		if errors.Is(err, repository.ErrNotMember) {
			http.Error(w, "Forbidden: you are not a member of this conversation", http.StatusForbidden)
			return
		}
		if errors.Is(err, repository.ErrNotAdmin) {
			http.Error(w, "Forbidden: only admins can add members", http.StatusForbidden)
			return
		}
		if errors.Is(err, repository.ErrConversationNotFound) {
			http.Error(w, "Conversation not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, repository.ErrNotGroup) {
			http.Error(w, "Conversation is not a group", http.StatusBadRequest)
			return
		}
		if errors.Is(err, repository.ErrUserNotFound) {
			http.Error(w, "Target user not found", http.StatusBadRequest)
			return
		}
		if errors.Is(err, repository.ErrAlreadyMember) {
			http.Error(w, "User is already a member of this conversation", http.StatusConflict)
			return
		}
		http.Error(w, "Failed to add member: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"message": "Member added successfully",
	})
}

// DELETE /conversations/{id}/members/{user_id}
func (h *ChatHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	requesterID, err := h.extractUserID(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	convID := r.PathValue("id")
	if convID == "" || !isValidUUID(convID) {
		http.Error(w, "Invalid conversation id", http.StatusBadRequest)
		return
	}

	userIDStr := r.PathValue("user_id")
	targetUID, err := strconv.Atoi(userIDStr)
	if err != nil || targetUID <= 0 {
		http.Error(w, "Invalid user_id", http.StatusBadRequest)
		return
	}

	if err := h.chatSvc.RemoveMemberFromGroup(r.Context(), convID, targetUID, requesterID); err != nil {
		if errors.Is(err, repository.ErrNotMember) {
			http.Error(w, "Forbidden: you are not a member of this conversation", http.StatusForbidden)
			return
		}
		if errors.Is(err, repository.ErrNotAdmin) {
			http.Error(w, "Forbidden: only admins can remove other members", http.StatusForbidden)
			return
		}
		if errors.Is(err, repository.ErrCannotRemoveOnlyAdmin) {
			http.Error(w, "Cannot remove the only group admin", http.StatusBadRequest)
			return
		}
		if errors.Is(err, repository.ErrMemberNotFound) {
			http.Error(w, "Member not found in this conversation", http.StatusNotFound)
			return
		}
		if errors.Is(err, repository.ErrConversationNotFound) {
			http.Error(w, "Conversation not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, repository.ErrNotGroup) {
			http.Error(w, "Conversation is not a group", http.StatusBadRequest)
			return
		}
		http.Error(w, "Failed to remove member: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"message": "Member removed successfully",
	})
}

func (h *ChatHandler) UpdateGroupAvatar(w http.ResponseWriter, r *http.Request) {
	userID, err := h.extractUserID(r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	convID := r.PathValue("id")
	if convID == "" || !isValidUUID(convID) {
		http.Error(w, "Invalid conversation id", http.StatusBadRequest)
		return
	}

	// Parse multipart form (limit 5MB)
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		http.Error(w, "File too large. Maximum size is 5MB.", http.StatusBadRequest)
		return
	}
	file, handler, err := r.FormFile("avatar")
	if err != nil {
		http.Error(w, "Failed to retrieve avatar file: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(handler.Filename))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
		http.Error(w, "Invalid file format. Only JPG and PNG are allowed.", http.StatusBadRequest)
		return
	}

	uploadDir := "uploads/groups"
	if err := os.MkdirAll(uploadDir, os.ModePerm); err != nil {
		http.Error(w, "Failed to create upload directory", http.StatusInternalServerError)
		return
	}

	filename := fmt.Sprintf("group_%s_%d%s", convID, time.Now().Unix(), ext)
	filePath := filepath.Join(uploadDir, filename)

	dst, err := os.Create(filePath)
	if err != nil {
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		http.Error(w, "Failed to write file", http.StatusInternalServerError)
		return
	}

	avatarURL := fmt.Sprintf("/uploads/groups/%s", filename)

	if err := h.chatSvc.UpdateGroupAvatar(r.Context(), convID, avatarURL, userID); err != nil {
		if errors.Is(err, repository.ErrNotMember) {
			http.Error(w, "Forbidden: you are not a member of this conversation", http.StatusForbidden)
			return
		}
		if errors.Is(err, repository.ErrNotAdmin) {
			http.Error(w, "Forbidden: only admins can update group avatar", http.StatusForbidden)
			return
		}
		if errors.Is(err, repository.ErrConversationNotFound) {
			http.Error(w, "Conversation not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, repository.ErrNotGroup) {
			http.Error(w, "Conversation is not a group", http.StatusBadRequest)
			return
		}
		http.Error(w, "Failed to update avatar: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"message":    "Group avatar updated successfully",
		"avatar_url": avatarURL,
	})
}

func (h *ChatHandler) GetConversationMembers(w http.ResponseWriter, r *http.Request) {
	userID, err := h.extractUserID(r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	convID := r.PathValue("id")
	if convID == "" || !isValidUUID(convID) {
		http.Error(w, "Invalid conversation id", http.StatusBadRequest)
		return
	}

	members, err := h.chatSvc.GetConversationMembers(r.Context(), convID, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotMember) {
			http.Error(w, "Forbidden: you are not a member of this conversation", http.StatusForbidden)
			return
		}
		if errors.Is(err, repository.ErrConversationNotFound) {
			http.Error(w, "Conversation not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, repository.ErrNotGroup) {
			http.Error(w, "Conversation is not a group", http.StatusBadRequest)
			return
		}
		http.Error(w, "Failed to get members: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(members)
}
