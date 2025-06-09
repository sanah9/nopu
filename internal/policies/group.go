package policies

import (
	"context"
	"fmt"
	"log"

	"github.com/fiatjaf/relay29"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip29"

	"nopu/internal/processor"
)

// HandleGroupCreation handles group creation events (kind 9007)
func HandleGroupCreation(ctx context.Context, event *nostr.Event, state *relay29.State, eventProcessor *processor.Processor) {
	log.Printf("Handling group creation event: [Kind: %d, ID: %s]", event.Kind, event.ID[:8])

	// Get group ID from the newly created event
	groupID := ExtractGroupIDFromEvent(event)
	if groupID == "" {
		log.Printf("Failed to extract group ID from creation event: %s", event.ID[:8])
		return
	}

	// Get group information from state
	if group := GetGroupFromState(state, groupID); group != nil {
		nip29Group := ConvertRelayGroupToNip29Group(group)
		if nip29Group != nil {
			eventProcessor.AddGroup(nip29Group)
			log.Printf("Successfully added new group to subscription matcher: %s", groupID)
		}
	} else {
		log.Printf("Newly created group not found in relay29.State: %s", groupID)
	}
}

// HandleGroupUpdate handles group information editing events (kind 9002)
func HandleGroupUpdate(ctx context.Context, event *nostr.Event, state *relay29.State, eventProcessor *processor.Processor) {
	log.Printf("Handling group update event: [Kind: %d, ID: %s]", event.Kind, event.ID[:8])

	groupID := ExtractGroupIDFromEvent(event)
	if groupID == "" {
		log.Printf("Failed to extract group ID from update event: %s", event.ID[:8])
		return
	}

	// Get updated group information from state
	if group := GetGroupFromState(state, groupID); group != nil {
		nip29Group := ConvertRelayGroupToNip29Group(group)
		if nip29Group != nil {
			eventProcessor.UpdateGroup(nip29Group)
			log.Printf("Successfully updated group subscription information: %s", groupID)
		}
	} else {
		log.Printf("Group to update not found in relay29.State: %s", groupID)
	}
}

// HandleGroupDeletion handles group deletion events (kind 9008)
func HandleGroupDeletion(ctx context.Context, event *nostr.Event, state *relay29.State, eventProcessor *processor.Processor) {
	log.Printf("Handling group deletion event: [Kind: %d, ID: %s]", event.Kind, event.ID[:8])

	groupID := ExtractGroupIDFromEvent(event)
	if groupID == "" {
		log.Printf("Failed to extract group ID from deletion event: %s", event.ID[:8])
		return
	}

	// Remove group from subscription matcher
	eventProcessor.RemoveGroup(groupID)
	log.Printf("Successfully removed group from subscription matcher: %s", groupID)
}

// ExtractGroupIDFromEvent extracts group ID from event
func ExtractGroupIDFromEvent(event *nostr.Event) string {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "h" {
			return tag[1]
		}
	}
	return ""
}

// GetGroupFromState gets group from relay29.State
func GetGroupFromState(state *relay29.State, groupID string) *relay29.Group {
	var result *relay29.Group
	state.Groups.Range(func(id string, group *relay29.Group) bool {
		if id == groupID {
			result = group
			return false // stop iteration
		}
		return true // continue iteration
	})
	return result
}

// ConvertRelayGroupToNip29Group converts relay29.Group to nip29.Group
func ConvertRelayGroupToNip29Group(relayGroup *relay29.Group) *nip29.Group {
	if relayGroup == nil {
		return nil
	}
	return &relayGroup.Group
}

// ValidateGroupSubscriptionFormat validates group subscription format in about field
func ValidateGroupSubscriptionFormat(ctx context.Context, event *nostr.Event) (reject bool, msg string) {
	// Only validate group creation and update events that contain subscription information
	if event.Kind != 9007 && event.Kind != 9002 {
		return false, "" // Not a group creation or update event, allow it
	}

	// Extract about field from event content or tags
	about := ExtractAboutFieldFromEvent(event)
	if about == "" {
		return false, "" // No about field, allow it (will be handled by NIP-29 validation)
	}

	// Validate REQ format using SubscriptionMatcher's ValidateREQFormat method
	matcher := processor.NewSubscriptionMatcher()
	if err := matcher.ValidateREQFormat(about); err != nil {
		return true, fmt.Sprintf("invalid group subscription format in about field: %v", err)
	}

	return false, "" // Valid format, allow the event
}

// ExtractAboutFieldFromEvent extracts about field from group event tags
func ExtractAboutFieldFromEvent(event *nostr.Event) string {
	// Find about field in tags
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "about" {
			return tag[1]
		}
	}
	return ""
}
