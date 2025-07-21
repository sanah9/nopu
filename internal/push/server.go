package push

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"nopu/internal/config"
)

// Server represents the push server
type Server struct {
	cfg          *config.Config
	apnsClient   *APNSClient
	fcmClient    *FCMClient
	shutdownChan chan struct{}
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
		cfg:          cfg,
		apnsClient:   apnsClient,
		fcmClient:    fcmClient,
		shutdownChan: make(chan struct{}),
	}

	return server, nil
}

// Start starts the push server
func (s *Server) Start(ctx context.Context) error {
	// Start HTTP server for health checks and push endpoints
	go s.startHTTPServer(ctx)

	log.Printf("Push server started")
	return nil
}

// SendPushNotification sends a push notification
func (s *Server) SendPushNotification(ctx context.Context, deviceToken, title, body string, customData map[string]interface{}) error {
	if s.apnsClient == nil {
		return fmt.Errorf("APNS client not initialized")
	}

	resp, err := s.apnsClient.Push(ctx, deviceToken, title, body, customData)
	if err != nil {
		return fmt.Errorf("failed to send APNS push: %w", err)
	}

	if resp != nil && !resp.Sent() {
		return fmt.Errorf("APNS push failed: %s", resp.Reason)
	}

	log.Printf("Successfully sent APNS push to %s", deviceToken)
	return nil
}

// startHTTPServer starts HTTP server for health checks and push endpoints
func (s *Server) startHTTPServer(ctx context.Context) {
	http.HandleFunc("/health", s.handleHealth)
	http.HandleFunc("/push", s.handlePush)

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
	if s.apnsClient == nil {
		status = "apns_disconnected"
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": status,
		"apns":   s.apnsClient != nil,
		"fcm":    s.fcmClient != nil,
	})
}

// handlePush handles push notification requests
func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		DeviceToken string                 `json:"device_token"`
		Title       string                 `json:"title"`
		Body        string                 `json:"body"`
		CustomData  map[string]interface{} `json:"custom_data,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.DeviceToken == "" || req.Title == "" || req.Body == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	if err := s.SendPushNotification(r.Context(), req.DeviceToken, req.Title, req.Body, req.CustomData); err != nil {
		log.Printf("Push notification failed: %v", err)
		http.Error(w, "Push notification failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "sent",
	})
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown() {
	close(s.shutdownChan)
	log.Printf("Push server shutdown")
}
