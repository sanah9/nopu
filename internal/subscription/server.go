package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/fiatjaf/eventstore/lmdb"
	"github.com/fiatjaf/khatru"
	"github.com/fiatjaf/khatru/policies"
	"github.com/fiatjaf/relay29"
	"github.com/fiatjaf/relay29/khatru29"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip29"

	"nopu/internal/config"
	"nopu/internal/listener"
	"nopu/internal/presence"
	"nopu/internal/processor"
	"nopu/internal/push"
	"nopu/internal/queue"
)

// Server represents the subscription server
type Server struct {
	cfg          *config.Config
	db           *lmdb.LMDBBackend
	queue        *queue.MemoryQueue
	relay        *khatru.Relay
	state        *relay29.State
	listener     *listener.Listener
	processor    *processor.Processor
	eventChan    chan *nostr.Event
	shutdownChan chan struct{}
	pushClient   *http.Client
	pushURL      string
}

// NewServer creates a new subscription server
func NewServer(cfg *config.Config) (*Server, error) {
	// Initialize LMDB storage
	db := &lmdb.LMDBBackend{
		Path: "./data/nopu.lmdb",
	}
	if err := db.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize LMDB: %v", err)
	}

	// Initialize memory queue
	queue := queue.NewMemoryQueue(cfg.MemoryQueue.MaxSize, cfg.MemoryQueue.DedupeTTL)

	// Initialize NIP-29 relay
	relay, state := khatru29.Init(relay29.Options{
		Domain:                  cfg.SubscriptionServer.Domain,
		DB:                      db,
		SecretKey:               cfg.SubscriptionServer.RelayPrivateKey,
		DefaultRoles:            []*nip29.Role{{Name: "king", Description: "the group's max top admin"}},
		GroupCreatorDefaultRole: &nip29.Role{Name: "king", Description: "the group's max top admin"},
	})

	// Set up group-related permissions
	state.AllowAction = func(ctx context.Context, group nip29.Group, role *nip29.Role, action relay29.Action) bool {
		// Group creators (king role) can do everything
		if role.Name == "king" {
			return true
		}
		// By default, deny other actions
		return false
	}

	// Install presence tracking hooks
	presence.SetupPresenceHooks(relay)

	// Configure relay information
	relay.Info.Name = cfg.SubscriptionServer.RelayName
	relay.Info.Description = cfg.SubscriptionServer.RelayDescription

	// Set event restriction policies
	relay.RejectEvent = append(relay.RejectEvent,
		policies.PreventLargeTags(64),
		policies.PreventTooManyIndexableTags(6, []int{9005}, nil),
		policies.RestrictToSpecifiedKinds(
			true,
			9000, 9001, 9002, 9003, 9004, 9005, 9006, 9007, 9008, 9009, // Group management
			9021, 9022, 20284, // Group invitations
		),
		policies.PreventTimestampsInThePast(60*time.Second),
		policies.PreventTimestampsInTheFuture(30*time.Second),
	)

	// Initialize event listener
	eventListener := listener.New(cfg.SubscriptionServer.Listener, queue)

	// Initialize APNS client if configured
	var apnsClient *push.APNSClient
	if cfg.SubscriptionServer.APNS.CertPath != "" {
		var err error
		apnsClient, err = push.NewAPNSClient(cfg.SubscriptionServer.APNS)
		if err != nil {
			log.Printf("Warning: Failed to initialize APNS client: %v", err)
			apnsClient = nil
		} else {
			log.Printf("APNS client initialized successfully")
		}
	} else {
		log.Printf("APNS config not provided, push notifications will be disabled")
	}

	// Initialize processor with APNS client
	processor := processor.New(queue, relay, state, cfg.SubscriptionServer.RelayPrivateKey, apnsClient)

	server := &Server{
		cfg:          cfg,
		db:           db,
		queue:        queue,
		relay:        relay,
		state:        state,
		listener:     eventListener,
		processor:    processor,
		eventChan:    make(chan *nostr.Event, 1000),
		shutdownChan: make(chan struct{}),
		pushClient:   &http.Client{Timeout: 10 * time.Second},
		pushURL:      cfg.SubscriptionServer.PushServerURL + "/push",
	}

	// Set up event processing
	relay.OnEventSaved = append(relay.OnEventSaved, func(ctx context.Context, event *nostr.Event) {
		if err := server.handleEvent(ctx, event); err != nil {
			log.Printf("Error handling event: %v", err)
		}
	})

	return server, nil
}

// Start starts the subscription server
func (s *Server) Start(ctx context.Context) error {
	// Start event listener
	go func() {
		if err := s.listener.Start(ctx); err != nil {
			if ctx.Err() == nil {
				log.Printf("Event listener error: %v", err)
			}
		}
	}()

	// Start event processor
	go func() {
		if err := s.processor.Start(ctx); err != nil {
			if ctx.Err() == nil {
				log.Printf("Event processor error: %v", err)
			}
		}
	}()

	// Start HTTP server
	addr := fmt.Sprintf(":%d", s.cfg.SubscriptionServer.Port)
	log.Printf("Starting subscription server on %s", addr)

	// Set up relay HTTP handler (for standard Nostr protocol)
	http.HandleFunc("/", s.handleRelay)
	http.HandleFunc("/health", s.handleHealth)

	return http.ListenAndServe(addr, nil)
}

// handleRelay handles standard Nostr relay requests
func (s *Server) handleRelay(w http.ResponseWriter, r *http.Request) {
	// Delegate to the khatru relay for standard Nostr protocol handling
	s.relay.ServeHTTP(w, r)
}

// handleHealth handles health check requests
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// The number of clients is now managed by the khatru relay's state.
	// For now, we'll return a placeholder value since the relay doesn't expose client count.
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "healthy",
		"clients": 0, // Client count is managed by khatru relay
	})
}

// SendPushNotification sends a push notification via the push server
func (s *Server) SendPushNotification(ctx context.Context, deviceToken, title, body string, customData map[string]interface{}) error {
	payload := map[string]interface{}{
		"device_token": deviceToken,
		"title":        title,
		"body":         body,
		"custom_data":  customData,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal push payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.pushURL, bytes.NewBuffer(data))
	if err != nil {
		return fmt.Errorf("failed to create push request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := s.pushClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send push request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("push server returned status %d", resp.StatusCode)
	}

	log.Printf("Successfully sent push notification to %s", deviceToken)
	return nil
}

// handleEvent processes events received from the relay
func (s *Server) handleEvent(ctx context.Context, event *nostr.Event) error {
	log.Printf("Received event: [Kind: %d, ID: %s, PubKey: %s]", event.Kind, event.ID[:8], event.PubKey[:8])

	// Process group-related events
	if event.Kind == nostr.KindSimpleGroupCreateGroup {
		log.Printf("Processing group creation event: %s", event.ID[:8])
		// Extract group ID from tags
		for _, tag := range event.Tags {
			if len(tag) >= 2 && tag[0] == "h" {
				groupID := tag[1]
				log.Printf("Group ID: %s", groupID)
				// Refresh groups in processor
				if err := s.processor.RefreshGroupsFromState(ctx); err != nil {
					log.Printf("Failed to refresh groups: %v", err)
				}
				break
			}
		}
	} else if event.Kind == nostr.KindSimpleGroupEditMetadata {
		log.Printf("Processing group update event: %s", event.ID[:8])
		// Extract group ID and about field from tags
		var groupID, aboutField string
		for _, tag := range event.Tags {
			if len(tag) >= 2 && tag[0] == "h" {
				groupID = tag[1]
			} else if len(tag) >= 2 && tag[0] == "about" {
				aboutField = tag[1]
			}
		}
		log.Printf("Group ID: %s, About: %s", groupID, aboutField)

		// Update group in processor
		if groupID != "" {
			if err := s.processor.RefreshGroupsFromState(ctx); err != nil {
				log.Printf("Failed to refresh groups: %v", err)
			}
		}
	}

	return nil
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown() {
	close(s.shutdownChan)

	// The khatru relay manages client connections, so we don't need to
	// explicitly close them here unless the relay itself provides a mechanism
	// for shutting down client connections.
	// For now, we just log the shutdown.
	log.Printf("Memory queue shutdown")
}
