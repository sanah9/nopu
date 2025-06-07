package config

import (
	"os"
	"strconv"
	"strings"
)

// Config application configuration
type Config struct {
	Redis              RedisConfig
	SubscriptionServer SubscriptionServerConfig
	Listener           ListenerConfig
}

// RedisConfig Redis configuration
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

// SubscriptionServerConfig subscription server configuration
type SubscriptionServerConfig struct {
	Port             int
	RelayName        string
	RelayDescription string
	Domain           string
}

// ListenerConfig listener server configuration
type ListenerConfig struct {
	Relays    []string
	Kinds     []int
	BatchSize int
}

// Load loads configuration
func Load() (*Config, error) {
	cfg := &Config{
		Redis: RedisConfig{
			Addr:     getEnv("REDIS_ADDR", "localhost:6379"),
			Password: getEnv("REDIS_PASSWORD", ""),
			DB:       getEnvInt("REDIS_DB", 0),
		},
		SubscriptionServer: SubscriptionServerConfig{
			Port:             getEnvInt("SUBSCRIPTION_SERVER_PORT", 8080),
			RelayName:        getEnv("RELAY_NAME", "Nopu Relay"),
			RelayDescription: getEnv("RELAY_DESCRIPTION", "Subscription-based message push service"),
			Domain:           getEnv("DOMAIN", "localhost:8080"),
		},
		Listener: ListenerConfig{
			Relays:    getEnvSlice("LISTEN_RELAYS", []string{"wss://relay.damus.io", "wss://nos.lol"}),
			Kinds:     getEnvIntSlice("LISTEN_KINDS", []int{1, 7}),
			BatchSize: getEnvInt("BATCH_SIZE", 100),
		},
	}

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
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
