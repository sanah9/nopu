package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"nopu/internal/config"
	"nopu/internal/subscription"
)

// TestSubscriptionServerBasic tests basic functionality of Subscription Server
func TestSubscriptionServerBasic(t *testing.T) {
	// Load test configuration
	cfg := &config.Config{
		SubscriptionServer: config.SubscriptionServerConfig{
			Port:             8080,
			RelayName:        "Test Relay",
			RelayDescription: "Test subscription server",
			Domain:           "localhost:8080",
			RelayPrivateKey:  "e81e9696fb2cfef97bd9daff0200e1bf544dd2e1b5ec65127fdedebb59ab6680",
		},
		Redis: config.RedisConfig{
			Addr:     "localhost:6379",
			Password: "",
			DB:       0,
		},
	}

	// Create server
	server, err := subscription.NewServer(cfg)
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

	// Test NIP-11 (Relay Information)
	t.Run("NIP-11 Relay Information", func(t *testing.T) {
		resp, err := http.Get("http://localhost:8080")
		if err != nil {
			t.Fatalf("Failed to get relay info: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		var relayInfo map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&relayInfo); err != nil {
			t.Fatalf("Failed to decode relay info: %v", err)
		}

		// Check required fields
		if relayInfo["name"] != "Test Relay" {
			t.Errorf("Expected name 'Test Relay', got %v", relayInfo["name"])
		}
		if relayInfo["description"] != "Test subscription server" {
			t.Errorf("Expected description 'Test subscription server', got %v", relayInfo["description"])
		}
	})

	// Test WebSocket connection
	t.Run("WebSocket Connection", func(t *testing.T) {
		// This would require a WebSocket client library
		// For now, we'll just test that the endpoint exists
		resp, err := http.Get("http://localhost:8080")
		if err != nil {
			t.Fatalf("Failed to connect to WebSocket endpoint: %v", err)
		}
		defer resp.Body.Close()

		// WebSocket upgrade should return 101 Switching Protocols
		// But for HTTP GET, it should return 200 for NIP-11
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}
	})

	// Cleanup
	cancel()
	server.Shutdown()
}

// TestSubscriptionServerConfig tests configuration loading
func TestSubscriptionServerConfig(t *testing.T) {
	cfg, err := config.LoadSubscriptionServerConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Check required fields
	if cfg.SubscriptionServer.RelayPrivateKey == "" {
		t.Error("Relay private key is required")
	}
	if cfg.SubscriptionServer.Port == 0 {
		t.Error("Port should be configured")
	}
}

// TestSubscriptionServerEndToEnd end-to-end testing
func TestSubscriptionServerEndToEnd(t *testing.T) {
	// This test would require:
	// 1. A running Redis instance
	// 2. A WebSocket client
	// 3. A test client that can send Nostr events
	// 4. Verification of event processing and storage

	t.Skip("End-to-end test requires external dependencies")
}

// BenchmarkSubscriptionServer performance testing
func BenchmarkSubscriptionServer(b *testing.B) {
	cfg := &config.Config{
		SubscriptionServer: config.SubscriptionServerConfig{
			Port:             8081,
			RelayName:        "Benchmark Relay",
			RelayDescription: "Benchmark test",
			Domain:           "localhost:8081",
			RelayPrivateKey:  "e81e9696fb2cfef97bd9daff0200e1bf544dd2e1b5ec65127fdedebb59ab6680",
		},
		Redis: config.RedisConfig{
			Addr:     "localhost:6379",
			Password: "",
			DB:       0,
		},
	}

	server, err := subscription.NewServer(cfg)
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
		resp, err := http.Get("http://localhost:8081")
		if err != nil {
			b.Fatalf("Request failed: %v", err)
		}
		resp.Body.Close()
	}

	cancel()
	server.Shutdown()
}
