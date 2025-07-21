package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nopu/internal/config"
	"nopu/internal/push"
)

func main() {
	// Load configuration for push server only
	cfg, err := config.LoadPushServerConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Validate required configuration
	if cfg.PushServer.Apns.CertPath == "" {
		log.Printf("Warning: APNs certificate path not configured. Push notifications will not work.")
	}

	// Create push server
	server, err := push.NewServer(cfg)
	if err != nil {
		log.Fatalf("Failed to create push server: %v", err)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server
	go func() {
		if err := server.Start(ctx); err != nil {
			log.Fatalf("Push server failed: %v", err)
		}
	}()

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("Push server started on port %d", cfg.PushServer.Port)

	// Wait for shutdown signal
	<-sigChan
	log.Println("Received shutdown signal, shutting down push server...")

	// Cancel context to stop all goroutines
	cancel()

	// Give components time to shutdown gracefully
	time.Sleep(2 * time.Second)

	// Shutdown server
	server.Shutdown()

	log.Println("Push server shutdown complete")
}
