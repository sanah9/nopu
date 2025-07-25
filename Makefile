# Nopu Project Management

.PHONY: help build build-subscription build-push run-subscription run-push run-both start test clean deps

# Default target
help:
	@echo "Nopu - Free Open Source Nostr Push Service"
	@echo ""
	@echo "Available commands:"
	@echo "  deps                    Install dependencies"
	@echo "  build                   Build all services"
	@echo "  build-subscription      Build subscription server"
	@echo "  build-push              Build push server"
	@echo "  run-subscription        Run subscription server"
	@echo "  run-push                Run push server"
	@echo "  run-both                Run both servers (tmux)"
	@echo "  start                   Start both services with script"
	@echo "  test                    Run tests"
	@echo "  clean                   Clean build files"

# Install dependencies
deps:
	go mod tidy
	go mod download

# Build all services
build: build-subscription build-push

# Build subscription server
build-subscription:
	@mkdir -p bin
	go build -o bin/nopu-subscription cmd/subscription-server/main.go

# Build push server
build-push:
	@mkdir -p bin
	go build -o bin/nopu-push cmd/push-server/main.go

# Run subscription server
run-subscription:
	@mkdir -p logs
	@echo "Starting subscription server, logs output to logs/subscription.log"
	go run cmd/subscription-server/main.go 2>&1 | tee logs/subscription.log

# Run push server
run-push:
	@mkdir -p logs
	@echo "Starting push server, logs output to logs/push.log"
	go run cmd/push-server/main.go 2>&1 | tee logs/push.log

# Run both servers (requires tmux)
run-both:
	@mkdir -p logs
	@echo "Starting both servers with tmux..."
	@if command -v tmux >/dev/null 2>&1; then \
		tmux new-session -d -s nopu 'make run-subscription'; \
		tmux split-window -h 'make run-push'; \
		tmux attach-session -t nopu; \
	else \
		echo "tmux not found. Please install tmux or run servers separately:"; \
		echo "  Terminal 1: make run-subscription"; \
		echo "  Terminal 2: make run-push"; \
	fi

# Start both services with script
start:
	@echo "Starting Nopu services with deployment script..."
	@./deploy.sh

# Run tests
test:
	go test -v ./...

# Run integration tests
test-integration:
	@echo "Running integration tests..."
	@./test_integration.sh

# Run test client
test-client:
	@echo "Running test client..."
	@go run test_client.go

# Run all tests
test-all: test test-integration

# Run benchmarks
benchmark:
	go test -bench=. -benchmem ./...

# Run specific service tests
test-subscription:
	go test -v -run TestSubscriptionServer ./test_subscription_server.go

test-push:
	go test -v -run TestPushServer ./test_push_server.go

# Clean build files
clean:
	rm -rf bin/
	rm -rf logs/