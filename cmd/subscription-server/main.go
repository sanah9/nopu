package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nopu/internal/config"
	"nopu/internal/subscription"
)

func main() {
	// Load configuration for subscription server only
	cfg, err := config.LoadSubscriptionServerConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Validate required configuration
	if cfg.SubscriptionServer.RelayPrivateKey == "" {
		log.Fatalf("Relay private key not configured. Please set relay_private_key in config.yaml or RELAY_PRIVATE_KEY environment variable")
	}

	// Create subscription server
	server, err := subscription.NewServer(cfg)
	if err != nil {
		log.Fatalf("Failed to create subscription server: %v", err)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server
	go func() {
		if err := server.Start(ctx); err != nil {
			log.Fatalf("Subscription server failed: %v", err)
		}
	}()

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("Subscription server started on port %d", cfg.SubscriptionServer.Port)

	// Wait for shutdown signal
	<-sigChan
	log.Println("Received shutdown signal, shutting down subscription server...")

	// Cancel context to stop all goroutines
	cancel()

	// Give components time to shutdown gracefully
	time.Sleep(2 * time.Second)

	// Shutdown server
	server.Shutdown()

	log.Println("Subscription server shutdown complete")
}
