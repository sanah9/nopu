package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config application configuration
type Config struct {
	Redis              RedisConfig              `yaml:"redis"`
	SubscriptionServer SubscriptionServerConfig `yaml:"subscription_server"`
	PushServer         PushServerConfig         `yaml:"push_server"`
}

// RedisConfig Redis configuration
type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
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
}

// PushServerConfig push server configuration
type PushServerConfig struct {
	Port                  int        `yaml:"port"`
	SubscriptionServerURL string     `yaml:"subscription_server_url"` // URL to connect to subscription server
	WorkerCount           int        `yaml:"worker_count"`            // Number of worker goroutines
	BatchSize             int        `yaml:"batch_size"`              // Batch size for processing messages
	Apns                  ApnsConfig `yaml:"apns"`                    // APNs push configuration
	FCM                   FCMConfig  `yaml:"fcm"`                     // FCM push configuration
}

// ListenerConfig listener server configuration
type ListenerConfig struct {
	Relays         []string      `yaml:"relays"`
	Kinds          []int         `yaml:"kinds"`
	BatchSize      int           `yaml:"batch_size"`
	ReconnectDelay time.Duration `yaml:"reconnect_delay"`
	MaxRetries     int           `yaml:"max_retries"`
}

// ApnsConfig contains APNs push configuration
type ApnsConfig struct {
	BundleID     string `yaml:"bundle_id"`     // Application bundle identifier (Topic)
	Production   bool   `yaml:"production"`    // Use production environment, false for sandbox
	CertPath     string `yaml:"cert_path"`     // .p12 or .pem APNs certificate path (required)
	CertPassword string `yaml:"cert_password"` // Certificate password, empty if not protected
}

// FCMConfig contains Firebase Cloud Messaging configuration
type FCMConfig struct {
	ProjectID          string `yaml:"project_id"`           // Firebase project ID
	ServiceAccountPath string `yaml:"service_account_path"` // Path to service account JSON file
	DefaultTopic       string `yaml:"default_topic"`        // Default FCM topic
}

// Load loads configuration
func Load() (*Config, error) {
	// Default configuration
	cfg := &Config{
		Redis: RedisConfig{
			Addr:     "localhost:6379",
			Password: "",
			DB:       0,
		},
		SubscriptionServer: SubscriptionServerConfig{
			Port:             8080,
			RelayName:        "Nopu Subscription Server",
			RelayDescription: "Self-hostable subscription server for push notifications",
			Domain:           "localhost:8080",
			RelayPrivateKey:  "",
			MaxSubscriptions: 100,
			Listener: ListenerConfig{
				Relays:         []string{"wss://relay.damus.io", "wss://relay.0xchat.com"},
				Kinds:          []int{1, 7},
				BatchSize:      100,
				ReconnectDelay: 5 * time.Second,
				MaxRetries:     0,
			},
		},
		PushServer: PushServerConfig{
			Port:                  8081,
			SubscriptionServerURL: "ws://localhost:8080",
			WorkerCount:           10,
			BatchSize:             100,
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
		Redis:              cfg.Redis,
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
		Redis:      cfg.Redis,
		PushServer: cfg.PushServer,
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
	// Redis configuration
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		cfg.Redis.Addr = addr
	}
	if password := os.Getenv("REDIS_PASSWORD"); password != "" {
		cfg.Redis.Password = password
	}
	if db := getEnvInt("REDIS_DB", cfg.Redis.DB); db != cfg.Redis.DB {
		cfg.Redis.DB = db
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
	if pushPort := getEnvInt("PUSH_SERVER_PORT", cfg.PushServer.Port); pushPort != cfg.PushServer.Port {
		cfg.PushServer.Port = pushPort
	}
	if subscriptionServerURL := os.Getenv("SUBSCRIPTION_SERVER_URL"); subscriptionServerURL != "" {
		cfg.PushServer.SubscriptionServerURL = subscriptionServerURL
	}
	if workerCount := getEnvInt("WORKER_COUNT", cfg.PushServer.WorkerCount); workerCount != cfg.PushServer.WorkerCount {
		cfg.PushServer.WorkerCount = workerCount
	}
	if batchSize := getEnvInt("BATCH_SIZE", cfg.PushServer.BatchSize); batchSize != cfg.PushServer.BatchSize {
		cfg.PushServer.BatchSize = batchSize
	}

	// Listener configuration
	if relays := getEnvSlice("LISTEN_RELAYS", nil); relays != nil {
		cfg.SubscriptionServer.Listener.Relays = relays
	}
	if kinds := getEnvIntSlice("LISTEN_KINDS", nil); kinds != nil {
		cfg.SubscriptionServer.Listener.Kinds = kinds
	}
	if batchSize := getEnvInt("BATCH_SIZE", cfg.SubscriptionServer.Listener.BatchSize); batchSize != cfg.SubscriptionServer.Listener.BatchSize {
		cfg.SubscriptionServer.Listener.BatchSize = batchSize
	}

	// Override APNS configuration with environment variables
	if bundleID := os.Getenv("APNS_BUNDLE_ID"); bundleID != "" {
		cfg.PushServer.Apns.BundleID = bundleID
	}
	if productionStr := os.Getenv("APNS_PRODUCTION"); productionStr != "" {
		if prod, err := strconv.ParseBool(productionStr); err == nil {
			cfg.PushServer.Apns.Production = prod
		}
	}
	if certPath := os.Getenv("APNS_CERT_PATH"); certPath != "" {
		cfg.PushServer.Apns.CertPath = certPath
	}
	if certPwd := os.Getenv("APNS_CERT_PASSWORD"); certPwd != "" {
		cfg.PushServer.Apns.CertPassword = certPwd
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

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvSlice(key string, defaultValue []string) []string {
	if value := os.Getenv(key); value != "" {
		return strings.Split(value, ",")
	}
	return defaultValue
}

func getEnvIntSlice(key string, defaultValue []int) []int {
	if value := os.Getenv(key); value != "" {
		parts := strings.Split(value, ",")
		result := make([]int, 0, len(parts))
		for _, part := range parts {
			if intValue, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				result = append(result, intValue)
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return defaultValue
}
