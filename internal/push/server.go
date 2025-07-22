package push

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"nopu/internal/config"
)

// PushRequest represents a push notification request
type PushRequest struct {
	DeviceToken string                 `json:"device_token"`
	Title       string                 `json:"title"`
	Body        string                 `json:"body"`
	CustomData  map[string]interface{} `json:"custom_data,omitempty"`
}

// Server represents the push server
type Server struct {
	cfg          *config.Config
	apnsClient   *APNSClient
	fcmClient    *FCMClient
	httpServer   *http.Server
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
	// Initialize APNS client if configured
	var apnsClient *APNSClient
	if cfg.PushServer.Apns.CertPath != "" {
		var err error
		apnsClient, err = NewAPNSClient(cfg.PushServer.Apns)
		if err != nil {
			log.Printf("Failed to initialize APNS client: %v", err)
		}
	}

	server := &Server{
		cfg:        cfg,
		apnsClient: apnsClient,
		httpServer: &http.Server{
			Addr: fmt.Sprintf(":%d", cfg.PushServer.Port),
		},
		shutdownChan: make(chan struct{}),
	}

	// Set up HTTP handlers
	http.HandleFunc("/push", server.handlePush)

	return server, nil
}

// Start starts the push server
func (s *Server) Start(ctx context.Context) error {
	log.Printf("Push server started")

	// Start HTTP server
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()

	// Shutdown server gracefully
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Failed to shutdown HTTP server: %v", err)
	}

	log.Printf("Push server shutdown")
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

// handlePush handles push notification requests
func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body
	var req PushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate request
	if req.DeviceToken == "" {
		http.Error(w, "Device token is required", http.StatusBadRequest)
		return
	}

	// Send push notification
	if err := s.sendPushNotification(r.Context(), req); err != nil {
		log.Printf("Push notification failed: %v", err)
		http.Error(w, "Failed to send push notification", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

// sendPushNotification sends a push notification
func (s *Server) sendPushNotification(ctx context.Context, req PushRequest) error {
	if s.apnsClient != nil {
		// Send APNS push
		resp, err := s.apnsClient.Push(ctx, req.DeviceToken, req.Title, req.Body, req.CustomData)
		if err != nil {
			return fmt.Errorf("APNS push failed: %w", err)
		}

		if resp != nil && !resp.Sent() {
			return fmt.Errorf("APNS push failed: %s", resp.Reason)
		}

		log.Printf("Successfully sent APNS push to %s", req.DeviceToken)
		return nil
	}

	return fmt.Errorf("no push service configured")
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown() {
	close(s.shutdownChan)
	log.Printf("Push server shutdown")
}
