package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/fiatjaf/khatru"
	"github.com/fiatjaf/relay29"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip29"

	"nopu/internal/config"
)

// Processor event processor
type Processor struct {
	queue               *MemoryQueue
	relay               *khatru.Relay
	state               *relay29.State
	subscriptionMatcher *SubscriptionMatcher
	relayPrivateKey     string
	consumer            *Consumer
	subscriptionServer  PushNotificationSender
	// Push rate limiting with sharded locks for better performance
	pushRateLimiters []*pushRateLimiter
	pushRateLimit    time.Duration  // Configurable push rate limit
	cfg              *config.Config // Configuration for silent push settings
}

// pushRateLimiter represents a shard for push rate limiting
type pushRateLimiter struct {
	lastPushTime map[string]time.Time
	mutex        sync.RWMutex
}

// PushNotificationSender interface for sending push notifications
type PushNotificationSender interface {
	SendPushNotification(ctx context.Context, deviceToken, title, body string, customData map[string]interface{}) error
	SendPushNotificationWithSilent(ctx context.Context, deviceToken, title, body string, customData map[string]interface{}, silent bool) error
}

// NewProcessor creates a new processor
func NewProcessor(queue *MemoryQueue, relay *khatru.Relay, state *relay29.State, relayPrivateKey string, subscriptionServer PushNotificationSender, pushRateLimit time.Duration, cfg *config.Config) *Processor {
	// Create 16 shards for push rate limiting to reduce lock contention
	shards := make([]*pushRateLimiter, 16)
	for i := 0; i < 16; i++ {
		shards[i] = &pushRateLimiter{
			lastPushTime: make(map[string]time.Time),
		}
	}

	return &Processor{
		queue:               queue,
		relay:               relay,
		state:               state,
		subscriptionMatcher: NewSubscriptionMatcher(state),
		relayPrivateKey:     relayPrivateKey,
		subscriptionServer:  subscriptionServer,
		pushRateLimiters:    shards,
		pushRateLimit:       pushRateLimit,
		cfg:                 cfg,
	}
}

// Start starts the processor
func (p *Processor) Start(ctx context.Context) error {
	log.Println("Starting event processor...")

	// Load all group information on initialization (async to avoid blocking startup)
	go func() {
		if err := p.loadGroups(ctx); err != nil {
			log.Printf("Failed to load groups: %v", err)
		} else {
			log.Println("Finished loading all groups")
		}
	}()

	// Create consumer
	consumer, err := p.queue.CreateConsumer(ctx, "nopu-processor-1", 1000)
	if err != nil {
		return fmt.Errorf("failed to create consumer: %v", err)
	}
	p.consumer = consumer

	// Start push rate limiting cleanup routine
	go p.cleanupPushRateLimit()

	// Start processing loop
	for {
		select {
		case <-ctx.Done():
			log.Println("Event processor stopped")
			return nil
		default:
			// Read events from queue
			messages, err := p.queue.ReadEvents(ctx, consumer.ID, 10, 1*time.Second)
			if err != nil {
				if err != context.DeadlineExceeded {
					log.Printf("Failed to read events from queue: %v", err)
				}
				continue
			}

			// Process events
			for _, message := range messages {
				p.processMessage(ctx, message)
			}
		}
	}
}

// cleanupPushRateLimit periodically cleans up old push rate limiting records
func (p *Processor) cleanupPushRateLimit() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		for _, shard := range p.pushRateLimiters {
			shard.mutex.Lock()
			now := time.Now()
			cleaned := 0
			for deviceToken, lastPush := range shard.lastPushTime {
				// Remove records older than 1 hour
				if now.Sub(lastPush) > time.Hour {
					delete(shard.lastPushTime, deviceToken)
					cleaned++
				}
			}
			shard.mutex.Unlock()

			if cleaned > 0 {
				log.Printf("Cleaned up %d old push rate limiting records from shard", cleaned)
			}
		}
	}
}

// loadGroups loads all group information from relay29.State to subscription matcher
func (p *Processor) loadGroups(_ context.Context) error {
	// Get all groups from relay29.State
	groupCount := 0
	startTime := time.Now()

	p.state.Groups.Range(func(groupID string, group *relay29.Group) bool {
		p.subscriptionMatcher.AddGroup(groupID)
		groupCount++
		return true // continue iteration
	})

	elapsed := time.Since(startTime)
	if groupCount > 0 {
		log.Printf("Loaded %d groups in %v", groupCount, elapsed)
	}

	return nil
}

// processMessage processes a single message
func (p *Processor) processMessage(ctx context.Context, message *EventMessage) {
	// Get event from message
	event := message.Event
	if event == nil {
		log.Printf("Invalid message format: missing event")
		return
	}

	// Use subscription matcher to find matching groups
	matchingGroups := p.subscriptionMatcher.GetMatchingGroups(event)

	// Log match result (for debugging)
	p.subscriptionMatcher.LogMatchResult(event)

	// Forward event to each matching group
	for _, group := range matchingGroups {
		if err := p.forwardToGroup(ctx, event, group); err != nil {
			log.Printf("Failed to forward event %s to group %s: %v",
				event.ID[:8], group.Address.ID, err)
		}
	}
}

// ForwardToGroupDirect forwards event directly to a specific group without 20284 wrapping
func (p *Processor) ForwardToGroupDirect(ctx context.Context, group *nip29.Group, originalEvent *nostr.Event, wrappedEvent *nostr.Event) error {
	// Decide whether to broadcast or push based on online presence
	online := p.isFirstMemberOnline(group)

	if online {
		// Use the khatru relay to broadcast the event directly
		p.relay.BroadcastEvent(wrappedEvent)
	} else {
		// For offline members, send push notification with the original event
		// Pass the same event as both original and wrapped for consistency
		p.pushNotification(ctx, group, originalEvent, wrappedEvent)
	}

	return nil
}

// ForwardToGroup forwards event to a specific group (public method)
func (p *Processor) ForwardToGroup(ctx context.Context, event *nostr.Event, group *nip29.Group) error {
	return p.forwardToGroup(ctx, event, group)
}

// forwardToGroup forwards event to a specific group
func (p *Processor) forwardToGroup(ctx context.Context, event *nostr.Event, group *nip29.Group) error {
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

	// Decide whether to broadcast or push based on online presence
	online := p.isFirstMemberOnline(group)

	if online {
		// Use the khatru relay to broadcast the event
		p.relay.BroadcastEvent(kind20284Event)
	} else {
		p.pushNotification(ctx, group, event, kind20284Event)
	}

	return nil
}

// isFirstMemberOnline checks if the first member of the group is currently online using presence tracking
func (p *Processor) isFirstMemberOnline(group *nip29.Group) bool {
	return IsOnline(group.Address.ID)
}

// getShardIndex returns the shard index for a device token
func (p *Processor) getShardIndex(deviceToken string) int {
	// Simple hash function: sum of first 4 bytes modulo number of shards
	hash := 0
	for i := 0; i < 4 && i < len(deviceToken); i++ {
		hash += int(deviceToken[i])
	}
	return hash % len(p.pushRateLimiters)
}

// pushNotification sends an APNs notification to offline members
func (p *Processor) pushNotification(ctx context.Context, group *nip29.Group, originalEvent *nostr.Event, wrappedEvent *nostr.Event) {
	if p.subscriptionServer == nil {
		log.Printf("Subscription server is nil, skipping push notification")
		return
	}

	// Obtain device token from cached parsed subscription (thread-safe access)
	var deviceToken string
	if parsed, ok := p.subscriptionMatcher.GetParsedSubscription(group.Address.ID); ok {
		deviceToken = parsed.DeviceToken
	}

	if deviceToken == "" {
		log.Printf("No device token found for group %s", group.Address.ID)
		return
	}

	// Check push rate limiting (configurable interval per device)
	shardIndex := p.getShardIndex(deviceToken)
	shard := p.pushRateLimiters[shardIndex]
	shard.mutex.RLock()
	if lastPush, exists := shard.lastPushTime[deviceToken]; exists {
		if time.Since(lastPush) < p.pushRateLimit {
			shard.mutex.RUnlock()
			log.Printf("Rate limiting: skipping push for device %s (last push was %v ago, limit is %v)",
				deviceToken, time.Since(lastPush), p.pushRateLimit)
			return
		}
	}
	shard.mutex.RUnlock()

	// Serialize wrapped event as JSON string
	dataBytes, err := json.Marshal(wrappedEvent)
	if err != nil {
		log.Printf("Failed to marshal wrapped event for push: %v", err)
		return
	}

	// APNs payload must be ≤ 4 KB in total. Leave room for the `aps` fields, so cap custom data around 3 KB.
	const maxCustomBytes = 3000

	custom := map[string]interface{}{
		"badge": 1,
	}

	if len(dataBytes) <= maxCustomBytes {
		custom["event"] = string(dataBytes)
	} else {
		custom["eventid"] = wrappedEvent.ID
	}

	title := group.Name

	body := p.alertBodyForKind(originalEvent.Kind, originalEvent)

	// Use silent push configuration from config
	silent := p.cfg.PushServer.Push.SilentPush

	//test
	title = "Notification"
	body = "You have a new message"
	custom = map[string]interface{}{
		"badge": 1,
	}
	silent = false

	// Send push notification via subscription server with silent option
	err = p.subscriptionServer.SendPushNotificationWithSilent(ctx, deviceToken, title, body, custom, silent)
	if err != nil {
		log.Printf("Failed to send push notification to %s: %v", deviceToken, err)
		return
	}

	// Record successful push time for rate limiting
	shardIndex = p.getShardIndex(deviceToken)
	shard = p.pushRateLimiters[shardIndex]
	shard.mutex.Lock()
	shard.lastPushTime[deviceToken] = time.Now()
	shard.mutex.Unlock()

	log.Printf("Successfully sent push notification to %s for group %s", deviceToken, group.Address.ID)
}

// alertBodyForKind generates alert body based on event kind
func (p *Processor) alertBodyForKind(evtKind int, evt *nostr.Event) string {
	switch evtKind {
	case 1:
		// Short text note
		if len(evt.Content) > 100 {
			return evt.Content[:100] + "..."
		}
		return evt.Content
	case 7:
		// Reaction
		return "Someone reacted to your post"
	case 9735:
		// Zap
		amount := parseBolt11Amount(evt.Content)
		if amount > 0 {
			return fmt.Sprintf("You received %d sats!", amount)
		}
		return "You received a zap!"
	case 1059:
		// Private message
		return "You have a new private message"
	default:
		return "You have a new notification"
	}
}

// parseBolt11Amount extracts amount from bolt11 invoice
func parseBolt11Amount(bolt string) int64 {
	// Simple parsing - look for amount in bolt11
	// This is a simplified version, in production you'd use a proper bolt11 parser
	if len(bolt) < 10 {
		return 0
	}

	// Look for amount pattern in bolt11
	// This is a very basic implementation
	for i := 0; i < len(bolt)-2; i++ {
		if bolt[i] == 'a' && bolt[i+1] == 'm' && bolt[i+2] == 'o' {
			// Found amount prefix, try to parse
			if i+3 < len(bolt) {
				// Extract amount value
				amountStr := ""
				for j := i + 3; j < len(bolt) && bolt[j] >= '0' && bolt[j] <= '9'; j++ {
					amountStr += string(bolt[j])
				}
				if amountStr != "" {
					if amount, err := json.Marshal(amountStr); err == nil {
						return int64(amount[0])
					}
				}
			}
		}
	}
	return 0
}

// AddGroup adds a group to the subscription matcher
func (p *Processor) AddGroup(groupID string) {
	p.subscriptionMatcher.AddGroup(groupID)
}

// RemoveGroup removes a group from the subscription matcher
func (p *Processor) RemoveGroup(groupID string) {
	p.subscriptionMatcher.RemoveGroup(groupID)
}

// UpdateGroup updates a group in the subscription matcher
func (p *Processor) UpdateGroup(groupID string) {
	p.subscriptionMatcher.UpdateGroup(groupID)
}

// RefreshGroupsFromState refreshes groups from relay29.State
func (p *Processor) RefreshGroupsFromState(ctx context.Context) error {
	// Clear existing groups
	p.subscriptionMatcher.ClearGroups()

	// Reload groups
	return p.loadGroups(ctx)
}

// ValidateGroupSubscription validates a group subscription
func (p *Processor) ValidateGroupSubscription(about string) error {
	return p.subscriptionMatcher.ValidateGroupSubscription(about)
}

// GetProcessorStats returns processor statistics
func (p *Processor) GetProcessorStats() map[string]interface{} {
	stats := p.queue.GetStats()
	stats["subscription_matcher_groups"] = p.subscriptionMatcher.GetGroupCount()
	return stats
}

// GetSubscriptionMatcher returns the subscription matcher
func (p *Processor) GetSubscriptionMatcher() *SubscriptionMatcher {
	return p.subscriptionMatcher
}
