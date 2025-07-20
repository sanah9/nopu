package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/fiatjaf/eventstore/lmdb"
	"github.com/fiatjaf/khatru"
	"github.com/fiatjaf/khatru/policies"
	"github.com/fiatjaf/relay29"
	"github.com/fiatjaf/relay29/khatru29"
	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip29"

	"nopu/internal/config"
	"nopu/internal/listener"
	"nopu/internal/presence"
	"nopu/internal/queue"
)

// Server represents the subscription server
type Server struct {
	cfg           *config.Config
	db            *lmdb.LMDBBackend
	queue         *queue.MemoryQueue
	relay         *khatru.Relay
	state         *relay29.State
	listener      *listener.Listener
	clients       map[string]*Client
	clientsMutex  sync.RWMutex
	pushConn      *websocket.Conn
	pushConnMutex sync.RWMutex
	upgrader      websocket.Upgrader
	eventChan     chan *nostr.Event
	shutdownChan  chan struct{}
}

// Client represents a connected client
type Client struct {
	ID            string
	Conn          *websocket.Conn
	Subscriptions []Subscription
	LastSeen      time.Time
	Mutex         sync.RWMutex
}

// Subscription represents a client subscription
type Subscription struct {
	ID       string            `json:"id"`
	Filters  []nostr.Filter    `json:"filters"`
	Created  time.Time         `json:"created"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// NewServer creates a new subscription server
func NewServer(cfg *config.Config) (*Server, error) {
	// Initialize LMDB storage
	db := &lmdb.LMDBBackend{
		Path: "./data/nopu.lmdb",
	}
	if err := db.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize LMDB: %v", err)
	}

	// Initialize memory queue
	queue := queue.NewMemoryQueue(cfg.MemoryQueue.MaxSize, cfg.MemoryQueue.DedupeTTL)

	// Initialize NIP-29 relay
	relay, state := khatru29.Init(relay29.Options{
		Domain:                  cfg.SubscriptionServer.Domain,
		DB:                      db,
		SecretKey:               cfg.SubscriptionServer.RelayPrivateKey,
		DefaultRoles:            []*nip29.Role{&nip29.Role{Name: "king", Description: "the group's max top admin"}},
		GroupCreatorDefaultRole: &nip29.Role{Name: "king", Description: "the group's max top admin"},
	})

	// Install presence tracking hooks
	presence.SetupPresenceHooks(relay)

	// Configure relay information
	relay.Info.Name = cfg.SubscriptionServer.RelayName
	relay.Info.Description = cfg.SubscriptionServer.RelayDescription

	// Set event restriction policies
	relay.RejectEvent = append(relay.RejectEvent,
		policies.PreventLargeTags(64),
		policies.PreventTooManyIndexableTags(6, []int{9005}, nil),
		policies.RestrictToSpecifiedKinds(
			true,
			9000, 9001, 9002, 9003, 9004, 9005, 9006, 9007, 9008, 9009, // Group management
			9021, 9022, 20284, // Group invitations
		),
		policies.PreventTimestampsInThePast(60*time.Second),
		policies.PreventTimestampsInTheFuture(30*time.Second),
	)

	// Initialize event listener
	eventListener := listener.New(cfg.SubscriptionServer.Listener, queue)

	server := &Server{
		cfg:          cfg,
		db:           db,
		queue:        queue,
		relay:        relay,
		state:        state,
		listener:     eventListener,
		clients:      make(map[string]*Client),
		upgrader:     websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		eventChan:    make(chan *nostr.Event, 1000),
		shutdownChan: make(chan struct{}),
	}

	// Set up event processing
	relay.OnEventSaved = append(relay.OnEventSaved, server.handleEvent)

	return server, nil
}

// Start starts the subscription server
func (s *Server) Start(ctx context.Context) error {
	// Start event listener
	go func() {
		if err := s.listener.Start(ctx); err != nil {
			if ctx.Err() == nil {
				log.Printf("Event listener error: %v", err)
			}
		}
	}()

	// Start event processor
	go s.processEvents(ctx)

	// Start client cleanup routine
	go s.cleanupClients(ctx)

	// Start HTTP server
	addr := fmt.Sprintf(":%d", s.cfg.SubscriptionServer.Port)
	log.Printf("Starting subscription server on %s", addr)

	http.HandleFunc("/ws", s.handleWebSocket)
	http.HandleFunc("/health", s.handleHealth)

	return http.ListenAndServe(addr, nil)
}

// handleWebSocket handles WebSocket connections from clients
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	// Check if this is a push server connection
	clientType := r.Header.Get("X-Client-Type")
	if clientType == "push-server" {
		s.handlePushServerConnection(conn)
		return
	}

	clientID := r.Header.Get("X-Client-ID")
	if clientID == "" {
		clientID = generateClientID()
	}

	client := &Client{
		ID:       clientID,
		Conn:     conn,
		LastSeen: time.Now(),
	}

	s.clientsMutex.Lock()
	s.clients[clientID] = client
	s.clientsMutex.Unlock()

	log.Printf("Client connected: %s", clientID)

	// Handle client messages
	go s.handleClientMessages(client)
}

// handlePushServerConnection handles connection from push server
func (s *Server) handlePushServerConnection(conn *websocket.Conn) {
	s.pushConnMutex.Lock()
	s.pushConn = conn
	s.pushConnMutex.Unlock()

	log.Printf("Push server connected")

	// Handle push server messages
	go s.handlePushServerMessages(conn)
}

// handlePushServerMessages handles messages from push server
func (s *Server) handlePushServerMessages(conn *websocket.Conn) {
	defer func() {
		conn.Close()
		s.pushConnMutex.Lock()
		s.pushConn = nil
		s.pushConnMutex.Unlock()
		log.Printf("Push server disconnected")
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Error reading message from push server: %v", err)
			return
		}

		var msg map[string]interface{}
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("Invalid JSON from push server: %v", err)
			continue
		}

		msgType, ok := msg["type"].(string)
		if !ok {
			log.Printf("Missing message type from push server")
			continue
		}

		switch msgType {
		case "pong":
			log.Printf("Received pong from push server")
		default:
			log.Printf("Unknown message type from push server: %s", msgType)
		}
	}
}

// handleClientMessages handles messages from a specific client
func (s *Server) handleClientMessages(client *Client) {
	defer func() {
		client.Conn.Close()
		s.clientsMutex.Lock()
		delete(s.clients, client.ID)
		s.clientsMutex.Unlock()
		log.Printf("Client disconnected: %s", client.ID)
	}()

	for {
		_, message, err := client.Conn.ReadMessage()
		if err != nil {
			log.Printf("Error reading message from client %s: %v", client.ID, err)
			return
		}

		client.LastSeen = time.Now()

		var msg map[string]interface{}
		if err := json.Unmarshal(message, &msg); err != nil {
			s.sendError(client, "Invalid JSON")
			continue
		}

		msgType, ok := msg["type"].(string)
		if !ok {
			s.sendError(client, "Missing message type")
			continue
		}

		switch msgType {
		case "subscribe":
			s.handleSubscribe(client, msg)
		case "unsubscribe":
			s.handleUnsubscribe(client, msg)
		case "ping":
			s.sendPong(client)
		default:
			s.sendError(client, "Unknown message type")
		}
	}
}

// handleSubscribe handles subscription requests from clients
func (s *Server) handleSubscribe(client *Client, msg map[string]interface{}) {
	client.Mutex.Lock()
	defer client.Mutex.Unlock()

	if len(client.Subscriptions) >= s.cfg.SubscriptionServer.MaxSubscriptions {
		s.sendError(client, "Maximum subscriptions reached")
		return
	}

	// Parse filters from message
	filtersData, ok := msg["filters"].([]interface{})
	if !ok {
		s.sendError(client, "Invalid filters")
		return
	}

	var filters []nostr.Filter
	for _, filterData := range filtersData {
		filterBytes, _ := json.Marshal(filterData)
		var filter nostr.Filter
		if err := json.Unmarshal(filterBytes, &filter); err != nil {
			s.sendError(client, "Invalid filter format")
			return
		}
		filters = append(filters, filter)
	}

	subscription := Subscription{
		ID:       generateSubscriptionID(),
		Filters:  filters,
		Created:  time.Now(),
		Metadata: make(map[string]string),
	}

	// Add metadata if provided
	if metadata, ok := msg["metadata"].(map[string]interface{}); ok {
		for k, v := range metadata {
			if str, ok := v.(string); ok {
				subscription.Metadata[k] = str
			}
		}
	}

	client.Subscriptions = append(client.Subscriptions, subscription)

	// Send confirmation
	response := map[string]interface{}{
		"type": "subscribed",
		"id":   subscription.ID,
	}
	s.sendMessage(client, response)

	log.Printf("Client %s subscribed with ID %s", client.ID, subscription.ID)
}

// handleUnsubscribe handles unsubscription requests from clients
func (s *Server) handleUnsubscribe(client *Client, msg map[string]interface{}) {
	client.Mutex.Lock()
	defer client.Mutex.Unlock()

	subscriptionID, ok := msg["id"].(string)
	if !ok {
		s.sendError(client, "Missing subscription ID")
		return
	}

	// Find and remove subscription
	for i, sub := range client.Subscriptions {
		if sub.ID == subscriptionID {
			client.Subscriptions = append(client.Subscriptions[:i], client.Subscriptions[i+1:]...)

			response := map[string]interface{}{
				"type": "unsubscribed",
				"id":   subscriptionID,
			}
			s.sendMessage(client, response)

			log.Printf("Client %s unsubscribed from %s", client.ID, subscriptionID)
			return
		}
	}

	s.sendError(client, "Subscription not found")
}

// handleEvent processes events from the relay
func (s *Server) handleEvent(ctx context.Context, event *nostr.Event) {
	select {
	case s.eventChan <- event:
	default:
		log.Printf("Event channel full, dropping event")
	}
}

// processEvents processes events and forwards them to relevant clients
func (s *Server) processEvents(ctx context.Context) {
	for {
		select {
		case event := <-s.eventChan:
			s.forwardEventToClients(event)
			s.forwardEventToPushServer(event)
		case <-ctx.Done():
			return
		case <-s.shutdownChan:
			return
		}
	}
}

// forwardEventToClients forwards an event to all clients that have matching subscriptions
func (s *Server) forwardEventToClients(event *nostr.Event) {
	s.clientsMutex.RLock()
	defer s.clientsMutex.RUnlock()

	for _, client := range s.clients {
		client.Mutex.RLock()
		for _, subscription := range client.Subscriptions {
			if s.matchesFilters(event, subscription.Filters) {
				message := map[string]interface{}{
					"type":            "event",
					"subscription_id": subscription.ID,
					"event":           event,
				}
				s.sendMessage(client, message)
			}
		}
		client.Mutex.RUnlock()
	}
}

// matchesFilters checks if an event matches any of the given filters
func (s *Server) matchesFilters(event *nostr.Event, filters []nostr.Filter) bool {
	for _, filter := range filters {
		// Check if event matches this filter
		if filter.Kinds != nil {
			found := false
			for _, kind := range filter.Kinds {
				if event.Kind == kind {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		if filter.Authors != nil {
			found := false
			for _, author := range filter.Authors {
				if event.PubKey == author {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		if filter.IDs != nil {
			found := false
			for _, id := range filter.IDs {
				if event.ID == id {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// If we get here, the event matches this filter
		return true
	}
	return false
}

// sendMessage sends a message to a client
func (s *Server) sendMessage(client *Client, message map[string]interface{}) {
	data, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling message: %v", err)
		return
	}

	if err := client.Conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("Error sending message to client %s: %v", client.ID, err)
	}
}

// sendError sends an error message to a client
func (s *Server) sendError(client *Client, error string) {
	message := map[string]interface{}{
		"type":  "error",
		"error": error,
	}
	s.sendMessage(client, message)
}

// sendPong sends a pong response to a client
func (s *Server) sendPong(client *Client) {
	message := map[string]interface{}{
		"type": "pong",
	}
	s.sendMessage(client, message)
}

// handleHealth handles health check requests
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "healthy",
		"clients": len(s.clients),
	})
}

// cleanupClients periodically cleans up inactive clients
func (s *Server) cleanupClients(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.clientsMutex.Lock()
			now := time.Now()
			for id, client := range s.clients {
				if now.Sub(client.LastSeen) > 30*time.Minute {
					client.Conn.Close()
					delete(s.clients, id)
					log.Printf("Cleaned up inactive client: %s", id)
				}
			}
			s.clientsMutex.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown() {
	close(s.shutdownChan)

	// Close all client connections
	s.clientsMutex.Lock()
	for _, client := range s.clients {
		client.Conn.Close()
	}
	s.clientsMutex.Unlock()

	// Close database connections
	if s.db != nil {
		s.db.Close()
	}
	// The queue does not have a direct Close method, so we just log the shutdown
	log.Printf("Memory queue shutdown")
}

// Helper functions
func generateClientID() string {
	return fmt.Sprintf("client_%d", time.Now().UnixNano())
}

func generateSubscriptionID() string {
	return fmt.Sprintf("sub_%d", time.Now().UnixNano())
}

// forwardEventToPushServer forwards an event to push server
func (s *Server) forwardEventToPushServer(event *nostr.Event) {
	s.pushConnMutex.RLock()
	conn := s.pushConn
	s.pushConnMutex.RUnlock()

	if conn == nil {
		log.Printf("Push server not connected, skipping event %s", event.ID[:8])
		return
	}

	// Find matching subscriptions for this event
	s.clientsMutex.RLock()
	var matchingSubscriptions []string
	for _, client := range s.clients {
		client.Mutex.RLock()
		for _, sub := range client.Subscriptions {
			if s.matchesFilters(event, sub.Filters) {
				matchingSubscriptions = append(matchingSubscriptions, sub.ID)
			}
		}
		client.Mutex.RUnlock()
	}
	s.clientsMutex.RUnlock()

	if len(matchingSubscriptions) == 0 {
		log.Printf("No matching subscriptions for event %s", event.ID[:8])
		return
	}

	// Send event to push server for each matching subscription
	for _, subscriptionID := range matchingSubscriptions {
		message := map[string]interface{}{
			"type":            "event",
			"subscription_id": subscriptionID,
			"event":           event,
		}

		data, err := json.Marshal(message)
		if err != nil {
			log.Printf("Error marshaling event for push server: %v", err)
			continue
		}

		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			log.Printf("Error sending event to push server: %v", err)
			// Mark connection as lost
			s.pushConnMutex.Lock()
			s.pushConn = nil
			s.pushConnMutex.Unlock()
			return
		}

		log.Printf("Forwarded event %s to push server for subscription %s", event.ID[:8], subscriptionID)
	}
}
