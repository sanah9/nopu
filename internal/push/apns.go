package push

import (
	"context"
	"fmt"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/payload"
	"github.com/sideshow/apns2/token"

	"nopu/internal/config"
)

// APNSClient wraps an apns2 client for sending push notifications.
// Uses token-based (.p8) authentication (JWT).
// Thread-safe and reusable.
//
// Example:
//   cfg, _ := config.Load()
//   apnsCli, err := NewAPNSClient(cfg.Apns)
//   if err != nil { /* handle error */ }
//   resp, err := apnsCli.Push(ctx, deviceToken, "Title", "Body", nil)

type APNSClient struct {
	client *apns2.Client
	topic  string
}

// NewAPNSClient creates a new APNSClient with the given configuration.
func NewAPNSClient(cfg config.ApnsConfig) (*APNSClient, error) {
	if cfg.KeyPath == "" || cfg.KeyID == "" || cfg.TeamID == "" || cfg.BundleID == "" {
		return nil, fmt.Errorf("incomplete apns configuration")
	}

	authKey, err := token.AuthKeyFromFile(cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("load apns auth key failed: %w", err)
	}

	tok := &token.Token{
		AuthKey: authKey,
		KeyID:   cfg.KeyID,
		TeamID:  cfg.TeamID,
	}

	// token expiry handled internally, token.GenerateIfExpired()

	cli := apns2.NewTokenClient(tok)
	if cfg.Production {
		cli = cli.Production()
	} else {
		cli = cli.Development()
	}

	return &APNSClient{
		client: cli,
		topic:  cfg.BundleID,
	}, nil
}

// Push sends a notification.
// customData can be nil when no extra fields are required.
func (a *APNSClient) Push(ctx context.Context, deviceToken, alertTitle, alertBody string, customData map[string]interface{}) (*apns2.Response, error) {
	if deviceToken == "" {
		return nil, fmt.Errorf("device token is empty")
	}

	pld := payload.NewPayload().AlertTitle(alertTitle).AlertBody(alertBody).Sound("default")
	for k, v := range customData {
		pld.Custom(k, v)
	}

	notif := &apns2.Notification{
		DeviceToken: deviceToken,
		Topic:       a.topic,
		Payload:     pld,
		Expiration:  time.Now().Add(24 * time.Hour),
		Priority:    apns2.PriorityHigh,
	}

	resp, err := a.client.PushWithContext(ctx, notif)
	if err != nil {
		return resp, err
	}
	return resp, nil
}
