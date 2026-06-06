package push

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"nopu/internal/config"
)

// PushRequest represents a push notification request
type PushRequest struct {
	DeviceToken string                 `json:"device_token"`
	Title       string                 `json:"title"`
	Body        string                 `json:"body"`
	CustomData  map[string]interface{} `json:"custom_data,omitempty"`
	Silent      bool                   `json:"silent,omitempty"` // Whether to send silent push
}

// Server represents the push server
type Server struct {
	cfg          *config.Config
	apnsClient   *APNSClient
	fcmClient    *FCMClient
	devices      *DeviceStore
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
		devices:    newDeviceStore(),
		httpServer: &http.Server{
			Addr: fmt.Sprintf(":%d", cfg.PushServer.Port),
		},
		shutdownChan: make(chan struct{}),
	}

	// NIP-9a device registry and relay callback endpoints.
	http.HandleFunc("/push/devices", server.handleDeviceRegister)   // POST
	http.HandleFunc("/push/devices/", server.handleDeviceDelete)    // DELETE /push/devices/{id}
	http.HandleFunc("/push/callback/", server.handleRelayCallback)  // POST  /push/callback/{id}

	// Legacy endpoints kept for backward compatibility.
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
	return s.SendPushNotificationWithSilent(ctx, deviceToken, title, body, customData, s.cfg.PushServer.Push.SilentPush)
}

// SendPushNotificationWithSilent sends a push notification with silent push option
// Automatically detects whether to use APNS or FCM based on device token format
func (s *Server) SendPushNotificationWithSilent(ctx context.Context, deviceToken, title, body string, customData map[string]interface{}, silent bool) error {
	// Detect push service based on device token format
	pushType := s.detectPushService(deviceToken)

	switch pushType {
	case "fcm":
		if s.fcmClient == nil {
			return fmt.Errorf("FCM client not initialized")
		}

		_, err := s.fcmClient.PushWithSilent(ctx, deviceToken, title, body, customData, silent)
		if err != nil {
			return fmt.Errorf("failed to send FCM push: %w", err)
		}

		pushType := "regular"
		if silent {
			pushType = "silent"
		}
		log.Printf("Successfully sent FCM %s push to %s", pushType, deviceToken)
		return nil

	case "apns":
		if s.apnsClient == nil {
			return fmt.Errorf("APNS client not initialized")
		}

		resp, err := s.apnsClient.PushWithSilent(ctx, deviceToken, title, body, customData, silent)
		if err != nil {
			return fmt.Errorf("failed to send APNS push: %w", err)
		}

		if resp != nil && !resp.Sent() {
			return fmt.Errorf("APNS push failed: %s", resp.Reason)
		}

		pushType := "regular"
		if silent {
			pushType = "silent"
		}
		log.Printf("Successfully sent APNS %s push to %s", pushType, deviceToken)
		return nil

	default:
		return fmt.Errorf("unknown device token format: %s", deviceToken)
	}
}

// detectPushService detects whether a device token is for APNS or FCM
// APNS tokens are 64 characters long and contain only hexadecimal characters
// FCM tokens are typically longer and may contain other characters
func (s *Server) detectPushService(deviceToken string) string {

	return "apns"

	// if len(deviceToken) == 64 {
	// 	// APNS tokens are always 64 characters long
	// 	// Check if it's a valid hex string (APNS format)
	// 	for _, char := range deviceToken {
	// 		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
	// 			// If contains non-hex characters, it might be a malformed APNS token
	// 			// but we'll still treat it as APNS since length is correct
	// 			log.Printf("Warning: Device token contains non-hex characters but length is 64, treating as APNS: %s", deviceToken)
	// 			return "apns"
	// 		}
	// 	}
	// 	return "apns"
	// }

	// // FCM tokens are typically longer than 64 characters
	// return "fcm"
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
	// Use request silent flag if provided, otherwise use default from config
	silent := req.Silent
	return s.SendPushNotificationWithSilent(ctx, req.DeviceToken, req.Title, req.Body, req.CustomData, silent)
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

// ── NIP-9a device registry ────────────────────────────────────────────────

type registerDeviceRequest struct {
	Pubkey     string `json:"pubkey"`
	Platform   string `json:"platform"`
	TokenType  string `json:"tokenType"`
	Token      string `json:"token"`
	DeviceID   string `json:"deviceId"`
	AppVersion string `json:"appVersion"`
}

type registerDeviceResponse struct {
	DeviceRegistrationID string `json:"deviceRegistrationId"`
	CallbackURL          string `json:"callbackUrl"`
}

// handleDeviceRegister handles POST /push/devices
func (s *Server) handleDeviceRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req registerDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Token == "" || req.TokenType == "" {
		http.Error(w, "token and tokenType are required", http.StatusBadRequest)
		return
	}

	id := uuid.New().String()
	reg := &DeviceRegistration{
		ID:         id,
		Pubkey:     req.Pubkey,
		Platform:   req.Platform,
		TokenType:  req.TokenType,
		Token:      req.Token,
		DeviceID:   req.DeviceID,
		AppVersion: req.AppVersion,
		CreatedAt:  time.Now(),
	}
	s.devices.Upsert(reg)

	publicURL := strings.TrimRight(s.cfg.PushServer.PublicURL, "/")
	callbackURL := publicURL + "/push/callback/" + id

	log.Printf("Device registered: id=%s platform=%s tokenType=%s", id, req.Platform, req.TokenType)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(registerDeviceResponse{
		DeviceRegistrationID: id,
		CallbackURL:          callbackURL,
	})
}

// handleDeviceDelete handles DELETE /push/devices/{id}
func (s *Server) handleDeviceDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/push/devices/")
	if id == "" {
		http.Error(w, "device ID is required", http.StatusBadRequest)
		return
	}

	s.devices.Delete(id)
	log.Printf("Device unregistered: id=%s", id)
	w.WriteHeader(http.StatusNoContent)
}

// ── NIP-9a relay callback ─────────────────────────────────────────────────

// relayCallbackPayload is the shape sent by NIP-9a relays to the callback URL.
type relayCallbackPayload struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Relay string          `json:"relay"`
	Event json.RawMessage `json:"event"`
}

// handleRelayCallback handles POST /push/callback/{deviceRegistrationId}
func (s *Server) handleRelayCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/push/callback/")
	if id == "" {
		http.Error(w, "device ID is required", http.StatusBadRequest)
		return
	}

	reg, ok := s.devices.Get(id)
	if !ok {
		// Unknown device — respond 200 so the relay doesn't retry forever.
		log.Printf("Relay callback for unknown device: %s", id)
		w.WriteHeader(http.StatusOK)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	var cbPayload relayCallbackPayload
	if err := json.Unmarshal(body, &cbPayload); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	switch reg.TokenType {
	case "apns_voip":
		if s.apnsClient == nil {
			log.Printf("APNs client not configured, cannot deliver to device %s", id)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// Build a flat map so NostrPushPayloadHandler can read top-level keys.
		var eventRaw interface{}
		_ = json.Unmarshal(cbPayload.Event, &eventRaw)
		data := map[string]interface{}{
			"type":  cbPayload.Type,
			"id":    cbPayload.ID,
			"relay": cbPayload.Relay,
			"event": eventRaw,
		}
		resp, err := s.apnsClient.PushVoIP(ctx, reg.Token, data)
		if err != nil {
			log.Printf("APNs VoIP push failed for device %s: %v", id, err)
			http.Error(w, "Push failed", http.StatusInternalServerError)
			return
		}
		if resp != nil && !resp.Sent() {
			log.Printf("APNs VoIP push rejected for device %s: %s", id, resp.Reason)
		} else {
			log.Printf("APNs VoIP push delivered to device %s", id)
		}

	case "unifiedpush":
		if err := sendUnifiedPush(ctx, reg.Token, body); err != nil {
			log.Printf("UnifiedPush delivery failed for device %s: %v", id, err)
			http.Error(w, "Push failed", http.StatusInternalServerError)
			return
		}
		log.Printf("UnifiedPush delivered to device %s", id)

	default:
		log.Printf("Unknown tokenType %q for device %s", reg.TokenType, id)
		http.Error(w, "Unsupported tokenType", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}
