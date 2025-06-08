package config

import (
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config application configuration
type Config struct {
	Redis              RedisConfig              `yaml:"redis"`
	SubscriptionServer SubscriptionServerConfig `yaml:"subscription_server"`
	Listener           ListenerConfig           `yaml:"listener"`
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
}

// ListenerConfig listener server configuration
type ListenerConfig struct {
	Relays    []string `yaml:"relays"`
	Kinds     []int    `yaml:"kinds"`
	BatchSize int      `yaml:"batch_size"`
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
			RelayName:        "Nopu Relay",
			RelayDescription: "Subscription-based message push service",
			Domain:           "localhost:8080",
		},
		Listener: ListenerConfig{
			Relays:    []string{"wss://relay.damus.io", "wss://relay.0xchat.com"},
			Kinds:     []int{1, 7},
			BatchSize: 100,
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

	// Listener configuration
	if relays := getEnvSlice("LISTEN_RELAYS", nil); relays != nil {
		cfg.Listener.Relays = relays
	}
	if kinds := getEnvIntSlice("LISTEN_KINDS", nil); kinds != nil {
		cfg.Listener.Kinds = kinds
	}
	if batchSize := getEnvInt("BATCH_SIZE", cfg.Listener.BatchSize); batchSize != cfg.Listener.BatchSize {
		cfg.Listener.BatchSize = batchSize
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
