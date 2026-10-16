package config

import (
	"os"
	"testing"
	"time"
)

func TestDefaultConfigIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}
	if cfg.DevMode {
		t.Error("expected DevMode to be false by default")
	}
}

func TestConfigValidate_DevModeRequiresLoopback(t *testing.T) {
	// The address cases belong to auth.ValidateDevMode; this checks the wiring.
	cfg := Default()
	cfg.HTTPAddr = "0.0.0.0:9000"
	cfg.DevMode = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected dev mode on a public address to fail validation")
	}
}

func TestBoolFromEnvStrict(t *testing.T) {
	cases := []struct {
		name     string
		envValue string
		fallback bool
		want     bool
		wantErr  bool
	}{
		{"unset", "", false, false, false},
		{"empty", "", true, true, false},
		{"true lower", "true", false, true, false},
		{"true upper", "TRUE", false, true, false},
		{"1", "1", false, true, false},
		{"false lower", "false", true, false, false},
		{"false upper", "FALSE", true, false, false},
		{"0", "0", true, false, false},
		{"invalid", "maybe", false, false, true},
		{"invalid mixed", "YeS", false, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envValue != "" || tc.name == "empty" {
				t.Setenv("TEST_BOOL_STRICT", tc.envValue)
			} else {
				os.Unsetenv("TEST_BOOL_STRICT")
			}

			got, err := boolFromEnvStrict("TEST_BOOL_STRICT", tc.fallback)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoad_DevFlag(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"fbs", "--dev", "--http-addr", "127.0.0.1:9001"}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.DevMode {
		t.Error("expected DevMode to be true")
	}
	if cfg.HTTPAddr != "127.0.0.1:9001" {
		t.Errorf("HTTPAddr = %q, want 127.0.0.1:9001", cfg.HTTPAddr)
	}
}

func TestLoad_DevFlagRejectsNonLoopback(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"fbs", "--dev", "--http-addr", "0.0.0.0:9000"}

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for --dev with non-loopback address")
	}
}

func TestLoad_DevEnvVar(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	t.Setenv("FBS_DEV", "true")
	os.Args = []string{"fbs", "--http-addr", "127.0.0.1:9000"}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.DevMode {
		t.Error("expected DevMode to be true from FBS_DEV")
	}
}

func TestLoad_DevEnvVarInvalid(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	t.Setenv("FBS_DEV", "maybe")
	os.Args = []string{"fbs"}

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid FBS_DEV value")
	}
}

func TestLoad_FlagOverridesEnv(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	t.Setenv("FBS_DEV", "false")
	os.Args = []string{"fbs", "--dev", "--http-addr", "127.0.0.1:9000"}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.DevMode {
		t.Error("expected --dev flag to override FBS_DEV=false")
	}
}

func TestConfigValidationRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{"zero read timeout", func(cfg *Config) { cfg.ReadTimeout = 0 }},
		{"empty HTTP address", func(cfg *Config) { cfg.HTTPAddr = "" }},
		{"empty database path", func(cfg *Config) { cfg.DBPath = "" }},
		{"empty data directory", func(cfg *Config) { cfg.DataDir = "" }},
		{"invalid public URL", func(cfg *Config) { cfg.PublicBaseURL = "://invalid" }},
		{"empty CORS origins", func(cfg *Config) { cfg.CORSAllowedOrigins = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.change(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected invalid configuration to fail validation")
			}
		})
	}
}

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b , c ", []string{"a", "b", "c"}},
		{"a,,b", []string{"a", "b"}},
		{"", []string{}},
		{",,", []string{}},
	}

	for _, tc := range cases {
		got := splitCSV(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("splitCSV(%q) = %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitCSV(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

func TestDurationFromEnv(t *testing.T) {
	t.Setenv("TEST_DURATION", "30s")
	got, err := durationFromEnv("TEST_DURATION", 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 30*time.Second {
		t.Errorf("got %v, want 30s", got)
	}

	got, err = durationFromEnv("TEST_DURATION_UNSET", 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 10*time.Second {
		t.Errorf("got %v, want 10s", got)
	}

	t.Setenv("TEST_DURATION_INVALID", "not-a-duration")
	_, err = durationFromEnv("TEST_DURATION_INVALID", 10*time.Second)
	if err == nil {
		t.Error("expected error for invalid duration")
	}
}

func TestParseByteSize(t *testing.T) {
	cases := []struct {
		input string
		want  int64
	}{
		{"512MiB", 512 * 1024 * 1024},
		{"64MB", 64 * 1000 * 1000},
		{"12345", 12345},
	}

	for _, tc := range cases {
		got, err := parseByteSize(tc.input)
		if err != nil {
			t.Fatalf("parseByteSize(%q) error = %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("parseByteSize(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestParseByteSizeInvalid(t *testing.T) {
	for _, input := range []string{"", "nope", "-1", "10Mi", "1.5MiB"} {
		if _, err := parseByteSize(input); err == nil {
			t.Fatalf("parseByteSize(%q) expected error", input)
		}
	}
}

func TestConfigValidation_PublicReadSigningSecretLength(t *testing.T) {
	cfg := Default()
	cfg.PublicReadSigningSecret = "too-short"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for short public read signing secret")
	}
}

func TestConfigValidation_PublicReadTTLs(t *testing.T) {
	cfg := Default()
	cfg.PublicReadDefaultTTL = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for zero default TTL")
	}

	cfg = Default()
	cfg.PublicReadMaxTTL = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for zero max TTL")
	}

	cfg = Default()
	cfg.PublicReadDefaultTTL = 25 * time.Hour
	cfg.PublicReadMaxTTL = 24 * time.Hour
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when default TTL exceeds max TTL")
	}
}

func TestEnvOrDefault(t *testing.T) {
	t.Setenv("TEST_ENV", "value")
	if got := envOrDefault("TEST_ENV", "fallback"); got != "value" {
		t.Errorf("got %q, want value", got)
	}

	os.Unsetenv("TEST_ENV_UNSET")
	if got := envOrDefault("TEST_ENV_UNSET", "fallback"); got != "fallback" {
		t.Errorf("got %q, want fallback", got)
	}

	t.Setenv("TEST_ENV_EMPTY", "   ")
	if got := envOrDefault("TEST_ENV_EMPTY", "fallback"); got != "fallback" {
		t.Errorf("got %q, want fallback for whitespace-only", got)
	}
}

func TestFlagSetErrorPropagates(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"fbs", "--invalid-flag"}
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid flag")
	}
}
