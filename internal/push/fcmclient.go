package push

import (
	"context"
	"fmt"
	"log"

	"firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
	"nopu/internal/config"
)

// FCMClient wraps Firebase Cloud Messaging client for sending push notifications.
// Uses service account authentication.
// Thread-safe and reusable.
//
// Example:
//   cfg, _ := config.Load()
//   fcmCli, err := NewFCMClient(cfg.PushServer.FCM)
//   if err != nil { /* handle error */ }
//   resp, err := fcmCli.Push(ctx, deviceToken, "Title", "Body", nil)

type FCMClient struct {
	client      *messaging.Client
	projectID   string
	defaultTopic string
}

// FCMResponse represents FCM response
type FCMResponse struct {
	SuccessCount int
	FailureCount int
	Responses    []*messaging.SendResponse
}

// NewFCMClient creates a new FCMClient with the given configuration.
func NewFCMClient(cfg config.FCMConfig) (*FCMClient, error) {
	if cfg.ProjectID == "" || cfg.ServiceAccountPath == "" {
		return nil, fmt.Errorf("incomplete fcm configuration (require project_id & service_account_path)")
	}

	// Initialize Firebase app
	opt := option.WithCredentialsFile(cfg.ServiceAccountPath)
	app, err := firebase.NewApp(context.Background(), &firebase.Config{
		ProjectID: cfg.ProjectID,
	}, opt)
	if err != nil {
		return nil, fmt.Errorf("initialize firebase app failed: %w", err)
	}

	// Get messaging client
	client, err := app.Messaging(context.Background())
	if err != nil {
		return nil, fmt.Errorf("get messaging client failed: %w", err)
	}

	return &FCMClient{
		client:       client,
		projectID:    cfg.ProjectID,
		defaultTopic: cfg.DefaultTopic,
	}, nil
}

// Push sends a notification to a specific device token.
// customData can be nil when no extra fields are required.
func (f *FCMClient) Push(ctx context.Context, deviceToken, title, body string, customData map[string]interface{}) (*FCMResponse, error) {
	if f.client == nil {
		return nil, fmt.Errorf("FCM client not initialized")
	}
	
	if deviceToken == "" {
		return nil, fmt.Errorf("device token is empty")
	}

	// Build message
	message := &messaging.Message{
		Token: deviceToken,
		Notification: &messaging.Notification{
			Title: title,
			Body:  body,
		},
		Android: &messaging.AndroidConfig{
			Notification: &messaging.AndroidNotification{
				Title: title,
				Body:  body,
				Sound: "default",
			},
			Priority: "high",
		},
		APNS: &messaging.APNSConfig{
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					Alert: &messaging.ApsAlert{
						Title: title,
						Body:  body,
					},
					Sound: "default",
					Badge: func() *int { v := 1; return &v }(),
				},
			},
			Headers: map[string]string{
				"apns-priority": "10",
			},
		},
		Data: make(map[string]string),
	}

	// Add custom data
	if customData != nil {
		for k, v := range customData {
			message.Data[k] = fmt.Sprintf("%v", v)
		}
	}

	// Send message
	resp, err := f.client.Send(ctx, message)
	if err != nil {
		return nil, fmt.Errorf("send fcm message failed: %w", err)
	}

	log.Printf("Successfully sent FCM message to %s, message ID: %s", deviceToken, resp)
	
	return &FCMResponse{
		SuccessCount: 1,
		FailureCount: 0,
	}, nil
}

// PushToTopic sends a notification to a topic.
// If topic is empty, uses the default topic.
func (f *FCMClient) PushToTopic(ctx context.Context, topic, title, body string, customData map[string]interface{}) (*FCMResponse, error) {
	if f.client == nil {
		return nil, fmt.Errorf("FCM client not initialized")
	}
	
	if topic == "" {
		topic = f.defaultTopic
	}

	// Build message
	message := &messaging.Message{
		Topic: topic,
		Notification: &messaging.Notification{
			Title: title,
			Body:  body,
		},
		Android: &messaging.AndroidConfig{
			Notification: &messaging.AndroidNotification{
				Title: title,
				Body:  body,
				Sound: "default",
			},
			Priority: "high",
		},
		APNS: &messaging.APNSConfig{
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					Alert: &messaging.ApsAlert{
						Title: title,
						Body:  body,
					},
					Sound: "default",
					Badge: func() *int { v := 1; return &v }(),
				},
			},
			Headers: map[string]string{
				"apns-priority": "10",
			},
		},
		Data: make(map[string]string),
	}

	// Add custom data
	if customData != nil {
		for k, v := range customData {
			message.Data[k] = fmt.Sprintf("%v", v)
		}
	}

	// Send message
	resp, err := f.client.Send(ctx, message)
	if err != nil {
		return nil, fmt.Errorf("send fcm topic message failed: %w", err)
	}

	log.Printf("Successfully sent FCM topic message to %s, message ID: %s", topic, resp)
	
	return &FCMResponse{
		SuccessCount: 1,
		FailureCount: 0,
	}, nil
}

// PushToMultipleTokens sends notifications to multiple device tokens.
func (f *FCMClient) PushToMultipleTokens(ctx context.Context, deviceTokens []string, title, body string, customData map[string]interface{}) (*FCMResponse, error) {
	if f.client == nil {
		return nil, fmt.Errorf("FCM client not initialized")
	}
	
	if len(deviceTokens) == 0 {
		return nil, fmt.Errorf("device tokens list is empty")
	}

	// Build messages
	var messages []*messaging.Message
	for _, token := range deviceTokens {
		if token == "" {
			continue
		}

		message := &messaging.Message{
			Token: token,
			Notification: &messaging.Notification{
				Title: title,
				Body:  body,
			},
			Android: &messaging.AndroidConfig{
				Notification: &messaging.AndroidNotification{
					Title: title,
					Body:  body,
					Sound: "default",
				},
				Priority: "high",
			},
			APNS: &messaging.APNSConfig{
				Payload: &messaging.APNSPayload{
					Aps: &messaging.Aps{
						Alert: &messaging.ApsAlert{
							Title: title,
							Body:  body,
						},
						Sound: "default",
						Badge: func() *int { v := 1; return &v }(),
					},
				},
				Headers: map[string]string{
					"apns-priority": "10",
				},
			},
			Data: make(map[string]string),
		}

		// Add custom data
		if customData != nil {
			for k, v := range customData {
				message.Data[k] = fmt.Sprintf("%v", v)
			}
		}

		messages = append(messages, message)
	}

	if len(messages) == 0 {
		return nil, fmt.Errorf("no valid device tokens")
	}

	// Send messages in batch
	batchResp, err := f.client.SendAll(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("send fcm batch messages failed: %w", err)
	}

	log.Printf("FCM batch send completed: %d success, %d failure", batchResp.SuccessCount, batchResp.FailureCount)
	
	return &FCMResponse{
		SuccessCount: batchResp.SuccessCount,
		FailureCount: batchResp.FailureCount,
		Responses:    batchResp.Responses,
	}, nil
}

// SubscribeToTopic subscribes device tokens to a topic.
func (f *FCMClient) SubscribeToTopic(ctx context.Context, deviceTokens []string, topic string) error {
	if f.client == nil {
		return fmt.Errorf("FCM client not initialized")
	}
	
	if len(deviceTokens) == 0 {
		return fmt.Errorf("device tokens list is empty")
	}

	if topic == "" {
		topic = f.defaultTopic
	}

	// Filter out empty tokens
	var validTokens []string
	for _, token := range deviceTokens {
		if token != "" {
			validTokens = append(validTokens, token)
		}
	}

	if len(validTokens) == 0 {
		return fmt.Errorf("no valid device tokens")
	}

	// Subscribe to topic
	resp, err := f.client.SubscribeToTopic(ctx, validTokens, topic)
	if err != nil {
		return fmt.Errorf("subscribe to topic failed: %w", err)
	}

	log.Printf("Successfully subscribed %d devices to topic %s", resp.SuccessCount, topic)
	return nil
}

// UnsubscribeFromTopic unsubscribes device tokens from a topic.
func (f *FCMClient) UnsubscribeFromTopic(ctx context.Context, deviceTokens []string, topic string) error {
	if f.client == nil {
		return fmt.Errorf("FCM client not initialized")
	}
	
	if len(deviceTokens) == 0 {
		return fmt.Errorf("device tokens list is empty")
	}

	if topic == "" {
		topic = f.defaultTopic
	}

	// Filter out empty tokens
	var validTokens []string
	for _, token := range deviceTokens {
		if token != "" {
			validTokens = append(validTokens, token)
		}
	}

	if len(validTokens) == 0 {
		return fmt.Errorf("no valid device tokens")
	}

	// Unsubscribe from topic
	resp, err := f.client.UnsubscribeFromTopic(ctx, validTokens, topic)
	if err != nil {
		return fmt.Errorf("unsubscribe from topic failed: %w", err)
	}

	log.Printf("Successfully unsubscribed %d devices from topic %s", resp.SuccessCount, topic)
	return nil
} 