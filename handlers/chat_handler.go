package handlers

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"encoding/json"
	"go-talk/models"
	"go-talk/service"
	"net/http"
	"strconv"
	"strings"
)

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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TargetUserID == 0 {
		http.Error(w, "Invalid target_user_id", http.StatusBadRequest)
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
	_, err := h.extractUserID(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	convID := r.PathValue("id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	beforeID, _ := strconv.ParseInt(r.URL.Query().Get("before_id"), 10, 64)

	messages, hasMore, err := h.chatSvc.GetChatMessages(r.Context(), convID, limit, beforeID)
	if err != nil {
		http.Error(w, "Failed to fetch messages", http.StatusInternalServerError)
		return
	}

	// ২. মেম্বারদের ওয়াটারমার্ক (কার কতটুকু দেখা শেষ) ফেচ করা (নতুন মেথড)
	watermarks, err := h.chatSvc.GetGroupReadWatermarks(r.Context(), convID)
	if err != nil {
		// এরর হলে খালি ম্যাপ ধরে প্রসেস করবে যাতে এপিআই ফেইল না করে
		watermarks = make(map[int64][]models.ReadReceiptUser)
	}

	// handlers/chat_handler.go
	const maxVisibleAvatars = 3
	// ৩. মেসেজের সাথে ReadBy ইউজার তালিকা এটাচ করে ফাইনাল রেসপন্স লিস্ট বানানো
	responseList := make([]models.MessageWithReceipts, 0, len(messages))
	for _, m := range messages {
		users := watermarks[m.ID]
		var filteredUsers []models.ReadReceiptUser
		for _, u := range users {
			if u.UserID != m.SenderID {
				filteredUsers = append(filteredUsers, u)
			}
		}

		var visibleUsers []models.ReadReceiptUser
		totalRead := len(filteredUsers)

		if len(users) > maxVisibleAvatars {
			visibleUsers = users[:maxVisibleAvatars]
		} else if len(users) > 0 {
			visibleUsers = users
		} else {
			visibleUsers = []models.ReadReceiptUser{}
		}

		responseList = append(responseList, models.MessageWithReceipts{
			Message:   m,
			ReadBy:    visibleUsers,
			ReadCount: totalRead, // Flutter-e "+97" dekhate (excluding sender)
		})
	}

	var nextBeforeID *int64 = nil
	if len(messages) > 0 && hasMore {
		// Chronological (ASC) list-e index 0 holo shobcheye purono message ID
		oldestId := messages[len(messages)-1].ID
		nextBeforeID = &oldestId
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"messages":       responseList,
		"has_more":       hasMore,
		"next_before_id": nextBeforeID,
	})
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

	if len(req.MemberIDs) == 0 {
		http.Error(w, "At least one member is required to create a group", http.StatusBadRequest)
		return
	}

	convID, err := h.chatSvc.CreateGroupChat(r.Context(), req.Title, currentUserID, req.MemberIDs)
	if err != nil {
		http.Error(w, "Failed to create group: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"conversation_id": convID,
		"title":           req.Title,
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
	if convID == "" {
		http.Error(w, "Missing conversation id", http.StatusBadRequest)
		return
	}

	var req struct {
		TargetUserID int `json:"target_user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TargetUserID == 0 {
		http.Error(w, "Invalid target_user_id", http.StatusBadRequest)
		return
	}

	if err := h.chatSvc.AddMemberToGroup(r.Context(), convID, req.TargetUserID, userID); err != nil {
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
	_, err := h.extractUserID(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	convID := r.PathValue("id")
	userIDStr := r.PathValue("user_id")
	targetUID, err := strconv.Atoi(userIDStr)
	if err != nil || targetUID == 0 {
		http.Error(w, "Invalid user_id", http.StatusBadRequest)
		return
	}

	if err := h.chatSvc.RemoveMemberFromGroup(r.Context(), convID, targetUID); err != nil {
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
	if convID == "" {
		http.Error(w, "Missing conversation id", http.StatusBadRequest)
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
		http.Error(w, "Failed to update avatar: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"message": "Group avatar updated successfully",
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
	if convID == "" {
		http.Error(w, "Missing conversation id", http.StatusBadRequest)
		return
	}

	members, err := h.chatSvc.GetConversationMembers(r.Context(), convID, userID)
	if err != nil {
		http.Error(w, "Failed to get members: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(members)
}
