package service

import (
	"context"
	"errors"
	"go-talk/models"
	"go-talk/pkg/token"
	"go-talk/repository"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type AuthService interface {
	Register(ctx context.Context, req *models.RegisterRequest) (*models.User, error)
	Login(ctx context.Context, req *models.LoginRequest) (*models.AuthResponse, error)
	ValidateToken(tokenStr string) (int, error)
	GetUserProfile(ctx context.Context, userID int) (*models.User, error)
	UpdateUserAvatar(ctx context.Context, userID int, avatarURL string) error
	UpdateUserName(ctx context.Context, userID int, name string) error
	UpdateUserBio(ctx context.Context, userID int, bio string) error
	RefreshToken(ctx context.Context, req *models.RefreshRequest) (*models.AuthResponse, error)
	Logout(ctx context.Context, req *models.RefreshRequest) error
}

type authService struct {
	userRepo   repository.UserRepository
	tokenMaker token.Maker
}

func NewAuthService(userRepo repository.UserRepository, tokenMaker token.Maker) AuthService {
	return &authService{
		userRepo:   userRepo,
		tokenMaker: tokenMaker,
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

func (s *authService) Login(ctx context.Context, req *models.LoginRequest) (*models.AuthResponse, error) {
	u, err := s.userRepo.GetByEmail(ctx, req.Email)
	if err != nil || u == nil {
		return nil, errors.New("invalid email or password")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		return nil, errors.New("invalid email or password")
	}
	// ১. Access Token তৈরি (মেয়াদ ১৫ মিনিট)
	accessToken, err := s.tokenMaker.CreateToken(u.ID, u.Email, 15*time.Minute)
	if err != nil {
		return nil, err
	}
	// ২. Refresh Token তৈরি (মেয়াদ ৭ দিন)
	refreshToken, err := s.tokenMaker.CreateToken(u.ID, u.Email, 7*24*time.Hour)
	if err != nil {
		return nil, err
	}
	// ৩. Refresh Token ডাটাবেসে সেভ করা
	err = s.userRepo.SaveRefreshToken(ctx, u.ID, refreshToken, time.Now().Add(7*24*time.Hour))
	if err != nil {
		return nil, err
	}
	u.PasswordHash = ""
	return &models.AuthResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		User:         u,
	}, nil
}

func (s *authService) ValidateToken(tokenStr string) (int, error) {
	// আগের সব কোড মুছে শুধু tokenMaker কে কল করুন
	return s.tokenMaker.VerifyToken(tokenStr)
}

func (s *authService) GetUserProfile(ctx context.Context, userID int) (*models.User, error) {
	u, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, errors.New("user not found")
	}
	u.PasswordHash = "" // Security: Hash jeno json response-e na jay
	return u, nil
}

func (s *authService) UpdateUserAvatar(ctx context.Context, userID int, avatarURL string) error {
	return s.userRepo.UpdateAvatar(ctx, userID, avatarURL)
}

func (s *authService) UpdateUserName(ctx context.Context, userID int, name string) error {
	return s.userRepo.UpdateName(ctx, userID, name)
}

func (s *authService) UpdateUserBio(ctx context.Context, userID int, bio string) error {
	return s.userRepo.UpdateBio(ctx, userID, bio)
}

func (s *authService) RefreshToken(ctx context.Context, req *models.RefreshRequest) (*models.AuthResponse, error) {
	// ১. টোকেনটি ভ্যালিড কি না চেক করুন
	userID, err := s.tokenMaker.VerifyToken(req.RefreshToken)
	if err != nil {
		return nil, errors.New("unauthorized: invalid refresh token")
	}

	// ২. ডাটাবেসে টোকেনটি আছে কি না চেক করুন (White-listing)
	dbUserID, err := s.userRepo.GetRefreshToken(ctx, req.RefreshToken)
	if err != nil || dbUserID != userID {
		return nil, errors.New("unauthorized: token revoked or not found")
	}

	u, err := s.userRepo.GetByID(ctx, userID)
	if err != nil || u == nil {
		return nil, errors.New("user not found")
	}

	// ৩. নতুন Access Token তৈরি করুন
	accessToken, err := s.tokenMaker.CreateToken(u.ID, u.Email, 15*time.Minute)
	if err != nil {
		return nil, err
	}

	u.PasswordHash = ""
	return &models.AuthResponse{
		AccessToken:  accessToken,
		RefreshToken: req.RefreshToken,
		User:         u,
	}, nil
}

func (s *authService) Logout(ctx context.Context, req *models.RefreshRequest) error {
	return s.userRepo.DeleteRefreshToken(ctx, req.RefreshToken)
}
