package service

import (
	"context"
	"errors"
	"go-talk/models"
	"go-talk/repository"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type AuthService interface {
	Register(ctx context.Context, req *models.RegisterRequest) (*models.User, error)
	Login(ctx context.Context, req *models.LoginRequest) (string, *models.User, error)
	ValidateToken(tokenStr string) (int, error)
}

type authService struct {
	userRepo  repository.UserRepository
	jwtSecret []byte
}

func NewAuthService(userRepo repository.UserRepository, jwtSecret string) AuthService {
	return &authService{
		userRepo:  userRepo,
		jwtSecret: []byte(jwtSecret),
	}
}

func (s *authService) Register(ctx context.Context, req *models.RegisterRequest) (*models.User, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	existing, err := s.userRepo.GetByEmail(ctx, req.Email)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, errors.New("email already registered")
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	return s.userRepo.CreateUser(ctx, req.Name, req.Email, string(hashed))
}

func (s *authService) Login(ctx context.Context, req *models.LoginRequest) (string, *models.User, error) {
	u, err := s.userRepo.GetByEmail(ctx, req.Email)
	if err != nil || u == nil {
		return "", nil, errors.New("invalid email or password")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		return "", nil, errors.New("invalid email or password")
	}

	claims := jwt.MapClaims{
		"user_id": u.ID,
		"email":   u.Email,
		"exp":     time.Now().Add(72 * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return "", nil, err
	}

	return tokenStr, u, nil
}

func (s *authService) ValidateToken(tokenStr string) (int, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return s.jwtSecret, nil
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
