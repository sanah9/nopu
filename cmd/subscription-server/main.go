package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"nopu/internal/config"
	"nopu/internal/subscription"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Create subscription server
	server, err := subscription.NewServer(cfg)
	if err != nil {
		log.Fatalf("Failed to create subscription server: %v", err)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received shutdown signal, shutting down subscription server...")
		cancel()
	}()

	// Start server
	log.Printf("Subscription server started on port %d", cfg.SubscriptionServer.Port)
	if err := server.Start(ctx); err != nil {
		log.Printf("Server error: %v", err)
	}

	// Shutdown
	server.Shutdown()
	log.Println("Subscription server shutdown complete")
}
