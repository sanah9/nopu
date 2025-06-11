# Nopu Project Management

.PHONY: help build run test clean deps docker-build docker-run

# Default target
help:
	@echo "Nopu - Free Open Source Nostr Push Service"
	@echo ""
	@echo "Available commands:"
	@echo "  deps         Install dependencies"
	@echo "  build        Build project"
	@echo "  run          Run service"
	@echo "  test         Run tests"
	@echo "  clean        Clean build files"

# Install dependencies
deps:
	go mod tidy
	go mod download

# Build project
build:
	go build -o bin/nopu cmd/main.go

# Run service
run:
	@mkdir -p logs
	@echo "Starting service, logs output to logs/nopu.log"
	go run cmd/main.go 2>&1 | tee logs/nopu.log

# Run tests
test:
	go test -v ./...

# Clean build files
clean:
	rm -rf bin/
	rm -rf logs/