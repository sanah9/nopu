package subscription

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// MemoryQueue represents an in-memory event queue
type MemoryQueue struct {
	events      []*EventMessage
	eventsMu    sync.RWMutex
	maxSize     int
	consumers   map[string]*Consumer
	consumersMu sync.RWMutex
	dedupeCache *DedupeCache
}

// EventMessage represents a message in the queue
type EventMessage struct {
	ID        string                 `json:"id"`
	Event     *nostr.Event           `json:"event"`
	Kind      int                    `json:"kind"`
	Timestamp time.Time              `json:"timestamp"`
	Values    map[string]interface{} `json:"values"`
}

// Consumer represents a consumer of the queue
type Consumer struct {
	ID       string
	Position int
	Messages chan *EventMessage
	ctx      context.Context
	cancel   context.CancelFunc
}

// DedupeCache represents a deduplication cache
type DedupeCache struct {
	cache map[string]time.Time
	mu    sync.RWMutex
	ttl   time.Duration
}

// NewMemoryQueue creates a new memory queue
func NewMemoryQueue(maxSize int, dedupeTTL time.Duration) *MemoryQueue {
	queue := &MemoryQueue{
		events:      make([]*EventMessage, 0, maxSize),
		maxSize:     maxSize,
		consumers:   make(map[string]*Consumer),
		dedupeCache: NewDedupeCache(dedupeTTL),
	}

	// Start cleanup routine
	go queue.cleanupRoutine()

	return queue
}

// NewDedupeCache creates a new deduplication cache
func NewDedupeCache(ttl time.Duration) *DedupeCache {
	cache := &DedupeCache{
		cache: make(map[string]time.Time),
		ttl:   ttl,
	}

	// Start cleanup routine
	go cache.cleanupRoutine()

	return cache
}

// AddEvent adds an event to the queue
func (q *MemoryQueue) AddEvent(ctx context.Context, event *nostr.Event) error {
	// Check deduplication
	if q.dedupeCache.IsDuplicate(event.ID) {
		return nil // Event already processed
	}

	// Mark as processed
	q.dedupeCache.Add(event.ID)

	// Create message
	messageID := fmt.Sprintf("%d-%s", time.Now().UnixNano(), event.ID)
	if len(event.ID) >= 8 {
		messageID = fmt.Sprintf("%d-%s", time.Now().UnixNano(), event.ID[:8])
	}

	message := &EventMessage{
		ID:        messageID,
		Event:     event,
		Kind:      event.Kind,
		Timestamp: time.Now(),
		Values: map[string]interface{}{
			"event": event,
			"kind":  event.Kind,
			"id":    event.ID,
		},
	}

	// Add to queue
	q.eventsMu.Lock()
	defer q.eventsMu.Unlock()

	// Check if queue is full
	if len(q.events) >= q.maxSize {
		// Remove oldest event
		q.events = q.events[1:]
	}

	q.events = append(q.events, message)

	// Notify all consumers
	q.consumersMu.RLock()
	for _, consumer := range q.consumers {
		select {
		case consumer.Messages <- message:
		default:
			// Consumer buffer is full, skip
		}
	}
	q.consumersMu.RUnlock()

	return nil
}

// CreateConsumer creates a new consumer
func (q *MemoryQueue) CreateConsumer(ctx context.Context, consumerID string, bufferSize int) (*Consumer, error) {
	consumerCtx, cancel := context.WithCancel(ctx)

	consumer := &Consumer{
		ID:       consumerID,
		Position: 0,
		Messages: make(chan *EventMessage, bufferSize),
		ctx:      consumerCtx,
		cancel:   cancel,
	}

	q.consumersMu.Lock()
	q.consumers[consumerID] = consumer
	q.consumersMu.Unlock()

	// Send existing events to consumer (non-blocking)
	q.eventsMu.RLock()
	for _, event := range q.events {
		select {
		case consumer.Messages <- event:
		default:
			// Buffer full, skip
		}
	}
	q.eventsMu.RUnlock()

	return consumer, nil
}

// RemoveConsumer removes a consumer
func (q *MemoryQueue) RemoveConsumer(consumerID string) {
	q.consumersMu.Lock()
	if consumer, exists := q.consumers[consumerID]; exists {
		consumer.cancel()
		close(consumer.Messages)
		delete(q.consumers, consumerID)
	}
	q.consumersMu.Unlock()
}

// ReadEvents reads events for a consumer
func (q *MemoryQueue) ReadEvents(ctx context.Context, consumerID string, count int, timeout time.Duration) ([]*EventMessage, error) {
	q.consumersMu.RLock()
	consumer, exists := q.consumers[consumerID]
	q.consumersMu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("consumer %s not found", consumerID)
	}

	var messages []*EventMessage
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for i := 0; i < count; i++ {
		select {
		case msg := <-consumer.Messages:
			messages = append(messages, msg)
		case <-timer.C:
			return messages, nil
		case <-ctx.Done():
			return messages, ctx.Err()
		case <-consumer.ctx.Done():
			return messages, consumer.ctx.Err()
		}
	}

	return messages, nil
}

// cleanupRoutine periodically cleans up old events
func (q *MemoryQueue) cleanupRoutine() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		q.eventsMu.Lock()
		// Keep only last 1000 events
		if len(q.events) > 1000 {
			q.events = q.events[len(q.events)-1000:]
		}
		q.eventsMu.Unlock()
	}
}

// IsDuplicate checks if an event ID is already processed
func (dc *DedupeCache) IsDuplicate(eventID string) bool {
	dc.mu.RLock()
	defer dc.mu.RUnlock()

	_, exists := dc.cache[eventID]
	return exists
}

// Add adds an event ID to the cache
func (dc *DedupeCache) Add(eventID string) {
	dc.mu.Lock()
	defer dc.mu.Unlock()

	dc.cache[eventID] = time.Now()
}

// cleanupRoutine periodically cleans up expired entries
func (dc *DedupeCache) cleanupRoutine() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		dc.mu.Lock()
		now := time.Now()
		for id, timestamp := range dc.cache {
			if now.Sub(timestamp) > dc.ttl {
				delete(dc.cache, id)
			}
		}
		dc.mu.Unlock()
	}
}

// GetStats returns queue statistics
func (q *MemoryQueue) GetStats() map[string]interface{} {
	q.eventsMu.RLock()
	q.consumersMu.RLock()
	defer q.eventsMu.RUnlock()
	defer q.consumersMu.RUnlock()

	return map[string]interface{}{
		"queue_size":  len(q.events),
		"max_size":    q.maxSize,
		"consumers":   len(q.consumers),
		"dedupe_size": q.dedupeCache.getSize(),
	}
}

// getSize returns the size of the dedupe cache
func (dc *DedupeCache) getSize() int {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	return len(dc.cache)
}
