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
	"github.com/nbd-wtf/go-nostr/nip29"
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
	relay, state := khatru29.Init(relay29.Options{
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
			9000, 9001, 9002, 9003, 9004, 9005, 9006, 9007, 9008, 9009, // Group management
			9021, 9022, 20284, // Group invitations
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
	eventProcessor := processor.New(rdb, relay, state)
	go func() {
		if err := eventProcessor.Start(ctx); err != nil {
			if ctx.Err() == nil { // Only log if not cancelled
				log.Printf("Event processor error: %v", err)
			}
		}
	}()

	// Add OnEventSaved policy for group management
	relay.OnEventSaved = append(relay.OnEventSaved, func(ctx context.Context, event *nostr.Event) {
		switch event.Kind {
		case 9007: // Group creation
			handleGroupCreation(ctx, event, state, eventProcessor)
		case 9002: // Edit group information
			handleGroupUpdate(ctx, event, state, eventProcessor)
		case 9008: // Delete group
			handleGroupDeletion(ctx, event, state, eventProcessor)
		}
	})

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

// handleGroupCreation handles group creation events (kind 9007)
func handleGroupCreation(ctx context.Context, event *nostr.Event, state *relay29.State, eventProcessor *processor.Processor) {
	log.Printf("Handling group creation event: [Kind: %d, ID: %s]", event.Kind, event.ID[:8])

	// Get newly created group from relay29.State
	groupID := extractGroupIDFromEvent(event)
	if groupID == "" {
		log.Printf("Failed to extract group ID from creation event: %s", event.ID[:8])
		return
	}

	// Get group information from state
	if group := getGroupFromState(state, groupID); group != nil {
		nip29Group := convertRelayGroupToNip29Group(group)
		if nip29Group != nil {
			eventProcessor.AddGroup(nip29Group)
			log.Printf("Successfully added new group to subscription matcher: %s", groupID)
		}
	} else {
		log.Printf("Newly created group not found in relay29.State: %s", groupID)
	}
}

// handleGroupUpdate handles group information editing events (kind 9002)
func handleGroupUpdate(ctx context.Context, event *nostr.Event, state *relay29.State, eventProcessor *processor.Processor) {
	log.Printf("Handling group update event: [Kind: %d, ID: %s]", event.Kind, event.ID[:8])

	groupID := extractGroupIDFromEvent(event)
	if groupID == "" {
		log.Printf("Failed to extract group ID from update event: %s", event.ID[:8])
		return
	}

	// Wait briefly to ensure relay29.State has been updated
	time.Sleep(100 * time.Millisecond)

	// Get updated group information from state
	if group := getGroupFromState(state, groupID); group != nil {
		nip29Group := convertRelayGroupToNip29Group(group)
		if nip29Group != nil {
			eventProcessor.UpdateGroup(nip29Group)
			log.Printf("Successfully updated group subscription information: %s", groupID)
		}
	} else {
		log.Printf("Group to update not found in relay29.State: %s", groupID)
	}
}

// handleGroupDeletion handles group deletion events (kind 9008)
func handleGroupDeletion(ctx context.Context, event *nostr.Event, state *relay29.State, eventProcessor *processor.Processor) {
	log.Printf("Handling group deletion event: [Kind: %d, ID: %s]", event.Kind, event.ID[:8])

	groupID := extractGroupIDFromEvent(event)
	if groupID == "" {
		log.Printf("Failed to extract group ID from deletion event: %s", event.ID[:8])
		return
	}

	// Remove group from subscription matcher
	eventProcessor.RemoveGroup(groupID)
	log.Printf("Successfully removed group from subscription matcher: %s", groupID)
}

// extractGroupIDFromEvent extracts group ID from event
func extractGroupIDFromEvent(event *nostr.Event) string {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "h" {
			return tag[1]
		}
	}
	return ""
}

// getGroupFromState gets group from relay29.State
func getGroupFromState(state *relay29.State, groupID string) *relay29.Group {
	var result *relay29.Group
	state.Groups.Range(func(id string, group *relay29.Group) bool {
		if id == groupID {
			result = group
			return false // stop iteration
		}
		return true // continue iteration
	})
	return result
}

// convertRelayGroupToNip29Group converts relay29.Group to nip29.Group
func convertRelayGroupToNip29Group(relayGroup *relay29.Group) *nip29.Group {
	if relayGroup == nil {
		return nil
	}
	return &relayGroup.Group
}
