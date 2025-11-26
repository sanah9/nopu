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
)

// requireHTagForExistingGroupExcept20285 is a custom version that excludes 20285 events from h tag requirement
func requireHTagForExistingGroupExcept20285(state *relay29.State) func(ctx context.Context, event *nostr.Event) (reject bool, msg string) {
	return func(ctx context.Context, event *nostr.Event) (reject bool, msg string) {
		// Skip h tag requirement for 20285 events
		if event.Kind == 20285 {
			return false, ""
		}
		// this allows us to never check again in any of the other rules if the tag exists and just assume it exists always
		gtag := event.Tags.GetFirst([]string{"h", ""})
		if gtag == nil {
			return true, "missing group (`h`) tag"
		}

		// skip this check when creating a group
		if event.Kind == nostr.KindSimpleGroupCreateGroup {
			return false, ""
		}

		// otherwise require a group to exist always
		if group := state.GetGroupFromEvent(event); group == nil {
			return true, "group '" + (*gtag)[1] + "' doesn't exist"
		}

		return false, ""
	}
}

// prevent20284FromExternalSources creates a policy function that rejects 20284 events
// from external sources (20284 events are for internal forwarding only)
func prevent20284FromExternalSources(internalRelayPubkey string) func(ctx context.Context, event *nostr.Event) (reject bool, msg string) {
	return func(ctx context.Context, event *nostr.Event) (reject bool, msg string) {
		// Only check 20284 events
		if event.Kind != 20284 {
			return false, ""
		}

		// 20284 events are for internal forwarding only
		// Only allow if it's from the relay itself (internal event)
		if event.PubKey == internalRelayPubkey {
			return false, "" // Allow internal events
		}

		// Reject all external 20284 events
		return true, "20284 events are for internal use only (external events not allowed)"
	}
}

// prevent20285FromNonWhitelistedPubkeys creates a policy function that rejects 20285 events
// based on the configured policy (20285 events are for external use)
func prevent20285FromNonWhitelistedPubkeys(policy config.Event20285Policy) func(ctx context.Context, event *nostr.Event) (reject bool, msg string) {
	return func(ctx context.Context, event *nostr.Event) (reject bool, msg string) {
		// Only check 20285 events
		if event.Kind != 20285 {
			return false, ""
		}

		// 20285 events don't need h tag (they are external events that will be matched against groups)
		// This allows 20285 events to pass through RequireHTagForExistingGroup policy

		// If reject_all is true, reject all 20285 events
		if policy.RejectAll {
			return true, "20285 events are not allowed (reject_all is enabled)"
		}

		// If whitelist is empty, no restriction (allow all 20285 events)
		if len(policy.Whitelist) == 0 {
			return false, ""
		}

		// Check if the event pubkey is in the whitelist
		for _, allowedPubkey := range policy.Whitelist {
			if event.PubKey == allowedPubkey {
				return false, "" // Allow the event
			}
		}

		// Reject the event if pubkey is not in whitelist
		return true, fmt.Sprintf("20285 event from pubkey %s is not allowed (not in whitelist)", event.PubKey)
	}
}

// Server represents the subscription server
type Server struct {
	cfg          *config.Config
	db           *lmdb.LMDBBackend
	queue        *MemoryQueue
	relay        *khatru.Relay
	state        *relay29.State
	listener     *Listener
	processor    *Processor
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
	queue := NewMemoryQueue(cfg.MemoryQueue.MaxSize, cfg.MemoryQueue.DedupeTTL)

	// Initialize NIP-29 relay
	relay, state := khatru29.Init(relay29.Options{
		Domain:                  cfg.SubscriptionServer.Domain,
		DB:                      db,
		SecretKey:               cfg.SubscriptionServer.RelayPrivateKey,
		DefaultRoles:            []*nip29.Role{{Name: "king", Description: "the group's max top admin"}},
		GroupCreatorDefaultRole: &nip29.Role{Name: "king", Description: "the group's max top admin"},
	})

	// Clear the original RejectEvent policies and set our custom ones
	// This allows us to control the order and exclude 20285 events from h tag requirement
	relay.RejectEvent = []func(context.Context, *nostr.Event) (bool, string){
		state.RequireModerationEventsToBeRecent,
		// Skip group rules check for 20285 events (external events)
		func(ctx context.Context, event *nostr.Event) (bool, string) {
			if event.Kind == 20285 {
				return false, "" // Allow 20285 events to skip group rules check
			}
			return state.RestrictWritesBasedOnGroupRules(ctx, event)
		},
		state.RestrictInvalidModerationActions,
		state.PreventWritingOfEventsJustDeleted,
		state.CheckPreviousTag,
		requireHTagForExistingGroupExcept20285(state), // Custom h tag policy that excludes 20285 events
	}

	// Get internal relay pubkey for 20284 policy
	var internalRelayPubkey string
	if cfg.SubscriptionServer.RelayPrivateKey != "" {
		internalRelayPubkey, _ = nostr.GetPublicKey(cfg.SubscriptionServer.RelayPrivateKey)
	}

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
	SetupPresenceHooks(relay)

	// Configure relay information
	relay.Info.Name = cfg.SubscriptionServer.RelayName
	relay.Info.Description = cfg.SubscriptionServer.RelayDescription

	// Add additional event restriction policies
	relay.RejectEvent = append(relay.RejectEvent,
		policies.PreventLargeTags(64),
		policies.PreventTooManyIndexableTags(6, []int{9005}, nil),
		policies.RestrictToSpecifiedKinds(
			true,
			9000, 9001, 9002, 9003, 9004, 9005, 9006, 9007, 9008, 9009, // Group management
			9021, 9022, 20284, 20285, // Group invitations and external events
		),
		policies.PreventTimestampsInThePast(60*time.Second),
		policies.PreventTimestampsInTheFuture(30*time.Second),
		prevent20284FromExternalSources(internalRelayPubkey),
		prevent20285FromNonWhitelistedPubkeys(cfg.SubscriptionServer.Event20285Policy),
	)

	// Initialize event listener (only if configured)
	var eventListener *Listener
	if cfg.SubscriptionServer.Listener != nil && len(cfg.SubscriptionServer.Listener.Relays) > 0 {
		eventListener = NewListener(*cfg.SubscriptionServer.Listener, queue)
		log.Printf("Event listener initialized with %d relays", len(cfg.SubscriptionServer.Listener.Relays))
	} else {
		log.Println("Event listener not configured or no relays specified - skipping listener initialization")
	}

	server := &Server{
		cfg:          cfg,
		db:           db,
		queue:        queue,
		relay:        relay,
		state:        state,
		listener:     eventListener,
		processor:    nil, // Will be set after server creation
		eventChan:    make(chan *nostr.Event, 1000),
		shutdownChan: make(chan struct{}),
		pushClient:   &http.Client{Timeout: 10 * time.Second},
		pushURL:      cfg.SubscriptionServer.PushServerURL + "/push",
	}

	// Initialize processor with subscription server
	processor := NewProcessor(queue, relay, state, cfg.SubscriptionServer.RelayPrivateKey, server, cfg.SubscriptionServer.PushRateLimit, cfg)
	server.processor = processor

	// Set up event processing
	relay.OnEventSaved = append(relay.OnEventSaved, func(ctx context.Context, event *nostr.Event) {
		if err := server.handleEvent(ctx, event); err != nil {
			log.Printf("Error handling event: %v", err)
		}
	})

	// Set up ephemeral event processing for 20285 events
	relay.OnEphemeralEvent = append(relay.OnEphemeralEvent, func(ctx context.Context, event *nostr.Event) {
		// Process 20285 events (external events)
		if event.Kind == 20285 {
			server.handle20285Event(event)
		}
	})

	return server, nil
}

// Start starts the subscription server
func (s *Server) Start(ctx context.Context) error {
	// Start event listener (only if configured)
	if s.listener != nil {
		go func() {
			if err := s.listener.Start(ctx); err != nil {
				if ctx.Err() == nil {
					log.Printf("Event listener error: %v", err)
				}
			}
		}()
	} else {
		log.Println("Event listener not configured - skipping listener startup")
	}

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

	return http.ListenAndServe(addr, nil)
}

// handleRelay handles standard Nostr relay requests
func (s *Server) handleRelay(w http.ResponseWriter, r *http.Request) {
	// Delegate to the khatru relay for standard Nostr protocol handling
	s.relay.ServeHTTP(w, r)
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

// SendPushNotificationWithSilent sends a push notification with silent option via the push server
func (s *Server) SendPushNotificationWithSilent(ctx context.Context, deviceToken, title, body string, customData map[string]interface{}, silent bool) error {
	payload := map[string]interface{}{
		"device_token": deviceToken,
		"title":        title,
		"body":         body,
		"custom_data":  customData,
		"silent":       silent,
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

	log.Printf("Successfully sent %s push notification to %s", map[bool]string{true: "silent", false: "regular"}[silent], deviceToken)
	return nil
}

// handleEvent handles events from the relay
func (s *Server) handleEvent(_ context.Context, event *nostr.Event) error {
	// Process NIP-29 group creation events (kind 9007)
	if event.Kind == 9007 {
		HandleGroupCreationEvent(event, s.processor.GetSubscriptionMatcher())
	}

	// Process NIP-29 group update events (kind 9002)
	if event.Kind == 9002 {
		HandleGroupUpdateEvent(event, s.processor.GetSubscriptionMatcher())
	}

	// Process NIP-29 group deletion events (kind 9008)
	if event.Kind == 9008 {
		HandleGroupDeletionEvent(event, s.processor.GetSubscriptionMatcher())
	}

	return nil
}

// handle20285Event handles external 20285 events by parsing content and matching against groups
func (s *Server) handle20285Event(event *nostr.Event) {
	var matchingGroups []*nip29.Group

	// Determine which event should be forwarded (original or parsed content)
	var eventToForward *nostr.Event

	// If content is empty, match against 20285 event itself
	if event.Content == "" {
		matchingGroups = s.processor.GetSubscriptionMatcher().GetMatchingGroups(event)
		eventToForward = event
	} else {
		// Parse the original event from content
		var originalEvent nostr.Event
		if err := json.Unmarshal([]byte(event.Content), &originalEvent); err != nil {
			log.Printf("Failed to parse original event from 20285 content: %v", err)
			return
		}

		// Get all matching groups for the original event
		matchingGroups = s.processor.GetSubscriptionMatcher().GetMatchingGroups(&originalEvent)
		eventToForward = &originalEvent
	}

	// Forward the event (as 20284) to each matching group
	for _, group := range matchingGroups {
		if err := s.processor.ForwardToGroup(context.Background(), eventToForward, group); err != nil {
			log.Printf("Failed to forward event %s to group %s: %v",
				event.ID[:8], group.Address.ID, err)
		}
	}
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown() {
	close(s.shutdownChan)
	log.Printf("Subscription server shutdown")
}
