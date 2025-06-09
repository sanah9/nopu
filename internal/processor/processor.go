package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"nopu/internal/listener"

	"github.com/fiatjaf/khatru"
	"github.com/fiatjaf/relay29"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip29"
	"github.com/redis/go-redis/v9"
)

// Processor event processor
type Processor struct {
	redis               *redis.Client
	relay               *khatru.Relay
	state               *relay29.State
	subscriptionMatcher *SubscriptionMatcher
	relayPrivateKey     string
}

// New creates a new processor
func New(rdb *redis.Client, relay *khatru.Relay, state *relay29.State, relayPrivateKey string) *Processor {
	return &Processor{
		redis:               rdb,
		relay:               relay,
		state:               state,
		subscriptionMatcher: NewSubscriptionMatcher(),
		relayPrivateKey:     relayPrivateKey,
	}
}

// Start starts the processor
func (p *Processor) Start(ctx context.Context) error {
	log.Println("Starting event processor...")

	// Load all group information on initialization
	if err := p.loadGroups(ctx); err != nil {
		log.Printf("Failed to load groups: %v", err)
	}

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

// loadGroups loads all group information from relay29.State to subscription matcher
func (p *Processor) loadGroups(ctx context.Context) error {
	log.Println("Loading groups from relay29.State for subscription matching...")

	// Get all groups from relay29.State
	groupCount := 0
	p.state.Groups.Range(func(groupID string, group *relay29.Group) bool {
		nip29Group := p.convertRelayGroupToNip29Group(group)
		if nip29Group != nil {
			p.subscriptionMatcher.AddGroup(nip29Group)
			groupCount++
			log.Printf("Loaded group %s: %s", groupID, nip29Group.Name)
		}
		return true // continue iteration
	})

	log.Printf("Loaded %d groups for subscription matching", groupCount)
	return nil
}

// convertRelayGroupToNip29Group converts relay29.Group to nip29.Group
func (p *Processor) convertRelayGroupToNip29Group(relayGroup *relay29.Group) *nip29.Group {
	if relayGroup == nil {
		return nil
	}

	return &relayGroup.Group
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

	// Use subscription matcher to find matching groups
	matchingGroups := p.subscriptionMatcher.GetMatchingGroups(&event)

	// Log match result (for debugging)
	p.subscriptionMatcher.LogMatchResult(&event)

	// Forward event to each matching group
	for _, group := range matchingGroups {
		if err := p.forwardToGroup(ctx, &event, group); err != nil {
			log.Printf("Failed to forward event %s to group %s: %v",
				event.ID[:8], group.Address.ID, err)
		}
	}
}

// forwardToGroup forwards event to a specific group
func (p *Processor) forwardToGroup(ctx context.Context, event *nostr.Event, group *nip29.Group) error {
	log.Printf("Forwarding event to group %s: [Kind: %d, ID: %s]",
		group.Address.ID, event.Kind, event.ID[:8])

	// Serialize original event to JSON
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to serialize event to JSON: %w", err)
	}

	// Create kind 20284 event
	kind20284Event := &nostr.Event{
		Kind:      20284,
		Content:   string(eventJSON),
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			// Add group ID as h tag
			{"h", group.Address.ID},
		},
	}

	// Sign the event with relay private key
	if err := kind20284Event.Sign(p.relayPrivateKey); err != nil {
		return fmt.Errorf("failed to sign kind 20284 event: %w", err)
	}

	// Broadcast the wrapped event to the group
	if p.state.Relay != nil {
		p.state.Relay.BroadcastEvent(kind20284Event)
		log.Printf("Forwarded event %s to group %s as kind 20284 event %s",
			event.ID[:8], group.Address.ID, kind20284Event.ID[:8])
	} else {
		log.Printf("Warning: No relay instance available for broadcasting")
	}

	return nil
}

// AddGroup adds new group to subscription matcher
func (p *Processor) AddGroup(group *nip29.Group) {
	p.subscriptionMatcher.AddGroup(group)
	log.Printf("Added group %s to subscription matcher", group.Address.ID)
}

// RemoveGroup removes group from subscription matcher
func (p *Processor) RemoveGroup(groupID string) {
	p.subscriptionMatcher.RemoveGroup(groupID)
	log.Printf("Removed group %s from subscription matcher", groupID)
}

// UpdateGroup updates group's subscription information
func (p *Processor) UpdateGroup(group *nip29.Group) {
	p.subscriptionMatcher.UpdateGroup(group)
	log.Printf("Updated group %s subscription", group.Address.ID)
}

// RefreshGroupsFromState refreshes groups from relay29.State
func (p *Processor) RefreshGroupsFromState(ctx context.Context) error {
	log.Println("Refreshing groups from relay29.State...")

	// Clear existing matcher and reload
	p.subscriptionMatcher = NewSubscriptionMatcher()

	return p.loadGroups(ctx)
}

// ValidateGroupSubscription validates group's subscription format
func (p *Processor) ValidateGroupSubscription(about string) error {
	return p.subscriptionMatcher.ValidateREQFormat(about)
}

// GetProcessorStats gets processor statistics
func (p *Processor) GetProcessorStats() map[string]interface{} {
	stats := make(map[string]interface{})

	// Get subscription matcher statistics
	matcherStats := p.subscriptionMatcher.GetGroupStats()
	stats["subscription_matcher"] = matcherStats

	// Get relay29.State statistics
	relayStats := make(map[string]interface{})
	totalGroups := 0
	p.state.Groups.Range(func(groupID string, group *relay29.Group) bool {
		totalGroups++
		return true
	})
	relayStats["total_groups_in_state"] = totalGroups
	stats["relay29_state"] = relayStats

	return stats
}
