package config

import (
	"database/sql"
	"log"
	"time"

	_ "github.com/lib/pq"
)

func InitDB(dsn string) *sql.DB {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatal("DB open error:", err)
	}

	// High concurrency connection pool settings
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		log.Fatal("DB ping failed:", err)
	}

	return db
}
