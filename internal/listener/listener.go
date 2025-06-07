package listener

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"nopu/internal/config"

	"github.com/nbd-wtf/go-nostr"
	"github.com/redis/go-redis/v9"
)

const (
	EventStreamKey = "nopu:events"
)

// Listener event listener
type Listener struct {
	cfg    config.ListenerConfig
	redis  *redis.Client
	relays []*nostr.Relay
}

// New creates a new listener
func New(cfg config.ListenerConfig, rdb *redis.Client) *Listener {
	return &Listener{
		cfg:   cfg,
		redis: rdb,
	}
}

// Start starts the listener
func (l *Listener) Start(ctx context.Context) error {
	log.Println("Starting event listener...")

	// Connect to all relays
	for _, relayURL := range l.cfg.Relays {
		relay, err := nostr.RelayConnect(ctx, relayURL)
		if err != nil {
			log.Printf("Failed to connect to relay %s: %v", relayURL, err)
			continue
		}
		l.relays = append(l.relays, relay)
		log.Printf("Successfully connected to relay: %s", relayURL)
	}

	if len(l.relays) == 0 {
		log.Println("No relays connected successfully")
		return nil
	}

	// Create filters
	since := nostr.Timestamp(time.Now().Unix())
	filters := []nostr.Filter{
		{
			Kinds: l.kindIntToKind(l.cfg.Kinds),
			Since: &since,
		},
	}

	// Start listening for each relay
	for _, relay := range l.relays {
		go l.listenToRelay(ctx, relay, filters)
	}

	<-ctx.Done()
	log.Println("Event listener stopped")
	return nil
}

// listenToRelay listens to a single relay
func (l *Listener) listenToRelay(ctx context.Context, relay *nostr.Relay, filters []nostr.Filter) {
	sub, err := relay.Subscribe(ctx, filters)
	if err != nil {
		log.Printf("Failed to subscribe to relay %s: %v", relay.URL, err)
		return
	}

	log.Printf("Started listening to relay: %s", relay.URL)

	for {
		select {
		case event := <-sub.Events:
			if event == nil {
				continue
			}
			l.handleEvent(ctx, event)
		case <-ctx.Done():
			sub.Unsub()
			relay.Close()
			return
		}
	}
}

// handleEvent handles received events
func (l *Listener) handleEvent(ctx context.Context, event *nostr.Event) {
	// Serialize event
	eventData, err := json.Marshal(event)
	if err != nil {
		log.Printf("Failed to serialize event: %v", err)
		return
	}

	// Add to Redis stream
	args := &redis.XAddArgs{
		Stream: EventStreamKey,
		Values: map[string]interface{}{
			"event": string(eventData),
			"kind":  event.Kind,
			"id":    event.ID,
		},
	}

	if err := l.redis.XAdd(ctx, args).Err(); err != nil {
		log.Printf("Failed to add event to Redis stream: %v", err)
		return
	}

	log.Printf("Received event [Kind: %d, ID: %s] from relay", event.Kind, event.ID[:8])
}

// kindIntToKind converts int types to nostr.Kind
func (l *Listener) kindIntToKind(kinds []int) []int {
	return kinds // go-nostr uses int type
}
