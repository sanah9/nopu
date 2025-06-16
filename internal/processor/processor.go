package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"nopu/internal/listener"
	"nopu/internal/presence"
	"nopu/internal/push"

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
	apns                *push.APNSClient
}

// New creates a new processor
func New(rdb *redis.Client, relay *khatru.Relay, state *relay29.State, relayPrivateKey string, apns *push.APNSClient) *Processor {
	return &Processor{
		redis:               rdb,
		relay:               relay,
		state:               state,
		subscriptionMatcher: NewSubscriptionMatcher(),
		relayPrivateKey:     relayPrivateKey,
		apns:                apns,
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

	// Decide whether to broadcast or push based on online presence
	online := p.isFirstMemberOnline(group)

	if online {
		if p.state.Relay != nil {
			p.state.Relay.BroadcastEvent(kind20284Event)
			log.Printf("[online] Forwarded event %s to group %s as kind 20284 event %s",
				event.ID[:8], group.Address.ID, kind20284Event.ID[:8])
		} else {
			log.Printf("Warning: No relay instance available for broadcasting")
		}
	} else {
		log.Printf("[offline] Group %s seems offline, sending APNs push", group.Address.ID)
		p.pushNotification(ctx, group, event, kind20284Event)
	}

	return nil
}

// isFirstMemberOnline checks if the first member of the group is currently online using presence tracking
func (p *Processor) isFirstMemberOnline(group *nip29.Group) bool {
	for member := range group.Members {
		return presence.IsOnline(member)
	}
	return false
}

// pushNotification sends an APNs notification to offline members
func (p *Processor) pushNotification(ctx context.Context, group *nip29.Group, originalEvent *nostr.Event, wrappedEvent *nostr.Event) {
	if p.apns == nil {
		log.Printf("APNS client not configured, skip push")
		return
	}

	// Obtain subscription ID (used here as device token) from cached parsed subscription
	var deviceToken string
	if parsed, ok := p.subscriptionMatcher.parsedSubscriptions[group.Address.ID]; ok {
		deviceToken = parsed.SubscriptionID
	}

	if deviceToken == "" {
		log.Printf("No subscription ID (device token) found for group %s, skip push", group.Address.ID)
		return
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

	// Send push with alert and badge=1
	if resp, err := p.apns.Push(ctx, deviceToken, title, body, custom); err != nil {
		log.Printf("APNS push error to %s: %v", deviceToken, err)
	} else if resp != nil && resp.StatusCode != 200 {
		log.Printf("APNS push non-200 (%d %s) to %s", resp.StatusCode, resp.Reason, deviceToken)
	}
}

// alertBodyForKind returns a human-readable message for different event kinds.
func (p *Processor) alertBodyForKind(evtKind int, evt *nostr.Event) string {
	tagVal := func(name string) string {
		for _, t := range evt.Tags {
			if len(t) >= 2 && t[0] == name {
				return t[1]
			}
		}
		return ""
	}

	short := func(pk string) string {
		if len(pk) >= 8 {
			return pk[:8]
		}
		return pk
	}

	content := evt.Content

	switch evtKind {
	case 1: // note / reply / quote
		if tagVal("p") != "" { // reply or quote to you
			if tagVal("q") != "" {
				return "Quote reposted your message: " + content
			}
			return "Replied to your message: " + content
		}
		return "New message: " + content

	case 7: // reaction
		if pk := evt.PubKey; pk != "" {
			return short(pk) + " liked: " + content
		}
		return "Received a like"

	case 1059: // dm
		if ptag := tagVal("p"); ptag != "" {
			return short(ptag) + "received a message"
		}
		return "Received a direct message"

	case 6: // repost
		if pk := evt.PubKey; pk != "" {
			return short(pk) + " reposted your message"
		}
		return "Message was reposted"

	case 9735: // zap
		bolt := tagVal("bolt11")
		sats := parseBolt11Amount(bolt)
		return "Received " + fmt.Sprintf("%d", sats) + " sats via Zap"

	default:
		return "Received a new notification"
	}
}

// parseBolt11Amount extracts sat amount from bolt11 invoice; minimal implementation.
func parseBolt11Amount(bolt string) int64 {
	// invoices like lnbc10u1... 10u = 10 * 100 = 1000 sat (u = micro-BTC = 100 sat) ; lnbc20m... 20m = 20*100000 =2,000,000 sat
	if len(bolt) < 4 {
		return 0
	}
	// strip prefix "lnbc" or "lntbs"
	var numPart string
	for i := 4; i < len(bolt); i++ {
		c := bolt[i]
		if c >= '0' && c <= '9' {
			numPart += string(c)
		} else {
			// reached unit
			unit := bolt[i]
			if n, err := strconv.ParseInt(numPart, 10, 64); err == nil {
				switch unit {
				case 'p':
					return n / 10 // pico-BTC 1p=0.1 sat
				case 'n':
					return n / 100 // nano 1n=1 sat/100
				case 'u':
					return n * 1
				case 'm':
					return n * 1000
				default:
					return n * 100000000 // assume BTC
				}
			}
			break
		}
	}
	return 0
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
