package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fiatjaf/eventstore/lmdb"
	"github.com/fiatjaf/khatru/policies"
	"github.com/fiatjaf/relay29"
	"github.com/fiatjaf/relay29/khatru29"
	"github.com/nbd-wtf/go-nostr"
	"github.com/redis/go-redis/v9"

	"nopu/internal/config"
	"nopu/internal/listener"
	"nopu/internal/processor"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Get relay private key from configuration
	relayPrivateKey := cfg.SubscriptionServer.RelayPrivateKey
	if relayPrivateKey == "" {
		log.Fatalf("Relay private key not configured. Please set relay_private_key in config.yaml or RELAY_PRIVATE_KEY environment variable")
	}

	relayPublicKey, err := nostr.GetPublicKey(relayPrivateKey)
	if err != nil {
		log.Fatalf("Invalid relay private key: %v", err)
	}

	fmt.Printf("Relay Private Key: %s\n", relayPrivateKey)
	fmt.Printf("Relay Public Key: %s\n", relayPublicKey)

	// Initialize LMDB storage
	db := &lmdb.LMDBBackend{
		Path: "./data/nopu.lmdb",
	}
	if err := db.Init(); err != nil {
		log.Fatalf("Failed to initialize LMDB: %v", err)
	}
	defer db.Close()

	// Initialize Redis
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})

	// Test Redis connection
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis connection failed: %v", err)
	}

	// Initialize NIP-29 relay
	relay, _ := khatru29.Init(relay29.Options{
		Domain:    cfg.SubscriptionServer.Domain,
		DB:        db,
		SecretKey: relayPrivateKey,
	})

	// Configure relay information
	relay.Info.Name = cfg.SubscriptionServer.RelayName
	relay.Info.Description = cfg.SubscriptionServer.RelayDescription

	// Set event restriction policies
	relay.RejectEvent = append(relay.RejectEvent,
		policies.PreventLargeTags(64),
		policies.PreventTooManyIndexableTags(6, []int{9005}, nil),
		policies.RestrictToSpecifiedKinds(
			9000, 9001, 9002, 9003, 9004, 9005, 9006, 9007, // Group management
			9021, // Group invitations
		),
		policies.PreventTimestampsInThePast(60*time.Second),
		policies.PreventTimestampsInTheFuture(30*time.Second),
	)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start event listener
	eventListener := listener.New(cfg.Listener, rdb)
	go func() {
		if err := eventListener.Start(ctx); err != nil {
			if ctx.Err() == nil { // Only log if not cancelled
				log.Printf("Event listener error: %v", err)
			}
		}
	}()

	// Start event processor
	eventProcessor := processor.New(rdb, relay)
	go func() {
		if err := eventProcessor.Start(ctx); err != nil {
			if ctx.Err() == nil { // Only log if not cancelled
				log.Printf("Event processor error: %v", err)
			}
		}
	}()

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("Nopu service started, relay running...")

	// Wait for shutdown signal
	<-sigChan
	log.Println("Received shutdown signal, shutting down service...")

	// Cancel context to stop all goroutines
	cancel()

	// Give components time to shutdown gracefully
	time.Sleep(2 * time.Second)

	// Close Redis connection last
	if err := rdb.Close(); err != nil {
		log.Printf("Redis close error: %v", err)
	}

	log.Println("Service shutdown complete")
}
