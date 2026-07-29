.PHONY: all fmt vet test cover bench build run tidy up up-obs down loadtest saturate breakdown proto demo ha-up ha-down failover failover-test

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

# same filtering as the CI gate: generated protobuf is excluded
cover:
	go test -covermode=atomic -coverprofile=coverage.out ./...
	@grep -v '/gen/' coverage.out > coverage.filtered.out
	@go tool cover -func=coverage.filtered.out | tail -1
	@echo "html report: go tool cover -html=coverage.filtered.out"

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

# with prometheus + grafana; left out of `up` so they don't compete with the
# service for CPU during a load run
up-obs:
	docker compose --profile observability up -d --wait

down:
	docker compose --profile observability down

loadtest:
	k6 run -e BASE_URL=$(BASE_URL) -e RATE=$(RATE) -e DURATION=$(DURATION) loadtest/check.js

saturate:
	./scripts/saturate.sh

# where a request's time actually goes, at light load and at the knee
breakdown:
	./scripts/latency_breakdown.sh 2000 20s
	./scripts/latency_breakdown.sh 12000 20s

demo:
	./demo/kill-redis.sh

# redis HA: master + replica + 3 sentinels
ha-up:
	docker compose -f docker-compose.ha.yml up -d --build --wait

ha-down:
	docker compose -f docker-compose.ha.yml down

failover:
	./demo/failover.sh

# sentinel hands out the master's address as a container hostname, which only
# resolves inside the compose network — so the sentinel-backed tests run in a
# container on that network rather than from the host
failover-test:
	docker run --rm --network distributed-rate-limiter_default \
		-v "$$PWD":/src -w /src \
		-e SENTINEL_ADDRS=sentinel1:26379,sentinel2:26379,sentinel3:26379 \
		golang:1.26-alpine go test ./internal/store/ -run Failover -v

proto:
	PATH="$(HOME)/go/bin:$$PATH" protoc \
		--go_out=. --go_opt=module=github.com/sohipan21/distributed-rate-limiter \
		--go-grpc_out=. --go-grpc_opt=module=github.com/sohipan21/distributed-rate-limiter \
		proto/ratelimit/v1/ratelimit.proto
