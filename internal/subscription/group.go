package subscription

import (
	"log"

	"github.com/nbd-wtf/go-nostr"
)

// HandleGroupCreationEvent handles group creation events (kind 20284)
func HandleGroupCreationEvent(event *nostr.Event, subscriptionMatcher *SubscriptionMatcher) {
	// Extract group ID from event
	groupID := extractGroupIDFromEvent(event)
	if groupID == "" {
		return
	}

	subscriptionMatcher.AddGroup(groupID)
}

// HandleGroupUpdateEvent handles group update events (kind 20285)
func HandleGroupUpdateEvent(event *nostr.Event, subscriptionMatcher *SubscriptionMatcher) {
	// Extract group ID from event
	groupID := extractGroupIDFromEvent(event)
	if groupID == "" {
		return
	}

	// Parse about field for subscription rules
	aboutField := ""
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "about" {
			aboutField = tag[1]
			break
		}
	}

	if aboutField == "" {
		return
	}

	// Update group subscription info
	if err := subscriptionMatcher.ValidateGroupSubscription(aboutField); err != nil {
		return
	}

	subscriptionMatcher.UpdateGroup(groupID)
}

// HandleGroupDeletionEvent handles group deletion events (kind 20286)
func HandleGroupDeletionEvent(event *nostr.Event, subscriptionMatcher *SubscriptionMatcher) {
	// Extract group ID from event
	groupID := extractGroupIDFromEvent(event)
	if groupID == "" {
		log.Printf("Failed to extract group ID from deletion event")
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
