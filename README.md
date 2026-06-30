# Distributed Rate Limiter

A distributed rate limiter as a service: enforces request limits consistently
across multiple nodes using Redis with atomic Lua scripts, and degrades
gracefully when Redis is unavailable. Built in Go with gRPC, a REST shim, and
full Prometheus/Grafana observability.
