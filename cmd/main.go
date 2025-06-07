package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fiatjaf/eventstore/slicestore"
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

	// Generate relay private key
	relayPrivateKey := nostr.GeneratePrivateKey()
	relayPublicKey, _ := nostr.GetPublicKey(relayPrivateKey)
	fmt.Printf("Relay Private Key: %s\n", relayPrivateKey)
	fmt.Printf("Relay Public Key: %s\n", relayPublicKey)

	// Initialize event storage (using in-memory storage for simplicity, database recommended for production)
	db := &slicestore.SliceStore{}
	db.Init()

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

	// TODO: Set group permission policies (needs adjustment for new API)

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

	// Start event listener
	eventListener := listener.New(cfg.Listener, rdb)
	go func() {
		if err := eventListener.Start(ctx); err != nil {
			log.Printf("Event listener error: %v", err)
		}
	}()

	// Start event processor
	eventProcessor := processor.New(rdb, relay)
	go func() {
		if err := eventProcessor.Start(ctx); err != nil {
			log.Printf("Event processor error: %v", err)
		}
	}()

	// Set HTTP routes
	relay.Router().HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `
		<h1>Nopu - Subscription-based Message Push Service</h1>
		<p>Please use a compatible Nostr client to connect</p>
		<p>WebSocket Address: ws://`+cfg.SubscriptionServer.Domain+`</p>
		`)
	})

	// Start HTTP server
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.SubscriptionServer.Port),
		Handler: relay,
	}

	// Graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		log.Println("Received shutdown signal, shutting down service...")

		if err := server.Shutdown(context.Background()); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}

		if err := rdb.Close(); err != nil {
			log.Printf("Redis close error: %v", err)
		}
	}()

	log.Printf("Nopu service started on http://0.0.0.0:%d", cfg.SubscriptionServer.Port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server startup failed: %v", err)
	}
}
