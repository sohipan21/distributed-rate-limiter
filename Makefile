.PHONY: all fmt vet test bench build run tidy up down

all: fmt vet test

fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

test:
	go test -race ./...

bench:
	go test -bench=. -benchmem -run='^$$' ./internal/limiter/

build:
	go build -o bin/server ./cmd/server

run:
	go run ./cmd/server

tidy:
	go mod tidy

up:
	docker compose up -d --wait

down:
	docker compose down
