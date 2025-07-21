package policies

import (
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip29"

	"nopu/internal/processor"
)

// HandleGroupCreationEvent handles group creation events (kind 20284)
func HandleGroupCreationEvent(event *nostr.Event, subscriptionMatcher *processor.SubscriptionMatcher) {
	// Extract group ID from event
	groupID := extractGroupIDFromEvent(event)
	if groupID == "" {
		return
	}

	// Create new group and add to subscription matcher
	nip29Group := &nip29.Group{
		Address: nip29.GroupAddress{
			ID: groupID,
		},
		Name: event.Content,
	}

	subscriptionMatcher.AddGroup(nip29Group)
}

// HandleGroupUpdateEvent handles group update events (kind 20285)
func HandleGroupUpdateEvent(event *nostr.Event, subscriptionMatcher *processor.SubscriptionMatcher) {
	// Extract group ID from event
	groupID := extractGroupIDFromEvent(event)
	if groupID == "" {
		return
	}

	// Parse about field for subscription rules
	aboutField := event.Content
	if aboutField == "" {
		return
	}

	// Update group subscription info
	if err := subscriptionMatcher.ValidateGroupSubscription(aboutField); err != nil {
		return
	}

	// Update group in subscription matcher
	nip29Group := &nip29.Group{
		Address: nip29.GroupAddress{
			ID: groupID,
		},
	}

	subscriptionMatcher.UpdateGroup(nip29Group)
}

// HandleGroupDeletionEvent handles group deletion events (kind 20286)
func HandleGroupDeletionEvent(event *nostr.Event, subscriptionMatcher *processor.SubscriptionMatcher) {
	// Extract group ID from event
	groupID := extractGroupIDFromEvent(event)
	if groupID == "" {
		return
	}

	// Remove group from subscription matcher
	subscriptionMatcher.RemoveGroup(groupID)
}

// extractGroupIDFromEvent extracts group ID from event tags
func extractGroupIDFromEvent(event *nostr.Event) string {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "h" {
			return tag[1]
		}
	}
	return ""
}
