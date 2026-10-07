package main

import (
	"fmt"
	"go-talk/config"
	"go-talk/handlers"
	"go-talk/middleware"
	"go-talk/pkg/token"
	"go-talk/repository"
	"go-talk/service"
	"go-talk/websocket"
	"log"
	"net/http"
)

func main() {
	cfg := config.LoadConfig()

	// 1. Connect Postgres
	db := config.InitDB(cfg.DBDSN)
	defer db.Close()

	// 2. Repositories
	userRepo := repository.NewUserRepository(db)
	chatRepo := repository.NewChatRepository(db)

	tokenMaker, _ := token.NewJWTMaker(cfg.JWTSecret)

	// 3. Services
	authSvc := service.NewAuthService(userRepo, tokenMaker)
	chatSvc := service.NewChatService(chatRepo)

	// 4. WebSocket Hub (Single Concurrency Goroutine)
	hub := websocket.NewHub(chatRepo, userRepo)
	go hub.Run()

	// 5. Handlers
	authHandler := handlers.NewAuthHandler(authSvc)
	wsHandler := handlers.NewWSHandler(hub, authSvc)
	chatHandler := handlers.NewChatHandler(chatSvc, authSvc)

	// 6. Router Setup
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/register", authHandler.Register)
	mux.HandleFunc("POST /auth/login", authHandler.Login)
	mux.HandleFunc("POST /auth/refresh", authHandler.RefreshToken) // নতুন
	mux.HandleFunc("POST /auth/logout", authHandler.Logout)        // নতুন
	mux.HandleFunc("GET /users/me", authHandler.GetMe)
	mux.HandleFunc("GET /users", authHandler.GetUsers)
	mux.HandleFunc("PUT /users/me/name", authHandler.UpdateName)
	mux.HandleFunc("PUT /users/me/bio", authHandler.UpdateBio)
	mux.HandleFunc("POST /users/me/avatar", authHandler.UploadAvatar)
	mux.HandleFunc("GET /ws", wsHandler.Handle)

	// Notun Chat REST Endpoints:
	mux.HandleFunc("POST /conversations/direct", chatHandler.CreateDirectChat)
	mux.HandleFunc("GET /conversations", chatHandler.GetConversations)
	mux.HandleFunc("GET /conversations/{id}/messages", chatHandler.GetMessages)
	mux.HandleFunc("POST /conversations/group", chatHandler.CreateGroupChat)

	mux.HandleFunc("POST /conversations/{id}/members", chatHandler.AddMember)
	mux.HandleFunc("PUT /conversations/{id}/avatar", chatHandler.UpdateGroupAvatar)
	mux.HandleFunc("GET /conversations/{id}/members", chatHandler.GetConversationMembers)

	mux.HandleFunc("DELETE /conversations/{id}/members/{user_id}", chatHandler.RemoveMember)

	// Health check endpoint
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("go-talk engine is healthy!"))
	})

	// Static file server for uploaded images
	fs := http.FileServer(http.Dir("./uploads"))
	mux.Handle("GET /uploads/", http.StripPrefix("/uploads/", fs))

	fmt.Printf("🚀 go-talk server running on http://192.168.22.254%s\n", cfg.Port)
	log.Fatal(http.ListenAndServe(cfg.Port, middleware.RecoverMiddleware(mux)))
	// http.ListenAndServe(":8080", mux)
	// ba
	//log.Fatal(http.ListenAndServe("0.0.0.0:8080", mux))
}
