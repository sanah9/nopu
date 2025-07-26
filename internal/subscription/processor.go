package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/fiatjaf/khatru"
	"github.com/fiatjaf/relay29"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip29"
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
}

// PushNotificationSender interface for sending push notifications
type PushNotificationSender interface {
	SendPushNotification(ctx context.Context, deviceToken, title, body string, customData map[string]interface{}) error
}

// NewProcessor creates a new processor
func NewProcessor(queue *MemoryQueue, relay *khatru.Relay, state *relay29.State, relayPrivateKey string, subscriptionServer PushNotificationSender) *Processor {
	return &Processor{
		queue:               queue,
		relay:               relay,
		state:               state,
		subscriptionMatcher: NewSubscriptionMatcher(),
		relayPrivateKey:     relayPrivateKey,
		subscriptionServer:  subscriptionServer,
	}
}

// Start starts the processor
func (p *Processor) Start(ctx context.Context) error {
	log.Println("Starting event processor...")

	// Load all group information on initialization
	if err := p.loadGroups(ctx); err != nil {
		log.Printf("Failed to load groups: %v", err)
	}

	// Create consumer
	consumer, err := p.queue.CreateConsumer(ctx, "nopu-processor-1", 1000)
	if err != nil {
		return fmt.Errorf("failed to create consumer: %v", err)
	}
	p.consumer = consumer

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

// loadGroups loads all group information from relay29.State to subscription matcher
func (p *Processor) loadGroups(_ context.Context) error {
	// Get all groups from relay29.State
	groupCount := 0
	p.state.Groups.Range(func(groupID string, group *relay29.Group) bool {
		nip29Group := p.convertRelayGroupToNip29Group(group)
		if nip29Group != nil {
			p.subscriptionMatcher.AddGroup(nip29Group)
			groupCount++
		}
		return true // continue iteration
	})

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
	for member := range group.Members {
		return IsOnline(member)
	}
	return false
}

// pushNotification sends an APNs notification to offline members
func (p *Processor) pushNotification(ctx context.Context, group *nip29.Group, originalEvent *nostr.Event, wrappedEvent *nostr.Event) {
	if p.subscriptionServer == nil {
		log.Printf("Subscription server is nil, skipping push notification")
		return
	}

	// Obtain device token from cached parsed subscription
	var deviceToken string
	if parsed, ok := p.subscriptionMatcher.parsedSubscriptions[group.Address.ID]; ok {
		deviceToken = parsed.DeviceToken
	}

	if deviceToken == "" {
		log.Printf("No device token found for group %s", group.Address.ID)
		return
	}

	// Check if this is a simulator device token (simulator tokens are invalid for APNS)
	if len(deviceToken) > 64 {
		log.Printf("Skipping push notification for simulator device token (length: %d) in group %s", len(deviceToken), group.Address.ID)
		return
	}

	// Additional validation for device token format
	if len(deviceToken) != 64 {
		log.Printf("Warning: Device token length is %d, expected 64 for APNS. Token: %s...", len(deviceToken), deviceToken[:20])
	}

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
	if title == "" {
		title = "Nopu"
	}

	body := p.alertBodyForKind(originalEvent.Kind, originalEvent)

	// Send push notification via subscription server
	err = p.subscriptionServer.SendPushNotification(ctx, deviceToken, title, body, custom)
	if err != nil {
		log.Printf("Failed to send push notification to %s: %v", deviceToken[:20]+"...", err)
		return
	}

	log.Printf("Successfully sent push notification to %s for group %s", deviceToken[:20]+"...", group.Address.ID)
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
func (p *Processor) AddGroup(group *nip29.Group) {
	p.subscriptionMatcher.AddGroup(group)
}

// RemoveGroup removes a group from the subscription matcher
func (p *Processor) RemoveGroup(groupID string) {
	p.subscriptionMatcher.RemoveGroup(groupID)
}

// UpdateGroup updates a group in the subscription matcher
func (p *Processor) UpdateGroup(group *nip29.Group) {
	p.subscriptionMatcher.UpdateGroup(group)
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
