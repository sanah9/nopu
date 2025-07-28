package subscription

import (
	"context"
	"testing"

	"nopu/internal/config"

	"github.com/nbd-wtf/go-nostr"
)

func TestPrevent20284FromNonWhitelistedPubkeys(t *testing.T) {
	ctx := context.Background()

	// Test event
	event := &nostr.Event{
		Kind:   20284,
		PubKey: "test_pubkey_123",
	}

	tests := []struct {
		name           string
		policy         config.Event20284Policy
		expectedReject bool
		expectedMsg    string
	}{
		{
			name: "no restriction - allow all",
			policy: config.Event20284Policy{
				Whitelist: []string{},
				RejectAll: false,
			},
			expectedReject: false,
			expectedMsg:    "",
		},
		{
			name: "reject all - reject all",
			policy: config.Event20284Policy{
				Whitelist: []string{},
				RejectAll: true,
			},
			expectedReject: true,
			expectedMsg:    "20284 events are not allowed (reject_all is enabled)",
		},
		{
			name: "whitelist with matching pubkey - allow",
			policy: config.Event20284Policy{
				Whitelist: []string{"test_pubkey_123", "other_pubkey"},
				RejectAll: false,
			},
			expectedReject: false,
			expectedMsg:    "",
		},
		{
			name: "whitelist without matching pubkey - reject",
			policy: config.Event20284Policy{
				Whitelist: []string{"other_pubkey_1", "other_pubkey_2"},
				RejectAll: false,
			},
			expectedReject: true,
			expectedMsg:    "20284 event from pubkey test_pubkey_123 is not allowed (not in whitelist)",
		},
		{
			name: "non-20284 event - no restriction",
			policy: config.Event20284Policy{
				Whitelist: []string{},
				RejectAll: true,
			},
			expectedReject: false,
			expectedMsg:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := prevent20284FromNonWhitelistedPubkeys(tt.policy)

			// Create test event
			testEvent := &nostr.Event{
				Kind:   event.Kind,
				PubKey: event.PubKey,
			}

			// For the last test case, use a different kind
			if tt.name == "non-20284 event - no restriction" {
				testEvent.Kind = 1
			}

			reject, msg := policy(ctx, testEvent)

			if reject != tt.expectedReject {
				t.Errorf("expected reject=%v, got %v", tt.expectedReject, reject)
			}

			if msg != tt.expectedMsg {
				t.Errorf("expected msg=%q, got %q", tt.expectedMsg, msg)
			}
		})
	}
}
