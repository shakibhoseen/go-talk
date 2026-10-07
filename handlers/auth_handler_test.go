package handlers_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"go-talk/handlers"
	"go-talk/pkg/token"
	"go-talk/repository"
	"go-talk/service"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func TestGetUsersHandler(t *testing.T) {
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		dsn = "postgres://shakib@localhost:5432/gotalkdb?sslmode=disable"
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("Failed to connect to DB: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Skipf("Skipping test; DB unreachable: %v", err)
	}

	userRepo := repository.NewUserRepository(db)
	tokenMaker, err := token.NewJWTMaker("test_secret_key_12345678901234567890123456789012")
	if err != nil {
		t.Fatalf("Failed to create token maker: %v", err)
	}

	authSvc := service.NewAuthService(userRepo, tokenMaker)
	authHandler := handlers.NewAuthHandler(authSvc)

	now := time.Now().UnixNano()

	// Create 3 users: Alice (current user), Bob, Charlie
	var uAlice, uBob, uCharlie int
	err = db.QueryRow(`INSERT INTO users (name, email, password_hash, bio) VALUES ($1, $2, $3, $4) RETURNING id`,
		"Alice", fmt.Sprintf("alice_%d@test.com", now), "secret_hash_alice", "Hello Alice").Scan(&uAlice)
	if err != nil {
		t.Fatalf("Failed to create Alice: %v", err)
	}
	defer func() {
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1, $2, $3)`, uAlice, uBob, uCharlie)
	}()

	err = db.QueryRow(`INSERT INTO users (name, email, password_hash, bio) VALUES ($1, $2, $3, $4) RETURNING id`,
		"Bob", fmt.Sprintf("bob_%d@test.com", now), "secret_hash_bob", "Hello Bob").Scan(&uBob)
	if err != nil {
		t.Fatalf("Failed to create Bob: %v", err)
	}

	err = db.QueryRow(`INSERT INTO users (name, email, password_hash, bio) VALUES ($1, $2, $3, $4) RETURNING id`,
		"Charlie", fmt.Sprintf("charlie_%d@test.com", now), "secret_hash_charlie", "Hello Charlie").Scan(&uCharlie)
	if err != nil {
		t.Fatalf("Failed to create Charlie: %v", err)
	}

	aliceToken, err := tokenMaker.CreateToken(uAlice, "alice@test.com", 24*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create token: %v", err)
	}

	t.Run("Authenticated user can fetch users, current user excluded, password_hash not exposed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/users", nil)
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		rr := httptest.NewRecorder()

		authHandler.GetUsers(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d. Body: %s", rr.Code, rr.Body.String())
		}

		var resp map[string][]map[string]any
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		users, ok := resp["users"]
		if !ok {
			t.Fatalf("Expected 'users' key in response")
		}

		foundBob := false
		foundCharlie := false

		for _, u := range users {
			idFloat, ok := u["id"].(float64)
			if !ok {
				continue
			}
			id := int(idFloat)

			// Current user (Alice) must NOT be in the result
			if id == uAlice {
				t.Fatalf("Current logged-in user Alice (%d) should be excluded from result", uAlice)
			}
			if id == uBob {
				foundBob = true
				if u["name"] != "Bob" {
					t.Errorf("Expected Bob's name, got %v", u["name"])
				}
				if u["bio"] != "Hello Bob" {
					t.Errorf("Expected Bob's bio, got %v", u["bio"])
				}
			}
			if id == uCharlie {
				foundCharlie = true
			}

			// Password or password_hash must NEVER be exposed
			if _, hasHash := u["password_hash"]; hasHash {
				t.Fatalf("Security violation: password_hash exposed in response for user %d", id)
			}
			if _, hasPass := u["password"]; hasPass {
				t.Fatalf("Security violation: password exposed in response for user %d", id)
			}
		}

		if !foundBob || !foundCharlie {
			t.Errorf("Expected to find Bob and Charlie in users list (foundBob=%v, foundCharlie=%v)", foundBob, foundCharlie)
		}
	})

	t.Run("Unauthenticated request returns 401 Unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/users", nil)
		// No Authorization header
		rr := httptest.NewRecorder()

		authHandler.GetUsers(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("Expected status 401 for unauthenticated request, got %d", rr.Code)
		}
	})

	t.Run("GetUsers populates is_online and GetPresence returns online user IDs", func(t *testing.T) {
		mockP := &mockPresence{
			onlineIDs: map[int]bool{
				uBob: true,
			},
		}
		handlerWithPresence := handlers.NewAuthHandler(authSvc, mockP)

		// 1. Test /users returns is_online for Bob
		req := httptest.NewRequest(http.MethodGet, "/users", nil)
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		rr := httptest.NewRecorder()
		handlerWithPresence.GetUsers(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d", rr.Code)
		}

		var resp map[string][]map[string]any
		_ = json.NewDecoder(rr.Body).Decode(&resp)
		users := resp["users"]

		for _, u := range users {
			id := int(u["id"].(float64))
			isOnline, _ := u["is_online"].(bool)
			if id == uBob && !isOnline {
				t.Errorf("Expected Bob (%d) to be online", uBob)
			}
			if id == uCharlie && isOnline {
				t.Errorf("Expected Charlie (%d) to be offline", uCharlie)
			}
		}

		// 2. Test /presence
		reqPres := httptest.NewRequest(http.MethodGet, "/presence", nil)
		rrPres := httptest.NewRecorder()
		handlerWithPresence.GetPresence(rrPres, reqPres)

		if rrPres.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d", rrPres.Code)
		}

		var presResp map[string][]int
		_ = json.NewDecoder(rrPres.Body).Decode(&presResp)
		if len(presResp["online_user_ids"]) != 1 || presResp["online_user_ids"][0] != uBob {
			t.Errorf("Expected online_user_ids to contain Bob (%d), got %v", uBob, presResp["online_user_ids"])
		}
	})
}

type mockPresence struct {
	onlineIDs map[int]bool
}

func (m *mockPresence) IsUserOnline(userID int) bool {
	return m.onlineIDs[userID]
}

func (m *mockPresence) GetOnlineUserIDs() []int {
	var ids []int
	for id, on := range m.onlineIDs {
		if on {
			ids = append(ids, id)
		}
	}
	return ids
}
