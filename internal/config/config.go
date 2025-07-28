package config

import (
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Config application configuration
type Config struct {
	MemoryQueue        MemoryQueueConfig        `yaml:"memory_queue"`
	SubscriptionServer SubscriptionServerConfig `yaml:"subscription_server"`
	PushServer         PushServerConfig         `yaml:"push_server"`
}

// MemoryQueueConfig memory queue configuration
type MemoryQueueConfig struct {
	MaxSize    int           `yaml:"max_size"`    // Maximum number of events in queue
	DedupeTTL  time.Duration `yaml:"dedupe_ttl"`  // TTL for deduplication cache
	BufferSize int           `yaml:"buffer_size"` // Buffer size for consumers
}

// SubscriptionServerConfig subscription server configuration
type SubscriptionServerConfig struct {
	Port             int    `yaml:"port"`
	RelayName        string `yaml:"relay_name"`
	RelayDescription string `yaml:"relay_description"`
	Domain           string `yaml:"domain"`
	RelayPrivateKey  string `yaml:"relay_private_key"`
	// New fields for subscription management
	MaxSubscriptions int            `yaml:"max_subscriptions"` // Maximum subscriptions per client
	Listener         ListenerConfig `yaml:"listener"`          // Listener configuration for Nostr relays
	PushServerURL    string         `yaml:"push_server_url"`   // URL to push server for sending notifications
	// Event 20284 access control configuration
	Event20284Policy Event20284Policy `yaml:"event_20284_policy"` // Access control policy for 20284 events
}

// PushServerConfig push server configuration
type PushServerConfig struct {
	Port        int        `yaml:"port"`
	WorkerCount int        `yaml:"worker_count"` // Number of worker goroutines
	BatchSize   int        `yaml:"batch_size"`   // Batch size for processing messages
	Apns        ApnsConfig `yaml:"apns"`         // APNs push configuration
	FCM         FCMConfig  `yaml:"fcm"`          // FCM push configuration
}

// ListenerConfig listener server configuration
type ListenerConfig struct {
	Relays         []string      `yaml:"relays"`          // List of Nostr relays to listen to
	Kinds          []int         `yaml:"kinds"`           // Event kinds to listen for
	BatchSize      int           `yaml:"batch_size"`      // Batch size for processing
	ReconnectDelay time.Duration `yaml:"reconnect_delay"` // Reconnection delay
	MaxRetries     int           `yaml:"max_retries"`     // Maximum retry attempts
}

// ApnsConfig APNs push configuration
type ApnsConfig struct {
	CertPath     string `yaml:"cert_path"`     // Path to APNs certificate (.p12)
	CertPassword string `yaml:"cert_password"` // Certificate password
	BundleID     string `yaml:"bundle_id"`     // App bundle ID
	Production   bool   `yaml:"production"`    // Use production environment
}

// FCMConfig FCM push configuration
type FCMConfig struct {
	ProjectID          string `yaml:"project_id"`           // Firebase project ID
	ServiceAccountPath string `yaml:"service_account_path"` // Path to service account JSON
	DefaultTopic       string `yaml:"default_topic"`        // Default FCM topic
}

// Event20284Policy defines access control policy for 20284 events
type Event20284Policy struct {
	Whitelist []string `yaml:"whitelist"`  // List of allowed pubkeys (empty means no restriction)
	RejectAll bool     `yaml:"reject_all"` // If true, reject all 20284 events regardless of whitelist
}

// Load loads configuration from file and environment variables
func Load() (*Config, error) {
	// Default configuration
	cfg := &Config{
		MemoryQueue: MemoryQueueConfig{
			MaxSize:    10000,          // 10k events max
			DedupeTTL:  24 * time.Hour, // 24 hours TTL
			BufferSize: 1000,           // 1k buffer per consumer
		},
		SubscriptionServer: SubscriptionServerConfig{
			Port:             8080,
			RelayName:        "Nopu Subscription Server",
			RelayDescription: "Self-hostable subscription server for push notifications",
			Domain:           "localhost:8080",
			RelayPrivateKey:  "",
			MaxSubscriptions: 100,
			PushServerURL:    "http://localhost:8081",
			Event20284Policy: Event20284Policy{
				Whitelist: nil, // No restriction by default
				RejectAll: false,
			},
			Listener: ListenerConfig{
				Relays:         []string{"wss://relay.damus.io", "wss://relay.0xchat.com"},
				Kinds:          []int{1, 7},
				BatchSize:      100,
				ReconnectDelay: 5 * time.Second,
				MaxRetries:     0,
			},
		},
		PushServer: PushServerConfig{
			Port:        8081,
			WorkerCount: 10,
			BatchSize:   100,
			Apns: ApnsConfig{
				BundleID:     "",
				Production:   false,
				CertPath:     "",
				CertPassword: "",
			},
			FCM: FCMConfig{
				ProjectID:          "",
				ServiceAccountPath: "",
				DefaultTopic:       "nopu_notifications",
			},
		},
	}

	// Try to load configuration from YAML file
	if err := loadFromYAML(cfg); err != nil {
		// If file does not exist, it's not an error, continue with default configuration
		if !os.IsNotExist(err) {
			return nil, err
		}
	}

	// Environment variables override configuration (highest priority)
	overrideWithEnv(cfg)

	return cfg, nil
}

// LoadSubscriptionServerConfig loads configuration for subscription server only
func LoadSubscriptionServerConfig() (*Config, error) {
	cfg, err := Load()
	if err != nil {
		return nil, err
	}

	// Return only the parts needed by subscription server
	return &Config{
		MemoryQueue:        cfg.MemoryQueue,
		SubscriptionServer: cfg.SubscriptionServer,
	}, nil
}

// LoadPushServerConfig loads configuration for push server only
func LoadPushServerConfig() (*Config, error) {
	cfg, err := Load()
	if err != nil {
		return nil, err
	}

	// Return only the parts needed by push server
	return &Config{
		MemoryQueue: cfg.MemoryQueue,
		PushServer:  cfg.PushServer,
	}, nil
}

// loadFromYAML loads configuration from YAML file
func loadFromYAML(cfg *Config) error {
	// Search for configuration files
	configPaths := []string{
		"config.yaml",
		"config.yml",
		"./config.yaml",
		"./config.yml",
	}

	var configData []byte
	var err error

	for _, path := range configPaths {
		if configData, err = os.ReadFile(path); err == nil {
			break
		}
	}

	if err != nil {
		return err
	}

	return yaml.Unmarshal(configData, cfg)
}

// overrideWithEnv overrides configuration with environment variables
func overrideWithEnv(cfg *Config) {
	// Memory queue configuration
	if maxSize := getEnvInt("MEMORY_QUEUE_MAX_SIZE", cfg.MemoryQueue.MaxSize); maxSize != cfg.MemoryQueue.MaxSize {
		cfg.MemoryQueue.MaxSize = maxSize
	}
	if bufferSize := getEnvInt("MEMORY_QUEUE_BUFFER_SIZE", cfg.MemoryQueue.BufferSize); bufferSize != cfg.MemoryQueue.BufferSize {
		cfg.MemoryQueue.BufferSize = bufferSize
	}

	// Subscription server configuration
	if port := getEnvInt("SUBSCRIPTION_SERVER_PORT", cfg.SubscriptionServer.Port); port != cfg.SubscriptionServer.Port {
		cfg.SubscriptionServer.Port = port
	}
	if name := os.Getenv("RELAY_NAME"); name != "" {
		cfg.SubscriptionServer.RelayName = name
	}
	if desc := os.Getenv("RELAY_DESCRIPTION"); desc != "" {
		cfg.SubscriptionServer.RelayDescription = desc
	}
	if domain := os.Getenv("DOMAIN"); domain != "" {
		cfg.SubscriptionServer.Domain = domain
	}
	if relayPrivateKey := os.Getenv("RELAY_PRIVATE_KEY"); relayPrivateKey != "" {
		cfg.SubscriptionServer.RelayPrivateKey = relayPrivateKey
	}
	if maxSubscriptions := getEnvInt("MAX_SUBSCRIPTIONS", cfg.SubscriptionServer.MaxSubscriptions); maxSubscriptions != cfg.SubscriptionServer.MaxSubscriptions {
		cfg.SubscriptionServer.MaxSubscriptions = maxSubscriptions
	}

	// Push server configuration
	if port := getEnvInt("PUSH_SERVER_PORT", cfg.PushServer.Port); port != cfg.PushServer.Port {
		cfg.PushServer.Port = port
	}
	// Removed subscription server URL as push server no longer connects to subscription server
	if workerCount := getEnvInt("WORKER_COUNT", cfg.PushServer.WorkerCount); workerCount != cfg.PushServer.WorkerCount {
		cfg.PushServer.WorkerCount = workerCount
	}
	if batchSize := getEnvInt("BATCH_SIZE", cfg.PushServer.BatchSize); batchSize != cfg.PushServer.BatchSize {
		cfg.PushServer.BatchSize = batchSize
	}

	// APNs configuration
	if certPath := os.Getenv("APNS_CERT_PATH"); certPath != "" {
		cfg.PushServer.Apns.CertPath = certPath
	}
	if certPassword := os.Getenv("APNS_CERT_PASSWORD"); certPassword != "" {
		cfg.PushServer.Apns.CertPassword = certPassword
	}
	if bundleID := os.Getenv("APNS_BUNDLE_ID"); bundleID != "" {
		cfg.PushServer.Apns.BundleID = bundleID
	}
	if production := os.Getenv("APNS_PRODUCTION"); production != "" {
		cfg.PushServer.Apns.Production = production == "true"
	}

	// FCM configuration
	if projectID := os.Getenv("FCM_PROJECT_ID"); projectID != "" {
		cfg.PushServer.FCM.ProjectID = projectID
	}
	if serviceAccountPath := os.Getenv("FCM_SERVICE_ACCOUNT_PATH"); serviceAccountPath != "" {
		cfg.PushServer.FCM.ServiceAccountPath = serviceAccountPath
	}
	if defaultTopic := os.Getenv("FCM_DEFAULT_TOPIC"); defaultTopic != "" {
		cfg.PushServer.FCM.DefaultTopic = defaultTopic
	}
}

// getEnvInt gets an integer environment variable
func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}
