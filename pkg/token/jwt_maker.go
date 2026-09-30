package token

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Maker interface: এটি টোকেন তৈরি এবং ভ্যালিডেট করার মেথডগুলো ডিফাইন করে
type Maker interface {
	CreateToken(userID int, email string, duration time.Duration) (string, error)
	VerifyToken(tokenStr string) (int, error)
}

type JWTMaker struct {
	secretKey string
}

func NewJWTMaker(secretKey string) (Maker, error) {
	if len(secretKey) < 8 {
		return nil, errors.New("invalid key size: must be at least 8 characters")
	}
	return &JWTMaker{secretKey: secretKey}, nil
}

// CreateToken নতুন JWT টোকেন তৈরি করে (Access বা Refresh যেকোনোটি হতে পারে duration এর ওপর ভিত্তি করে)
func (maker *JWTMaker) CreateToken(userID int, email string, duration time.Duration) (string, error) {
	claims := jwt.MapClaims{
		"user_id": userID,
		"email":   email,
		"exp":     time.Now().Add(duration).Unix(),
		"iat":     time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(maker.secretKey))
}

// VerifyToken একটি টোকেন চেক করে এবং এর ভেতর থেকে UserID রিটার্ন করে
func (maker *JWTMaker) VerifyToken(tokenStr string) (int, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(maker.secretKey), nil
	})

	if err != nil || !token.Valid {
		return 0, errors.New("invalid or expired token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return 0, errors.New("invalid token payload")
	}

	userIDFloat, ok := claims["user_id"].(float64)
	if !ok {
		return 0, errors.New("missing user_id claim")
	}

	return int(userIDFloat), nil
}
