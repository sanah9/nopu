package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"nopu/internal/config"
	"nopu/internal/push"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Create push server
	server, err := push.NewServer(cfg)
	if err != nil {
		log.Fatalf("Failed to create push server: %v", err)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received shutdown signal, shutting down push server...")
		cancel()
	}()

	// Start server
	log.Printf("Push server started on port %d", cfg.PushServer.Port)
	if err := server.Start(ctx); err != nil {
		log.Printf("Server error: %v", err)
	}

	log.Println("Push server shutdown complete")
}
