DB_URL=postgres://shakib@localhost:5432/gotalkdb?sslmode=disable

migrate-up:
	migrate -path db/migrations -database "$(DB_URL)" -verbose up

migrate-down:
	migrate -path db/migrations -database "$(DB_URL)" -verbose down 1
