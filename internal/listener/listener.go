package listener

import (
	"context"
	"log"
	"sync"
	"time"

	"nopu/internal/config"
	"nopu/internal/queue"

	"github.com/nbd-wtf/go-nostr"
)

// RelayConnection represents a connection to a relay
type RelayConnection struct {
	URL           string
	Relay         *nostr.Relay
	RetryCount    int
	LastConnected time.Time
	Connected     bool
	mu            sync.RWMutex
}

// Listener event listener
type Listener struct {
	cfg        config.ListenerConfig
	queue      *queue.MemoryQueue
	relays     map[string]*RelayConnection
	relayMutex sync.RWMutex
}

// New creates a new listener
func New(cfg config.ListenerConfig, queue *queue.MemoryQueue) *Listener {
	return &Listener{
		cfg:    cfg,
		queue:  queue,
		relays: make(map[string]*RelayConnection),
	}
}

// Start starts the listener
func (l *Listener) Start(ctx context.Context) error {
	log.Println("Starting event listener...")

	// Initialize all relay connections
	for _, relayURL := range l.cfg.Relays {
		conn := &RelayConnection{
			URL:       relayURL,
			Connected: false,
		}
		l.relayMutex.Lock()
		l.relays[relayURL] = conn
		l.relayMutex.Unlock()

		// Start goroutine for each relay's connection management and listening
		go l.manageRelayConnection(ctx, conn)
	}

	<-ctx.Done()
	log.Println("Event listener stopped")
	return nil
}

// manageRelayConnection manages the connection and reconnection for a single relay
func (l *Listener) manageRelayConnection(ctx context.Context, conn *RelayConnection) {
	for {
		select {
		case <-ctx.Done():
			if conn.Relay != nil {
				conn.Relay.Close()
			}
			return
		default:
			if err := l.connectAndListen(ctx, conn); err != nil {
				conn.mu.Lock()
				conn.Connected = false
				conn.RetryCount++
				conn.mu.Unlock()

				// Check if maximum retry attempts reached
				if l.cfg.MaxRetries > 0 && conn.RetryCount >= l.cfg.MaxRetries {
					log.Printf("Relay %s reached maximum retry attempts (%d), stopping reconnection", conn.URL, l.cfg.MaxRetries)
					return
				}

				log.Printf("Lost connection to relay %s (attempt %d), reconnecting in %v...",
					conn.URL, conn.RetryCount, l.cfg.ReconnectDelay)

				// Wait for reconnect delay duration
				select {
				case <-ctx.Done():
					return
				case <-time.After(l.cfg.ReconnectDelay):
					continue
				}
			}
		}
	}
}

// connectAndListen connects to a relay and starts listening
func (l *Listener) connectAndListen(ctx context.Context, conn *RelayConnection) error {
	// Try to connect to relay
	relay, err := nostr.RelayConnect(ctx, conn.URL)
	if err != nil {
		return err
	}

	// Update connection status
	conn.mu.Lock()
	conn.Relay = relay
	conn.Connected = true
	conn.LastConnected = time.Now()
	conn.mu.Unlock()

	log.Printf("Successfully connected to relay: %s", conn.URL)

	// Create filters
	since := nostr.Timestamp(time.Now().Unix())
	filters := []nostr.Filter{
		{
			Kinds: l.kindIntToKind(l.cfg.Kinds),
			Since: &since,
		},
	}

	// Subscribe to events
	sub, err := relay.Subscribe(ctx, filters)
	if err != nil {
		relay.Close()
		return err
	}

	// Start listening for events
	for {
		select {
		case <-ctx.Done():
			sub.Unsub()
			relay.Close()
			return nil
		case event := <-sub.Events:
			if event == nil {
				continue
			}
			l.handleEvent(ctx, event)
		case <-relay.Context().Done():
			// Relay connection has been lost
			return relay.Context().Err()
		}
	}
}

// handleEvent handles received events
func (l *Listener) handleEvent(ctx context.Context, event *nostr.Event) {
	// Add event to memory queue (deduplication is handled by the queue)
	if err := l.queue.AddEvent(ctx, event); err != nil {
		log.Printf("Failed to add event to queue: %v", err)
		return
	}
}

// kindIntToKind converts int types to nostr.Kind
func (l *Listener) kindIntToKind(kinds []int) []int {
	return kinds // go-nostr uses int type
}
