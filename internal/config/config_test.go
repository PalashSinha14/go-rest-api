package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.MongoDatabase != "library" {
		t.Errorf("MongoDatabase = %q, want library", cfg.MongoDatabase)
	}
	if cfg.LoanPeriod != 14*24*time.Hour {
		t.Errorf("LoanPeriod = %v, want 336h", cfg.LoanPeriod)
	}
	if cfg.MaxActiveLoans != 5 {
		t.Errorf("MaxActiveLoans = %d, want 5", cfg.MaxActiveLoans)
	}
	if cfg.IsProduction() {
		t.Error("IsProduction() = true for the default environment")
	}
}

// The dev secret is a known constant published in this repository, so shipping it
// to production would let anyone mint a librarian token. Startup must refuse.
func TestLoadRejectsDevSecretInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted the default dev secret with APP_ENV=production")
	}
	if !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Errorf("error = %q, want it to name JWT_SECRET", err)
	}
}

func TestLoadAcceptsExplicitSecretInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", "a-real-secret-of-adequate-length")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.IsProduction() {
		t.Error("IsProduction() = false with APP_ENV=production")
	}
}

func TestLoadRejectsShortSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "too-short")

	if _, err := Load(); err == nil {
		t.Error("Load() accepted a secret below the minimum length")
	}
}

func TestLoadRejectsUnusablePolicy(t *testing.T) {
	tests := []struct {
		name string
		key  string
		val  string
	}{
		{"zero loan quota", "MAX_ACTIVE_LOANS", "0"},
		{"negative loan quota", "MAX_ACTIVE_LOANS", "-1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.val)
			if _, err := Load(); err == nil {
				t.Errorf("Load() accepted %s=%s", tc.key, tc.val)
			}
		})
	}
}

// Malformed durations fall back to the default rather than failing startup, so a
// typo in one optional variable cannot take the service down.
func TestLoadFallsBackOnMalformedDuration(t *testing.T) {
	t.Setenv("JWT_TTL", "not-a-duration")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.JWTTTL != 24*time.Hour {
		t.Errorf("JWTTTL = %v, want the 24h default", cfg.JWTTTL)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("MONGO_DATABASE", "library_test")
	t.Setenv("LOAN_PERIOD", "72h")
	t.Setenv("MAX_ACTIVE_LOANS", "2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Port != "9090" {
		t.Errorf("Port = %q, want 9090", cfg.Port)
	}
	if cfg.MongoDatabase != "library_test" {
		t.Errorf("MongoDatabase = %q, want library_test", cfg.MongoDatabase)
	}
	if cfg.LoanPeriod != 72*time.Hour {
		t.Errorf("LoanPeriod = %v, want 72h", cfg.LoanPeriod)
	}
	if cfg.MaxActiveLoans != 2 {
		t.Errorf("MaxActiveLoans = %d, want 2", cfg.MaxActiveLoans)
	}
}
