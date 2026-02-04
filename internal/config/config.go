package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// GetEnvInt reads an integer environment variable with a default.
func GetEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}

// GetEnvDuration reads an integer env var and returns it as seconds.
func GetEnvDuration(key string, defaultSeconds int) time.Duration {
	return time.Duration(GetEnvInt(key, defaultSeconds)) * time.Second
}

// GetEnvBool returns true if the env var equals "true".
func GetEnvBool(key string) bool {
	return os.Getenv(key) == "true"
}

// GetEnvOrDefault returns the env var value or a default.
func GetEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// GetSecret reads a secret from a file path (Docker/K8s secrets) with env fallback.
func GetSecret(envVar, secretPath string) string {
	if data, err := os.ReadFile(secretPath); err == nil {
		return strings.TrimSpace(string(data))
	}
	return os.Getenv(envVar)
}

// IsDebugMode returns true if _LOG_LEVEL is "2" or "debug".
func IsDebugMode() bool {
	val := os.Getenv("_LOG_LEVEL")
	return val == "2" || val == "debug"
}
