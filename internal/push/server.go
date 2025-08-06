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

	// Initialize FCM client if configured
	var fcmClient *FCMClient
	if cfg.PushServer.FCM.ProjectID != "" && cfg.PushServer.FCM.ServiceAccountPath != "" {
		var err error
		fcmClient, err = NewFCMClient(cfg.PushServer.FCM)
		if err != nil {
			log.Printf("Failed to initialize FCM client: %v", err)
		}
	}

	server := &Server{
		cfg:        cfg,
		apnsClient: apnsClient,
		fcmClient:  fcmClient,
		httpServer: &http.Server{
			Addr: fmt.Sprintf(":%d", cfg.PushServer.Port),
		},
		shutdownChan: make(chan struct{}),
	}

	// Set up HTTP handlers
	http.HandleFunc("/push", server.handlePush)
	http.HandleFunc("/push/fcm/topic", server.handleFCMTopicPush)
	http.HandleFunc("/push/fcm/subscribe", server.handleFCMSubscribe)
	http.HandleFunc("/push/fcm/unsubscribe", server.handleFCMUnsubscribe)

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
// Automatically detects whether to use APNS or FCM based on device token format
func (s *Server) SendPushNotification(ctx context.Context, deviceToken, title, body string, customData map[string]interface{}) error {
	// Detect push service based on device token format
	pushType := s.detectPushService(deviceToken)

	switch pushType {
	case "fcm":
		if s.fcmClient == nil {
			return fmt.Errorf("FCM client not initialized")
		}

		_, err := s.fcmClient.Push(ctx, deviceToken, title, body, customData)
		if err != nil {
			return fmt.Errorf("failed to send FCM push: %w", err)
		}

		log.Printf("Successfully sent FCM push to %s", deviceToken)
		return nil

	case "apns":
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

	default:
		return fmt.Errorf("unknown device token format: %s", deviceToken)
	}
}

// detectPushService detects whether a device token is for APNS or FCM
// APNS tokens are 64 characters long and contain only hexadecimal characters
// FCM tokens are typically longer and may contain other characters
func (s *Server) detectPushService(deviceToken string) string {
	if len(deviceToken) == 64 {
		// Check if it's a valid hex string (APNS format)
		for _, char := range deviceToken {
			if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
				return "fcm"
			}
		}
		return "apns"
	}

	// FCM tokens are typically longer than 64 characters
	return "fcm"
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
	return s.SendPushNotification(ctx, req.DeviceToken, req.Title, req.Body, req.CustomData)
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown() {
	close(s.shutdownChan)
	log.Printf("Push server shutdown")
}

// FCMTopicPushRequest represents a FCM topic push request
type FCMTopicPushRequest struct {
	Topic      string                 `json:"topic"`
	Title      string                 `json:"title"`
	Body       string                 `json:"body"`
	CustomData map[string]interface{} `json:"custom_data,omitempty"`
}

// FCMSubscribeRequest represents a FCM subscribe request
type FCMSubscribeRequest struct {
	DeviceTokens []string `json:"device_tokens"`
	Topic        string   `json:"topic"`
}

// handleFCMTopicPush handles FCM topic push requests
func (s *Server) handleFCMTopicPush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.fcmClient == nil {
		http.Error(w, "FCM client not configured", http.StatusServiceUnavailable)
		return
	}

	// Parse request body
	var req FCMTopicPushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate request
	if req.Title == "" || req.Body == "" {
		http.Error(w, "Title and body are required", http.StatusBadRequest)
		return
	}

	// Send FCM topic push
	_, err := s.fcmClient.PushToTopic(r.Context(), req.Topic, req.Title, req.Body, req.CustomData)
	if err != nil {
		log.Printf("FCM topic push failed: %v", err)
		http.Error(w, "Failed to send FCM topic push", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

// handleFCMSubscribe handles FCM subscribe requests
func (s *Server) handleFCMSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.fcmClient == nil {
		http.Error(w, "FCM client not configured", http.StatusServiceUnavailable)
		return
	}

	// Parse request body
	var req FCMSubscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate request
	if len(req.DeviceTokens) == 0 {
		http.Error(w, "Device tokens are required", http.StatusBadRequest)
		return
	}

	// Subscribe to topic
	err := s.fcmClient.SubscribeToTopic(r.Context(), req.DeviceTokens, req.Topic)
	if err != nil {
		log.Printf("FCM subscribe failed: %v", err)
		http.Error(w, "Failed to subscribe to topic", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

// handleFCMUnsubscribe handles FCM unsubscribe requests
func (s *Server) handleFCMUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.fcmClient == nil {
		http.Error(w, "FCM client not configured", http.StatusServiceUnavailable)
		return
	}

	// Parse request body
	var req FCMSubscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate request
	if len(req.DeviceTokens) == 0 {
		http.Error(w, "Device tokens are required", http.StatusBadRequest)
		return
	}

	// Unsubscribe from topic
	err := s.fcmClient.UnsubscribeFromTopic(r.Context(), req.DeviceTokens, req.Topic)
	if err != nil {
		log.Printf("FCM unsubscribe failed: %v", err)
		http.Error(w, "Failed to unsubscribe from topic", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}
