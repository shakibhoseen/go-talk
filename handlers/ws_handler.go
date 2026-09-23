package handlers

import (
	"go-talk/service"
	"go-talk/websocket"
	"net/http"
)

type WSHandler struct {
	hub     *websocket.Hub
	authSvc service.AuthService
}

func NewWSHandler(hub *websocket.Hub, authSvc service.AuthService) *WSHandler {
	return &WSHandler{hub: hub, authSvc: authSvc}
}

func (h *WSHandler) Handle(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "Unauthorized: missing token query param", http.StatusUnauthorized)
		return
	}

	userID, err := h.authSvc.ValidateToken(token)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	// Upgrade HTTP connection to duplex WebSocket with authenticated user ID
	websocket.ServeWs(h.hub, w, r, userID)
}
