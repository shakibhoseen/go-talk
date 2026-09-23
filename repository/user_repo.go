package repository

import (
	"context"
	"database/sql"
	"errors"
	"go-talk/models"
)

type UserRepository interface {
	CreateUser(ctx context.Context, name, email, passwordHash string) (*models.User, error)
	GetByEmail(ctx context.Context, email string) (*models.User, error)
	GetByID(ctx context.Context, id int) (*models.User, error)
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
	query := `SELECT id, name, email, password_hash, created_at FROM users WHERE email = $1`
	var u models.User
	err := r.db.QueryRowContext(ctx, query, email).
		Scan(&u.ID, &u.Name, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func (r *userRepo) GetByID(ctx context.Context, id int) (*models.User, error) {
	query := `SELECT id, name, email, created_at FROM users WHERE id = $1`
	var u models.User
	err := r.db.QueryRowContext(ctx, query, id).
		Scan(&u.ID, &u.Name, &u.Email, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}
