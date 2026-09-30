package repository

import (
	"context"
	"database/sql"
	"errors"
	"go-talk/models"
	"time"
)

type UserRepository interface {
	CreateUser(ctx context.Context, name, email, passwordHash string) (*models.User, error)
	GetByEmail(ctx context.Context, email string) (*models.User, error)
	GetByID(ctx context.Context, id int) (*models.User, error)
	UpdateAvatar(ctx context.Context, userID int, avatarURL string) error
	UpdateName(ctx context.Context, userID int, name string) error
	UpdateBio(ctx context.Context, userID int, bio string) error
	SaveRefreshToken(ctx context.Context, userID int, tokenStr string, expiresAt time.Time) error
	GetRefreshToken(ctx context.Context, tokenStr string) (int, error) // Returns userID
	DeleteRefreshToken(ctx context.Context, tokenStr string) error
}

type userRepo struct {
	db *sql.DB
}

func NewUserRepository(db *sql.DB) UserRepository {
	return &userRepo{db: db}
}

func (r *userRepo) CreateUser(ctx context.Context, name, email, passwordHash string) (*models.User, error) {
	query := `INSERT INTO users (name, email, password_hash) 
	          VALUES ($1, $2, $3) 
	          RETURNING id, name, email, created_at`
	var u models.User
	err := r.db.QueryRowContext(ctx, query, name, email, passwordHash).
		Scan(&u.ID, &u.Name, &u.Email, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *userRepo) GetByEmail(ctx context.Context, email string) (*models.User, error) {
	query := `SELECT id, name, email, password_hash, avatar_url, bio, created_at FROM users WHERE email = $1`
	var u models.User
	err := r.db.QueryRowContext(ctx, query, email).
		Scan(&u.ID, &u.Name, &u.Email, &u.PasswordHash, &u.AvatarURL, &u.Bio, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func (r *userRepo) GetByID(ctx context.Context, id int) (*models.User, error) {
	query := `SELECT id, name, email, avatar_url, bio, created_at FROM users WHERE id = $1`
	var u models.User
	err := r.db.QueryRowContext(ctx, query, id).
		Scan(&u.ID, &u.Name, &u.Email, &u.AvatarURL, &u.Bio, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func (r *userRepo) UpdateAvatar(ctx context.Context, userID int, avatarURL string) error {
	query := `UPDATE users SET avatar_url = $1 WHERE id = $2`
	_, err := r.db.ExecContext(ctx, query, avatarURL, userID)
	return err
}

func (r *userRepo) UpdateName(ctx context.Context, userID int, name string) error {
	query := `UPDATE users SET name = $1 WHERE id = $2`
	_, err := r.db.ExecContext(ctx, query, name, userID)
	return err
}

func (r *userRepo) UpdateBio(ctx context.Context, userID int, bio string) error {
	var err error
	if bio == "" {
		_, err = r.db.ExecContext(ctx, `UPDATE users SET bio = NULL WHERE id = $1`, userID)
	} else {
		_, err = r.db.ExecContext(ctx, `UPDATE users SET bio = $1 WHERE id = $2`, bio, userID)
	}
	return err
}

func (r *userRepo) SaveRefreshToken(ctx context.Context, userID int, tokenStr string, expiresAt time.Time) error {
	query := `INSERT INTO refresh_tokens (user_id, token, expires_at) VALUES ($1, $2, $3)`
	_, err := r.db.ExecContext(ctx, query, userID, tokenStr, expiresAt)
	return err
}
func (r *userRepo) GetRefreshToken(ctx context.Context, tokenStr string) (int, error) {
	var userID int
	query := `SELECT user_id FROM refresh_tokens WHERE token = $1 AND expires_at > NOW()`
	err := r.db.QueryRowContext(ctx, query, tokenStr).Scan(&userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("invalid or expired refresh token")
		}
		return 0, err
	}
	return userID, nil
}
func (r *userRepo) DeleteRefreshToken(ctx context.Context, tokenStr string) error {
	query := `DELETE FROM refresh_tokens WHERE token = $1`
	_, err := r.db.ExecContext(ctx, query, tokenStr)
	return err
}
