package push

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/certificate"
	"github.com/sideshow/apns2/payload"

	"nopu/internal/config"
)

// APNSClient wraps an apns2 client for sending push notifications.
// Uses certificate-based (.p12 or .pem) authentication.
// Thread-safe and reusable.
//
// Example:
//   cfg, _ := config.Load()
//   apnsCli, err := NewAPNSClient(cfg.PushServer.Apns)
//   if err != nil { /* handle error */ }
//   resp, err := apnsCli.Push(ctx, deviceToken, "Title", "Body", nil)

type APNSClient struct {
	client *apns2.Client
	topic  string
}

// NewAPNSClient creates a new APNSClient with the given configuration.
func NewAPNSClient(cfg config.ApnsConfig) (*APNSClient, error) {
	if cfg.CertPath == "" || cfg.BundleID == "" {
		return nil, fmt.Errorf("incomplete apns configuration (require cert_path & bundle_id)")
	}

	cert, err := certificate.FromP12File(cfg.CertPath, cfg.CertPassword)
	if err != nil {
		return nil, fmt.Errorf("load apns certificate failed: %w", err)
	}

	cli := apns2.NewClient(cert)
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
	return a.PushWithSilent(ctx, deviceToken, alertTitle, alertBody, customData, false)
}

// PushWithSilent sends a notification with silent push option.
// customData can be nil when no extra fields are required.
func (a *APNSClient) PushWithSilent(ctx context.Context, deviceToken, alertTitle, alertBody string, customData map[string]interface{}, silent bool) (*apns2.Response, error) {
	if deviceToken == "" {
		return nil, fmt.Errorf("device token is empty")
	}

	var pld *payload.Payload
	if silent {
		// Silent push - no alert, just content-available
		pld = payload.NewPayload().ContentAvailable()
	} else {
		// Regular push with alert
		pld = payload.NewPayload().AlertTitle(alertTitle).AlertBody(alertBody).Sound("default").ContentAvailable()

		// extract badge if provided (only for regular push)
		if badgeVal, ok := customData["badge"]; ok {
			switch b := badgeVal.(type) {
			case int:
				pld.Badge(b)
			case int32:
				pld.Badge(int(b))
			case int64:
				pld.Badge(int(b))
			case float64:
				pld.Badge(int(b))
			}
		}

		// Add custom data (only for regular push)
		for k, v := range customData {
			if k != "badge" { // Skip badge as it's already handled
				pld.Custom(k, v)
			}
		}
	}

	notif := &apns2.Notification{
		DeviceToken: deviceToken,
		Topic:       a.topic,
		Payload:     pld,
		Expiration:  time.Now().Add(24 * time.Hour),
		Priority:    apns2.PriorityHigh,
		PushType:    apns2.PushTypeAlert,
	}

	// Set priority to low and push type to background for silent push
	if silent {
		notif.Priority = apns2.PriorityLow
		notif.PushType = apns2.PushTypeBackground
	}

	// Log the final notification payload
	payloadBytes, _ := json.Marshal(notif.Payload)
	log.Printf("APNS notification payload: %s", string(payloadBytes))

	return a.client.Push(notif)
}
