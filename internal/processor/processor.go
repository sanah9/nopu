package processor

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"nopu/internal/listener"

	"github.com/fiatjaf/khatru"
	"github.com/nbd-wtf/go-nostr"
	"github.com/redis/go-redis/v9"
)

// Processor event processor
type Processor struct {
	redis *redis.Client
	relay *khatru.Relay
}

// New creates a new processor
func New(rdb *redis.Client, relay *khatru.Relay) *Processor {
	return &Processor{
		redis: rdb,
		relay: relay,
	}
}

// Start starts the processor
func (p *Processor) Start(ctx context.Context) error {
	log.Println("Starting event processor...")

	// Create consumer group
	groupName := "nopu-processors"
	consumerName := "processor-1"

	// Try to create consumer group (if it doesn't exist)
	p.redis.XGroupCreateMkStream(ctx, listener.EventStreamKey, groupName, "0")

	for {
		select {
		case <-ctx.Done():
			log.Println("Event processor stopped")
			return nil
		default:
			// Read events
			args := &redis.XReadGroupArgs{
				Group:    groupName,
				Consumer: consumerName,
				Streams:  []string{listener.EventStreamKey, ">"},
				Count:    10,
				Block:    1 * time.Second,
			}

			streams, err := p.redis.XReadGroup(ctx, args).Result()
			if err != nil {
				if err != redis.Nil {
					log.Printf("Failed to read event stream: %v", err)
				}
				continue
			}

			// Process events
			for _, stream := range streams {
				for _, message := range stream.Messages {
					p.processMessage(ctx, message, groupName)
				}
			}
		}
	}
}

// processMessage processes a single message
func (p *Processor) processMessage(ctx context.Context, msg redis.XMessage, groupName string) {
	defer func() {
		// Acknowledge message processing completion
		p.redis.XAck(ctx, listener.EventStreamKey, groupName, msg.ID)
	}()

	eventStr, ok := msg.Values["event"].(string)
	if !ok {
		log.Printf("Invalid message format: %v", msg.Values)
		return
	}

	// Deserialize event
	var event nostr.Event
	if err := json.Unmarshal([]byte(eventStr), &event); err != nil {
		log.Printf("Failed to deserialize event: %v", err)
		return
	}

	// Check if should forward to group
	if p.shouldForwardToGroup(&event) {
		p.forwardToGroup(ctx, &event)
	}
}

// shouldForwardToGroup determines whether to forward to group
func (p *Processor) shouldForwardToGroup(event *nostr.Event) bool {
	// Simple example: forward kind 1 (short text notes) and kind 7 (reactions)
	return event.Kind == 1 || event.Kind == 7
}

// forwardToGroup forwards event to group
func (p *Processor) forwardToGroup(ctx context.Context, event *nostr.Event) {
	// TODO: Implement subscription matching logic
	// Here should match corresponding subscription topics based on event content, tags, etc.
	// Then forward to corresponding NIP-29 groups

	// Example: Create a general group to receive all forwarded messages
	groupID := "general"

	// Modify event, add group tag
	forwardedEvent := &nostr.Event{
		ID:        event.ID,
		PubKey:    event.PubKey,
		CreatedAt: event.CreatedAt,
		Kind:      event.Kind,
		Tags:      append(event.Tags, nostr.Tag{"h", groupID}), // Add group tag
		Content:   "[Forwarded] " + event.Content,
	}

	// Recalculate signature
	forwardedEvent.Sign(event.PubKey)

	log.Printf("Forwarding event to group %s: [Kind: %d, ID: %s]", groupID, event.Kind, event.ID[:8])

	// Publish event to relay
}
