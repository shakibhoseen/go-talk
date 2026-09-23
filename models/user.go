package models

import (
	"errors"
	"net/mail"
	"strings"
	"time"
)

type User struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	AvatarURL    *string   `json:"avatar_url,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type RegisterRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (r *RegisterRequest) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Email = strings.TrimSpace(r.Email)
	if r.Name == "" || len(r.Name) < 3 {
		return errors.New("name must be at least 3 characters")
	}
	if _, err := mail.ParseAddress(r.Email); err != nil {
		return errors.New("invalid email address")
	}
	if len(r.Password) < 6 {
		return errors.New("password must be at least 6 characters")
	}
	return nil
}
