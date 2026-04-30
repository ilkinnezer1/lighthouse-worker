.PHONY: env dev run build tidy fmt vet

env:
	@test -f .env || (cp .env.example .env && echo "Created .env (edit MASTER_ENCRYPTION_KEY)")

dev: env
	go run ./cmd/worker

run: build
	./bin/worker

build:
	go build -o bin/worker ./cmd/worker

tidy:
	go mod tidy

fmt:
	go fmt ./...

vet:
	go vet ./...