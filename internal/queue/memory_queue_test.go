package queue

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

func TestMemoryQueue(t *testing.T) {
	// Create queue with small size for testing
	queue := NewMemoryQueue(100, 1*time.Hour)

	// Test adding events
	event1 := &nostr.Event{
		ID:        "test1",
		Kind:      1,
		Content:   "test content 1",
		CreatedAt: nostr.Now(),
	}

	event2 := &nostr.Event{
		ID:        "test2",
		Kind:      7,
		Content:   "test content 2",
		CreatedAt: nostr.Now(),
	}

	// Add events
	if err := queue.AddEvent(context.Background(), event1); err != nil {
		t.Fatalf("Failed to add event1: %v", err)
	}

	if err := queue.AddEvent(context.Background(), event2); err != nil {
		t.Fatalf("Failed to add event2: %v", err)
	}

	// Test deduplication
	if err := queue.AddEvent(context.Background(), event1); err != nil {
		t.Fatalf("Failed to add duplicate event: %v", err)
	}

	// Create consumer
	consumer, err := queue.CreateConsumer(context.Background(), "test-consumer", 10)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}

	// Read events
	messages, err := queue.ReadEvents(context.Background(), consumer.ID, 5, 1*time.Second)
	if err != nil {
		t.Fatalf("Failed to read events: %v", err)
	}

	// Should have 2 unique events
	if len(messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(messages))
	}

	// Check event IDs
	found := make(map[string]bool)
	for _, msg := range messages {
		found[msg.Event.ID] = true
	}

	if !found["test1"] || !found["test2"] {
		t.Error("Expected to find both test1 and test2 events")
	}

	// Test stats
	stats := queue.GetStats()
	if stats["queue_size"].(int) != 2 {
		t.Errorf("Expected queue size 2, got %d", stats["queue_size"])
	}

	if stats["consumers"].(int) != 1 {
		t.Errorf("Expected 1 consumer, got %d", stats["consumers"])
	}

	// Cleanup
	queue.RemoveConsumer(consumer.ID)
}

func TestMemoryQueueDeduplication(t *testing.T) {
	queue := NewMemoryQueue(100, 1*time.Hour)

	event := &nostr.Event{
		ID:        "duplicate-test",
		Kind:      1,
		Content:   "test content",
		CreatedAt: nostr.Now(),
	}

	// Add same event multiple times
	for i := 0; i < 5; i++ {
		if err := queue.AddEvent(context.Background(), event); err != nil {
			t.Fatalf("Failed to add event: %v", err)
		}
	}

	// Create consumer
	consumer, err := queue.CreateConsumer(context.Background(), "dedup-test", 10)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}

	// Read events
	messages, err := queue.ReadEvents(context.Background(), consumer.ID, 10, 1*time.Second)
	if err != nil {
		t.Fatalf("Failed to read events: %v", err)
	}

	// Should only have 1 event due to deduplication
	if len(messages) != 1 {
		t.Errorf("Expected 1 message after deduplication, got %d", len(messages))
	}

	if messages[0].Event.ID != "duplicate-test" {
		t.Errorf("Expected event ID 'duplicate-test', got %s", messages[0].Event.ID)
	}

	// Cleanup
	queue.RemoveConsumer(consumer.ID)
}

func TestMemoryQueueMaxSize(t *testing.T) {
	// Create queue with very small size
	queue := NewMemoryQueue(2, 1*time.Hour)

	// Add 3 events (exceeds max size)
	for i := 0; i < 3; i++ {
		event := &nostr.Event{
			ID:        fmt.Sprintf("test%d", i),
			Kind:      1,
			Content:   fmt.Sprintf("content %d", i),
			CreatedAt: nostr.Now(),
		}

		if err := queue.AddEvent(context.Background(), event); err != nil {
			t.Fatalf("Failed to add event %d: %v", i, err)
		}
	}

	// Create consumer
	consumer, err := queue.CreateConsumer(context.Background(), "maxsize-test", 10)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}

	// Read events
	messages, err := queue.ReadEvents(context.Background(), consumer.ID, 10, 1*time.Second)
	if err != nil {
		t.Fatalf("Failed to read events: %v", err)
	}

	// Should only have 2 events (max size)
	if len(messages) != 2 {
		t.Errorf("Expected 2 messages (max size), got %d", len(messages))
	}

	// Should have the latest events (oldest should be dropped)
	expectedIDs := map[string]bool{"test1": true, "test2": true}
	for _, msg := range messages {
		if !expectedIDs[msg.Event.ID] {
			t.Errorf("Unexpected event ID: %s", msg.Event.ID)
		}
	}

	// Cleanup
	queue.RemoveConsumer(consumer.ID)
}
