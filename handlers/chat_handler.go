package handlers

import (
	"encoding/json"
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

	msgs, err := h.chatSvc.GetChatMessages(r.Context(), convID, limit, beforeID)
	if err != nil {
		http.Error(w, "Failed to fetch messages", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"messages": msgs,
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
	_, err := h.extractUserID(r)
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

	if err := h.chatSvc.AddMemberToGroup(r.Context(), convID, req.TargetUserID); err != nil {
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
