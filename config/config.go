package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Port      string
	DBDSN     string
	JWTSecret string
}

func LoadConfig() *Config {
	if err := godotenv.Load(); err != nil {
		log.Println("Note: .env file not found, loading from system environment")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = ":8080"
	}

	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		dsn = "postgres://shakib@localhost:5432/gotalkdb?sslmode=disable"
	}

	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		secret = "default_secret_key"
	}

	return &Config{
		Port:      port,
		DBDSN:     dsn,
		JWTSecret: secret,
	}
}
