.PHONY: all fmt vet test bench build run tidy up down loadtest

BASE_URL ?= http://localhost:8080
RATE ?= 300
DURATION ?= 30s

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

loadtest:
	k6 run -e BASE_URL=$(BASE_URL) -e RATE=$(RATE) -e DURATION=$(DURATION) loadtest/check.js
