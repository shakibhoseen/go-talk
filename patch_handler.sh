#!/bin/bash
awk '
/h\.chatSvc\.AddMemberToGroup/ {
    gsub(/h\.chatSvc\.AddMemberToGroup\(r\.Context\(\), convID, req\.TargetUserID\)/, "h.chatSvc.AddMemberToGroup(r.Context(), convID, req.TargetUserID, userID)")
    print
    next
}
/_, err := h.extractUserID/ {
    gsub(/_, err := h.extractUserID/, "userID, err := h.extractUserID")
    print
    next
}
1
' handlers/chat_handler.go > tmp_handler.go && mv tmp_handler.go handlers/chat_handler.go

cat << 'EOF' >> handlers/chat_handler.go

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

	var req models.UpdateGroupAvatarRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if err := h.chatSvc.UpdateGroupAvatar(r.Context(), convID, req.AvatarURL, userID); err != nil {
		http.Error(w, "Failed to update avatar: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"message": "Group avatar updated successfully"})
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
