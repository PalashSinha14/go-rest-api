// Package config loads runtime settings from the environment, applying safe
// defaults for local development and refusing to start on unsafe production values.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds every tunable the service needs. It is read once at startup and
// then treated as immutable.
type Config struct {
	Env             string
	Port            string
	MongoURI        string
	MongoDatabase   string
	JWTSecret       string
	JWTTTL          time.Duration
	LoanPeriod      time.Duration
	MaxActiveLoans  int
	ShutdownTimeout time.Duration
	LogLevel        string
}

// devJWTSecret is the fallback signing key used only outside production. It is a
// known constant, so Load refuses to use it when Env is "production".
const devJWTSecret = "dev-only-insecure-secret-change-me"

// Load reads configuration from the environment. It returns an error rather than
// exiting so main can log the failure through the configured logger.
func Load() (*Config, error) {
	cfg := &Config{
		Env:             env("APP_ENV", "development"),
		Port:            env("PORT", "8080"),
		MongoURI:        env("MONGO_URI", "mongodb://localhost:27017"),
		MongoDatabase:   env("MONGO_DATABASE", "library"),
		JWTSecret:       env("JWT_SECRET", devJWTSecret),
		JWTTTL:          envDuration("JWT_TTL", 24*time.Hour),
		LoanPeriod:      envDuration("LOAN_PERIOD", 14*24*time.Hour),
		MaxActiveLoans:  envInt("MAX_ACTIVE_LOANS", 5),
		ShutdownTimeout: envDuration("SHUTDOWN_TIMEOUT", 10*time.Second),
		LogLevel:        env("LOG_LEVEL", "info"),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// IsProduction reports whether the service is running with production settings.
func (c *Config) IsProduction() bool { return c.Env == "production" }

func (c *Config) validate() error {
	if c.JWTSecret == devJWTSecret && c.IsProduction() {
		return fmt.Errorf("JWT_SECRET must be set explicitly when APP_ENV=production")
	}
	if len(c.JWTSecret) < 16 {
		return fmt.Errorf("JWT_SECRET must be at least 16 characters, got %d", len(c.JWTSecret))
	}
	if c.MaxActiveLoans < 1 {
		return fmt.Errorf("MAX_ACTIVE_LOANS must be at least 1, got %d", c.MaxActiveLoans)
	}
	if c.LoanPeriod <= 0 {
		return fmt.Errorf("LOAN_PERIOD must be positive, got %s", c.LoanPeriod)
	}
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return v
	}
	return fallback
}
