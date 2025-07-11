package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"nopu/internal/config"
	"nopu/internal/push"
)

// TestPushServerBasic tests basic functionality of Push Server
func TestPushServerBasic(t *testing.T) {
	// Load test configuration
	cfg := &config.Config{
		PushServer: config.PushServerConfig{
			Port:                  8082,
			SubscriptionServerURL: "ws://localhost:8080",
			Apns: config.ApnsConfig{
				CertPath:     "cert/nopupush.p12",
				CertPassword: "123",
				BundleID:     "sh.nopu.app",
				Production:   false, // Use sandbox for testing
			},
			FCM: config.FCMConfig{
				ServiceAccountPath: "cert/firebase-credentials.json",
				ProjectID:          "test-project",
			},
		},
		Redis: config.RedisConfig{
			Addr:     "localhost:6379",
			Password: "",
			DB:       0,
		},
	}

	// Create server
	server, err := push.NewServer(cfg)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server in background
	go func() {
		if err := server.Start(ctx); err != nil {
			t.Errorf("Server failed: %v", err)
		}
	}()

	// Wait for server to start
	time.Sleep(2 * time.Second)

	// Test health check endpoint
	t.Run("Health Check", func(t *testing.T) {
		resp, err := http.Get("http://localhost:8082/health")
		if err != nil {
			t.Fatalf("Failed to get health check: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		var healthInfo map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&healthInfo); err != nil {
			t.Fatalf("Failed to decode health info: %v", err)
		}

		// Check required fields
		if healthInfo["status"] != "ok" {
			t.Errorf("Expected status 'ok', got %v", healthInfo["status"])
		}
	})

	// Test metrics endpoint
	t.Run("Metrics", func(t *testing.T) {
		resp, err := http.Get("http://localhost:8082/metrics")
		if err != nil {
			t.Fatalf("Failed to get metrics: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}
	})

	// Cleanup
	cancel()
	server.Shutdown()
}

// TestPushServerConfig tests configuration loading
func TestPushServerConfig(t *testing.T) {
	cfg, err := config.LoadPushServerConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Check required fields
	if cfg.PushServer.SubscriptionServerURL == "" {
		t.Error("Subscription server URL is required")
	}
	if cfg.PushServer.Port == 0 {
		t.Error("Port should be configured")
	}
}

// TestPushServerAPNs tests APNs integration
func TestPushServerAPNs(t *testing.T) {
	cfg := &config.Config{
		PushServer: config.PushServerConfig{
			Port:                  8083,
			SubscriptionServerURL: "ws://localhost:8080",
			Apns: config.ApnsConfig{
				CertPath:     "cert/nopupush.p12",
				CertPassword: "123",
				BundleID:     "sh.nopu.app",
				Production:   false, // Use sandbox for testing
			},
		},
		Redis: config.RedisConfig{
			Addr:     "localhost:6379",
			Password: "",
			DB:       0,
		},
	}

	_, err := push.NewServer(cfg)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Test APNs client creation
	// Note: APNs client is created during server initialization
	// In a real test, you would verify the client was created successfully
	// and test sending notifications to a test device
	t.Run("APNs Client Creation", func(t *testing.T) {
		// The server should have been created successfully
		// APNs client creation is handled during server initialization
		// If cert files don't exist, the server will still start but log warnings
	})
}

// TestPushServerFCM tests FCM integration
func TestPushServerFCM(t *testing.T) {
	cfg := &config.Config{
		PushServer: config.PushServerConfig{
			Port:                  8084,
			SubscriptionServerURL: "ws://localhost:8080",
			FCM: config.FCMConfig{
				ServiceAccountPath: "cert/firebase-credentials.json",
				ProjectID:          "test-project",
			},
		},
		Redis: config.RedisConfig{
			Addr:     "localhost:6379",
			Password: "",
			DB:       0,
		},
	}

	_, err := push.NewServer(cfg)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Test FCM client creation
	// Note: FCM client is created during server initialization
	// In a real test, you would verify the client was created successfully
	// and test sending notifications to a test device
	t.Run("FCM Client Creation", func(t *testing.T) {
		// The server should have been created successfully
		// FCM client creation is handled during server initialization
		// If credentials files don't exist, the server will still start but log warnings
	})
}

// TestPushServerEndToEnd end-to-end testing
func TestPushServerEndToEnd(t *testing.T) {
	// This test would require:
	// 1. A running Redis instance
	// 2. A running Subscription Server
	// 3. Valid APNs/FCM credentials
	// 4. Test devices with valid tokens
	// 5. Verification of notification delivery

	t.Skip("End-to-end test requires external dependencies")
}

// BenchmarkPushServer performance testing
func BenchmarkPushServer(b *testing.B) {
	cfg := &config.Config{
		PushServer: config.PushServerConfig{
			Port:                  8085,
			SubscriptionServerURL: "ws://localhost:8080",
		},
		Redis: config.RedisConfig{
			Addr:     "localhost:6379",
			Password: "",
			DB:       0,
		},
	}

	server, err := push.NewServer(cfg)
	if err != nil {
		b.Fatalf("Failed to create server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := server.Start(ctx); err != nil {
			b.Errorf("Server failed: %v", err)
		}
	}()

	time.Sleep(1 * time.Second)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := http.Get("http://localhost:8085/health")
		if err != nil {
			b.Fatalf("Request failed: %v", err)
		}
		resp.Body.Close()
	}

	cancel()
	server.Shutdown()
}
