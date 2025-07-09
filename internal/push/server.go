package push

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"nopu/internal/config"
)

// Server represents the push server
type Server struct {
	cfg                 *config.Config
	redis               *redis.Client
	subscriptionConn    *websocket.Conn
	apnsClient          *APNSClient
	fcmClient           *FCMClient
	workers             []*Worker
	workerCount         int
	messageQueue        chan *PushMessage
	shutdownChan        chan struct{}
	subscriptionURL     string
	reconnectDelay      time.Duration
	maxReconnectRetries int
}

// Worker represents a worker goroutine for processing push messages
type Worker struct {
	ID     int
	server *Server
	ctx    context.Context
	cancel context.CancelFunc
}

// PushMessage represents a message to be pushed
type PushMessage struct {
	ID             string            `json:"id"`
	SubscriptionID string            `json:"subscription_id"`
	Event          *json.RawMessage  `json:"event"`
	Targets        []PushTarget      `json:"targets"`
	Priority       string            `json:"priority,omitempty"`
	TTL            int               `json:"ttl,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
}

// PushTarget represents a push target (device token, topic, etc.)
type PushTarget struct {
	Type     string `json:"type"`      // "apns", "fcm", "topic"
	Token    string `json:"token"`     // Device token or topic name
	Platform string `json:"platform"`  // "ios", "android"
	BundleID string `json:"bundle_id"` // App bundle ID
}

// FCMClient represents Firebase Cloud Messaging client
type FCMClient struct {
	ProjectID          string
	ServiceAccountPath string
	DefaultTopic       string
	// Add FCM client implementation here
}

// NewServer creates a new push server
func NewServer(cfg *config.Config) (*Server, error) {
	// Initialize Redis
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})

	// Test Redis connection
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("Redis connection failed: %v", err)
	}

	// Create APNS client
	apnsClient, err := NewAPNSClient(cfg.PushServer.Apns)
	if err != nil {
		log.Printf("Failed to initialize APNS client: %v", err)
	}

	// Create FCM client
	fcmClient := &FCMClient{
		ProjectID:          cfg.PushServer.FCM.ProjectID,
		ServiceAccountPath: cfg.PushServer.FCM.ServiceAccountPath,
		DefaultTopic:       cfg.PushServer.FCM.DefaultTopic,
	}

	server := &Server{
		cfg:                 cfg,
		redis:               rdb,
		apnsClient:          apnsClient,
		fcmClient:           fcmClient,
		workerCount:         cfg.PushServer.WorkerCount,
		messageQueue:        make(chan *PushMessage, 1000),
		shutdownChan:        make(chan struct{}),
		subscriptionURL:     cfg.PushServer.SubscriptionServerURL,
		reconnectDelay:      5 * time.Second,
		maxReconnectRetries: 10,
	}

	return server, nil
}

// Start starts the push server
func (s *Server) Start(ctx context.Context) error {
	// Start workers
	s.startWorkers(ctx)

	// Start subscription server connection
	go s.connectToSubscriptionServer(ctx)

	// Start HTTP server for health checks and metrics
	go s.startHTTPServer(ctx)

	// Start message queue processor
	go s.processMessageQueue(ctx)

	log.Printf("Push server started with %d workers", s.workerCount)
	return nil
}

// startWorkers starts worker goroutines
func (s *Server) startWorkers(ctx context.Context) {
	s.workers = make([]*Worker, s.workerCount)
	for i := 0; i < s.workerCount; i++ {
		workerCtx, cancel := context.WithCancel(ctx)
		worker := &Worker{
			ID:     i,
			server: s,
			ctx:    workerCtx,
			cancel: cancel,
		}
		s.workers[i] = worker
		go worker.start()
	}
}

// connectToSubscriptionServer connects to the subscription server
func (s *Server) connectToSubscriptionServer(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.shutdownChan:
			return
		default:
			if err := s.connect(ctx); err != nil {
				log.Printf("Failed to connect to subscription server: %v", err)
				time.Sleep(s.reconnectDelay)
				continue
			}

			// Handle connection
			s.handleSubscriptionConnection(ctx)
		}
	}
}

// connect establishes connection to subscription server
func (s *Server) connect(ctx context.Context) error {
	headers := http.Header{}
	headers.Set("X-Client-Type", "push-server")

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, s.subscriptionURL, headers)
	if err != nil {
		return fmt.Errorf("failed to dial subscription server: %w", err)
	}

	s.subscriptionConn = conn
	log.Printf("Connected to subscription server: %s", s.subscriptionURL)
	return nil
}

// handleSubscriptionConnection handles messages from subscription server
func (s *Server) handleSubscriptionConnection(ctx context.Context) {
	defer func() {
		if s.subscriptionConn != nil {
			s.subscriptionConn.Close()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.shutdownChan:
			return
		default:
			_, message, err := s.subscriptionConn.ReadMessage()
			if err != nil {
				log.Printf("Error reading from subscription server: %v", err)
				return
			}

			var msg map[string]interface{}
			if err := json.Unmarshal(message, &msg); err != nil {
				log.Printf("Error unmarshaling message: %v", err)
				continue
			}

			s.handleSubscriptionMessage(msg)
		}
	}
}

// handleSubscriptionMessage handles messages from subscription server
func (s *Server) handleSubscriptionMessage(msg map[string]interface{}) {
	msgType, ok := msg["type"].(string)
	if !ok {
		log.Printf("Missing message type")
		return
	}

	switch msgType {
	case "event":
		s.handleEventMessage(msg)
	case "ping":
		s.sendPong()
	default:
		log.Printf("Unknown message type: %s", msgType)
	}
}

// handleEventMessage handles event messages from subscription server
func (s *Server) handleEventMessage(msg map[string]interface{}) {
	subscriptionID, ok := msg["subscription_id"].(string)
	if !ok {
		log.Printf("Missing subscription_id in event message")
		return
	}

	// Get push targets for this subscription
	targets, err := s.getPushTargets(subscriptionID)
	if err != nil {
		log.Printf("Error getting push targets for subscription %s: %v", subscriptionID, err)
		return
	}

	if len(targets) == 0 {
		return // No targets to push to
	}

	// Process event field as json.RawMessage
	var eventRaw json.RawMessage
	if eventVal, ok := msg["event"]; ok {
		if eventBytes, err := json.Marshal(eventVal); err == nil {
			eventRaw = eventBytes
		}
	}

	// Create push message
	pushMsg := &PushMessage{
		ID:             generateMessageID(),
		SubscriptionID: subscriptionID,
		Event:          &eventRaw,
		Targets:        targets,
		Priority:       "high",
		TTL:            3600, // 1 hour
		CreatedAt:      time.Now(),
	}

	// Send to message queue
	select {
	case s.messageQueue <- pushMsg:
		log.Printf("Queued push message for subscription %s", subscriptionID)
	default:
		log.Printf("Message queue full, dropping message for subscription %s", subscriptionID)
	}
}

// getPushTargets gets push targets for a subscription
func (s *Server) getPushTargets(subscriptionID string) ([]PushTarget, error) {
	// This would typically query a database or cache to get push targets
	// For now, return some example targets
	return []PushTarget{
		{
			Type:     "apns",
			Token:    "example_device_token",
			Platform: "ios",
			BundleID: s.cfg.PushServer.Apns.BundleID,
		},
		{
			Type:     "fcm",
			Token:    "example_fcm_token",
			Platform: "android",
			BundleID: "com.example.app",
		},
	}, nil
}

// processMessageQueue processes messages from the queue
func (s *Server) processMessageQueue(ctx context.Context) {
	for {
		select {
		case msg := <-s.messageQueue:
			s.processPushMessage(msg)
		case <-ctx.Done():
			return
		case <-s.shutdownChan:
			return
		}
	}
}

// processPushMessage processes a single push message
func (s *Server) processPushMessage(msg *PushMessage) {
	for _, target := range msg.Targets {
		switch target.Type {
		case "apns":
			s.sendAPNSPush(msg, target)
		case "fcm":
			s.sendFCMPush(msg, target)
		case "topic":
			s.sendTopicPush(msg, target)
		default:
			log.Printf("Unknown push target type: %s", target.Type)
		}
	}
}

// sendAPNSPush sends push notification via APNs
func (s *Server) sendAPNSPush(msg *PushMessage, target PushTarget) {
	if s.apnsClient == nil {
		log.Printf("APNS client not initialized")
		return
	}

	// Create APNS payload
	// This is just an example, please construct according to business requirements for actual push
	// payload := map[string]interface{}{
	// 	"aps": map[string]interface{}{
	// 		"alert": map[string]interface{}{
	// 			"title": "New Message",
	// 			"body":  "You have a new message",
	// 		},
	// 		"badge": 1,
	// 		"sound": "default",
	// 	},
	// 	"subscription_id": msg.SubscriptionID,
	// 	"event":           msg.Event,
	// }

	log.Printf("Sending APNS push to %s", target.Token)
}

// sendFCMPush sends push notification via FCM
func (s *Server) sendFCMPush(msg *PushMessage, target PushTarget) {
	// Create FCM payload
	// payload := map[string]interface{}{
	// 	"notification": map[string]interface{}{
	// 		"title": "New Message",
	// 		"body":  "You have a new message",
	// 	},
	// 	"data": map[string]interface{}{
	// 		"subscription_id": msg.SubscriptionID,
	// 		"event":           msg.Event,
	// 	},
	// 	"token": target.Token,
	// }

	log.Printf("Sending FCM push to %s", target.Token)
}

// sendTopicPush sends push notification to a topic
func (s *Server) sendTopicPush(msg *PushMessage, target PushTarget) {
	// Send to topic
	log.Printf("Sending topic push to %s", target.Token)
}

// sendPong sends pong response to subscription server
func (s *Server) sendPong() {
	if s.subscriptionConn == nil {
		return
	}

	message := map[string]interface{}{
		"type": "pong",
	}

	data, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling pong: %v", err)
		return
	}

	if err := s.subscriptionConn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("Error sending pong: %v", err)
	}
}

// startHTTPServer starts HTTP server for health checks
func (s *Server) startHTTPServer(ctx context.Context) {
	http.HandleFunc("/health", s.handleHealth)
	http.HandleFunc("/metrics", s.handleMetrics)

	addr := fmt.Sprintf(":%d", s.cfg.PushServer.Port)
	log.Printf("Starting HTTP server on %s", addr)

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Printf("HTTP server error: %v", err)
	}
}

// handleHealth handles health check requests
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	status := "healthy"
	if s.subscriptionConn == nil {
		status = "disconnected"
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     status,
		"workers":    len(s.workers),
		"queue_size": len(s.messageQueue),
	})
}

// handleMetrics handles metrics requests
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"workers":    len(s.workers),
		"queue_size": len(s.messageQueue),
		"connected":  s.subscriptionConn != nil,
	})
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown() {
	close(s.shutdownChan)

	// Stop all workers
	for _, worker := range s.workers {
		worker.cancel()
	}

	// Close subscription connection
	if s.subscriptionConn != nil {
		s.subscriptionConn.Close()
	}

	// Close Redis connection
	if s.redis != nil {
		s.redis.Close()
	}
}

// Worker methods
func (w *Worker) start() {
	log.Printf("Worker %d started", w.ID)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			log.Printf("Worker %d stopped", w.ID)
			return
		case <-ticker.C:
			// Process any pending work
			w.processWork()
		}
	}
}

func (w *Worker) processWork() {
	// Process any worker-specific tasks
	// This could include batch processing, cleanup, etc.
}

// Helper functions
func generateMessageID() string {
	return fmt.Sprintf("msg_%d", time.Now().UnixNano())
}
