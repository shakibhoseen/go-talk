package token_test

import (
	"go-talk/pkg/token"
	"testing"
	"time"
)

func TestJWTMaker(t *testing.T) {
	maker, err := token.NewJWTMaker("test_secret_key_12345678901234567890123456789012")
	if err != nil {
		t.Fatalf("Failed to create token maker: %v", err)
	}

	t.Run("Valid token succeeds and returns correct userID", func(t *testing.T) {
		tokenStr, err := maker.CreateToken(123, "alice@test.com", 15*time.Minute)
		if err != nil {
			t.Fatalf("CreateToken failed: %v", err)
		}

		userID, err := maker.VerifyToken(tokenStr)
		if err != nil {
			t.Fatalf("VerifyToken failed: %v", err)
		}
		if userID != 123 {
			t.Errorf("Expected userID 123, got %d", userID)
		}
	})

	t.Run("Expired token returns error", func(t *testing.T) {
		// Create expired token with negative duration
		tokenStr, err := maker.CreateToken(123, "alice@test.com", -time.Minute)
		if err != nil {
			t.Fatalf("CreateToken failed: %v", err)
		}

		userID, err := maker.VerifyToken(tokenStr)
		if err == nil {
			t.Errorf("Expected error for expired token, got nil with userID %d", userID)
		}
	})

	t.Run("Invalid signature with different key returns error", func(t *testing.T) {
		otherMaker, err := token.NewJWTMaker("different_secret_key_12345678901234567890")
		if err != nil {
			t.Fatalf("Failed to create other token maker: %v", err)
		}

		tokenStr, err := otherMaker.CreateToken(123, "alice@test.com", 15*time.Minute)
		if err != nil {
			t.Fatalf("CreateToken failed: %v", err)
		}

		userID, err := maker.VerifyToken(tokenStr)
		if err == nil {
			t.Errorf("Expected error for invalid signature, got nil with userID %d", userID)
		}
	})

	t.Run("Tampered token string returns error", func(t *testing.T) {
		tokenStr, err := maker.CreateToken(123, "alice@test.com", 15*time.Minute)
		if err != nil {
			t.Fatalf("CreateToken failed: %v", err)
		}

		// Tamper with the last few characters
		tampered := tokenStr[:len(tokenStr)-4] + "xxxx"
		userID, err := maker.VerifyToken(tampered)
		if err == nil {
			t.Errorf("Expected error for tampered token, got nil with userID %d", userID)
		}
	})
}
