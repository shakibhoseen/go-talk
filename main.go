package main

import (
	"fmt"
	"go-talk/config"
	"go-talk/handlers"
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

	// 3. Services
	authSvc := service.NewAuthService(userRepo, cfg.JWTSecret)

	// 4. WebSocket Hub (Single Concurrency Goroutine)
	hub := websocket.NewHub(chatRepo)
	go hub.Run()

	// 5. Handlers
	authHandler := handlers.NewAuthHandler(authSvc)
	wsHandler := handlers.NewWSHandler(hub, authSvc)

	// 6. Router Setup
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/register", authHandler.Register)
	mux.HandleFunc("POST /auth/login", authHandler.Login)
	mux.HandleFunc("GET /ws", wsHandler.Handle)

	// Health check endpoint
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("go-talk engine is healthy!"))
	})

	fmt.Printf("🚀 go-talk server running on http://localhost%s\n", cfg.Port)
	log.Fatal(http.ListenAndServe(cfg.Port, mux))
}
