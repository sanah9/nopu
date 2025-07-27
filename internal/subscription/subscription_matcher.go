package subscription

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/fiatjaf/relay29"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip29"
)

// ParsedGroupSubscription stores parsed group subscription information
type ParsedGroupSubscription struct {
	GroupID     string
	DeviceToken string
	Filters     nostr.Filters
	ParseError  error
}

// SubscriptionMatcher handles event matching with group subscriptions
type SubscriptionMatcher struct {
	groupIDs map[string]bool // stores only group IDs, key is groupID
	// Cache parsed REQ to avoid repeated parsing
	parsedSubscriptions map[string]*ParsedGroupSubscription // key is groupID
	mu                  sync.RWMutex                        // Protect cache concurrent access
	state               *relay29.State                      // Reference to relay29 state for getting group info
}

// NewSubscriptionMatcher creates a new subscription matcher
func NewSubscriptionMatcher(state *relay29.State) *SubscriptionMatcher {
	return &SubscriptionMatcher{
		groupIDs:            make(map[string]bool),
		parsedSubscriptions: make(map[string]*ParsedGroupSubscription),
		state:               state,
	}
}

// AddGroup adds a group to the matcher
func (sm *SubscriptionMatcher) AddGroup(groupID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.groupIDs[groupID] = true
	// Pre-parse REQ and cache it
	sm.parseAndCacheSubscription(groupID)
}

// RemoveGroup removes a group from the matcher
func (sm *SubscriptionMatcher) RemoveGroup(groupID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	delete(sm.groupIDs, groupID)
	// Clear cache
	delete(sm.parsedSubscriptions, groupID)
}

// UpdateGroup updates group information
func (sm *SubscriptionMatcher) UpdateGroup(groupID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.groupIDs[groupID] = true
	// Re-parse and update cache
	sm.parseAndCacheSubscription(groupID)
}

// parseAndCacheSubscription parses group's REQ and caches the result (internal method, requires lock when called)
func (sm *SubscriptionMatcher) parseAndCacheSubscription(groupID string) {
	parsed := &ParsedGroupSubscription{
		GroupID: groupID,
	}

	// Get group from state
	group, exists := sm.state.Groups.Load(groupID)
	if !exists {
		parsed.ParseError = fmt.Errorf("group not found in state")
		log.Printf("Group %s not found in state", groupID)
		sm.parsedSubscriptions[groupID] = parsed
		return
	}

	filters, subID, err := sm.parseREQFromAbout(group.About)
	if err != nil {
		parsed.ParseError = err
		log.Printf("Failed to parse REQ for group %s: %v", groupID, err)
	} else {
		parsed.Filters = filters
		parsed.DeviceToken = subID
	}
	log.Printf("Parsed REQ for group %s: %v", groupID, parsed)
	sm.parsedSubscriptions[groupID] = parsed
}

// GetMatchingGroups gets all groups that match the event
func (sm *SubscriptionMatcher) GetMatchingGroups(event *nostr.Event) []*nip29.Group {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var matchingGroups []*nip29.Group

	sm.state.Groups.Range(func(groupID string, group *relay29.Group) bool {
		if sm.doesEventMatchGroupCached(event, groupID) {
			matchingGroups = append(matchingGroups, &group.Group)
		}
		return true // continue iteration
	})

	return matchingGroups
}

// doesEventMatchGroupCached checks if event matches specific group using cached parsed subscription
func (sm *SubscriptionMatcher) doesEventMatchGroupCached(event *nostr.Event, groupID string) bool {
	// Get parsed subscription information from cache
	parsed, exists := sm.parsedSubscriptions[groupID]
	if !exists {
		log.Printf("No cached subscription found for group %s", groupID)
		return false
	}

	// If parsing failed, return false
	if parsed.ParseError != nil {
		return false
	}

	// If no valid filters, return false
	if len(parsed.Filters) == 0 {
		return false
	}

	// Use go-nostr's Match method to check if event matches filters
	return parsed.Filters.Match(event)
}

// parseREQFromAbout parses REQ request from About field and extracts subscription ID and filters
func (sm *SubscriptionMatcher) parseREQFromAbout(about string) (nostr.Filters, string, error) {
	// If About field is empty, return empty filters
	if about == "" {
		return nil, "", fmt.Errorf("empty about field")
	}

	// Try to parse JSON format REQ request
	var reqArray []interface{}
	if err := json.Unmarshal([]byte(about), &reqArray); err != nil {
		return nil, "", fmt.Errorf("failed to parse JSON from about field: %w", err)
	}

	// Check if it's valid REQ format: ["REQ", <subscription_id>, <filters1>, <filters2>, ...]
	if len(reqArray) < 3 {
		return nil, "", fmt.Errorf("invalid REQ format: need at least 3 elements")
	}

	// Check if first element is "REQ"
	if reqType, ok := reqArray[0].(string); !ok || reqType != "REQ" {
		return nil, "", fmt.Errorf("invalid REQ format: first element must be 'REQ'")
	}

	// Second element is subscription_id
	subID := ""
	if sid, ok := reqArray[1].(string); ok {
		subID = sid
	}

	// From third element onwards are filters
	var filters nostr.Filters
	for i := 2; i < len(reqArray); i++ {
		filterData, err := json.Marshal(reqArray[i])
		if err != nil {
			log.Printf("Failed to marshal filter %d: %v", i-2, err)
			continue
		}

		var filter nostr.Filter
		if err := json.Unmarshal(filterData, &filter); err != nil {
			log.Printf("Failed to unmarshal filter %d: %v", i-2, err)
			continue
		}

		filters = append(filters, filter)
	}

	return filters, subID, nil
}

// ValidateREQFormat validates if REQ format in About field is correct
func (sm *SubscriptionMatcher) ValidateREQFormat(about string) error {
	_, _, err := sm.parseREQFromAbout(about)
	return err
}

// ExtractSubscriptionID extracts subscription ID from About field
func (sm *SubscriptionMatcher) ExtractSubscriptionID(about string) (string, error) {
	var reqArray []interface{}
	if err := json.Unmarshal([]byte(about), &reqArray); err != nil {
		return "", fmt.Errorf("failed to parse JSON: %w", err)
	}

	if len(reqArray) < 2 {
		return "", fmt.Errorf("invalid REQ format")
	}

	if subID, ok := reqArray[1].(string); ok {
		return subID, nil
	}

	return "", fmt.Errorf("subscription ID is not a string")
}

// GetGroupStats gets group statistics (for debugging)
func (sm *SubscriptionMatcher) GetGroupStats() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	stats := make(map[string]interface{})
	stats["total_groups"] = len(sm.groupIDs)

	groupList := make([]string, 0, len(sm.groupIDs))
	for groupID := range sm.groupIDs {
		groupList = append(groupList, groupID)
	}
	stats["group_ids"] = groupList

	// Add cache statistics
	validCached := 0
	errorCached := 0
	for _, parsed := range sm.parsedSubscriptions {
		if parsed.ParseError == nil {
			validCached++
		} else {
			errorCached++
		}
	}
	stats["cached_subscriptions"] = map[string]interface{}{
		"total":  len(sm.parsedSubscriptions),
		"valid":  validCached,
		"errors": errorCached,
	}

	return stats
}

// MatchStats match statistics information
type MatchStats struct {
	EventID       string   `json:"event_id"`
	MatchedGroups []string `json:"matched_groups"`
	TotalGroups   int      `json:"total_groups"`
}

// GetMatchStats gets event match statistics
func (sm *SubscriptionMatcher) GetMatchStats(event *nostr.Event) *MatchStats {
	matchingGroups := sm.GetMatchingGroups(event)

	groupIDs := make([]string, len(matchingGroups))
	for i, group := range matchingGroups {
		groupIDs[i] = group.Address.ID
	}

	return &MatchStats{
		EventID:       event.ID,
		MatchedGroups: groupIDs,
		TotalGroups:   len(sm.groupIDs),
	}
}

// LogMatchResult logs match result (for debugging)
func (sm *SubscriptionMatcher) LogMatchResult(event *nostr.Event) {
	stats := sm.GetMatchStats(event)

	if len(stats.MatchedGroups) > 0 {
		log.Printf("Event %s matched %d groups: %s",
			event.ID[:8],
			len(stats.MatchedGroups),
			strings.Join(stats.MatchedGroups, ", "))
	} else {
		log.Printf("Event %s matched no groups (total groups: %d)",
			event.ID[:8],
			stats.TotalGroups)
	}
}

// RefreshAllCaches refreshes all group caches (for proactive updates)
func (sm *SubscriptionMatcher) RefreshAllCaches() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Clear existing cache
	sm.parsedSubscriptions = make(map[string]*ParsedGroupSubscription)

	// Re-parse all groups
	sm.state.Groups.Range(func(groupID string, group *relay29.Group) bool {
		sm.parseAndCacheSubscription(groupID)
		return true // continue iteration
	})

	log.Printf("Refreshed subscription caches for %d groups", len(sm.groupIDs))
}

// ClearGroups clears all groups from the matcher
func (sm *SubscriptionMatcher) ClearGroups() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.groupIDs = make(map[string]bool)
	sm.parsedSubscriptions = make(map[string]*ParsedGroupSubscription)
}

// ValidateGroupSubscription validates a group subscription
func (sm *SubscriptionMatcher) ValidateGroupSubscription(about string) error {
	return sm.ValidateREQFormat(about)
}

// GetGroupCount returns the number of groups in the matcher
func (sm *SubscriptionMatcher) GetGroupCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.groupIDs)
}

// GetGroup gets a group by ID
func (sm *SubscriptionMatcher) GetGroup(groupID string) *nip29.Group {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	group, exists := sm.state.Groups.Load(groupID)
	if !exists {
		return nil
	}
	return &group.Group
}
