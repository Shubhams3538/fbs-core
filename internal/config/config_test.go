package config

import (
	"os"
	"reflect"
	"strings"
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
	isolateConfigEnv(t)
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
	isolateConfigEnv(t)
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"fbs", "--dev", "--http-addr", "0.0.0.0:9000"}

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for --dev with non-loopback address")
	}
}

func TestLoad_DevEnvVar(t *testing.T) {
	isolateConfigEnv(t)
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
	isolateConfigEnv(t)
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
	isolateConfigEnv(t)
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
		{"negative read timeout", func(cfg *Config) { cfg.ReadTimeout = -time.Second }},
		{"zero write timeout", func(cfg *Config) { cfg.WriteTimeout = 0 }},
		{"negative write timeout", func(cfg *Config) { cfg.WriteTimeout = -time.Second }},
		{"zero idle timeout", func(cfg *Config) { cfg.IdleTimeout = 0 }},
		{"negative idle timeout", func(cfg *Config) { cfg.IdleTimeout = -time.Second }},
		{"zero shutdown timeout", func(cfg *Config) { cfg.ShutdownTimeout = 0 }},
		{"negative shutdown timeout", func(cfg *Config) { cfg.ShutdownTimeout = -time.Second }},
		{"negative metadata cache", func(cfg *Config) { cfg.MetadataCacheSizeBytes = -1 }},
		{"zero multipart TTL", func(cfg *Config) { cfg.MultipartTTL = 0 }},
		{"negative multipart TTL", func(cfg *Config) { cfg.MultipartTTL = -time.Second }},
		{"zero cleanup interval", func(cfg *Config) { cfg.MultipartCleanupInterval = 0 }},
		{"negative cleanup interval", func(cfg *Config) { cfg.MultipartCleanupInterval = -time.Minute }},
		{"cleanup interval below minimum", func(cfg *Config) { cfg.MultipartCleanupInterval = time.Minute - time.Nanosecond }},
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
	isolateConfigEnv(t)
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"fbs", "--invalid-flag"}
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid flag")
	}
}

func isolateConfigEnv(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "FBS_") {
			// Setenv records the inherited value for restoration at cleanup.
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestLoadWiresEnvironmentAndFlags(t *testing.T) {
	for _, source := range []string{"environment", "flags override environment"} {
		t.Run(source, func(t *testing.T) {
			isolateConfigEnv(t)
			oldArgs := os.Args
			t.Cleanup(func() { os.Args = oldArgs })
			os.Args = []string{"fbs"}
			settings := []struct{ env, flag, value string }{
				{"FBS_HTTP_ADDR", "http-addr", "127.0.0.1:9123"},
				{"FBS_DB_PATH", "db-path", "/tmp/config-wiring.db"},
				{"FBS_DATA_DIR", "data-dir", "/tmp/config-wiring-data"},
				{"FBS_PUBLIC_BASE_URL", "public-base-url", "https://files.example.test"},
				{"FBS_READ_TIMEOUT", "read-timeout", "11s"},
				{"FBS_WRITE_TIMEOUT", "write-timeout", "22s"},
				{"FBS_IDLE_TIMEOUT", "idle-timeout", "33s"},
				{"FBS_SHUTDOWN_TIMEOUT", "shutdown-timeout", "44s"},
				{"FBS_MULTIPART_TTL", "multipart-ttl", "5h"},
				{"FBS_MULTIPART_CLEANUP_INTERVAL", "multipart-cleanup-interval", "6m"},
				{"FBS_METADATA_CACHE_SIZE", "metadata-cache-size", "7MiB"},
				{"FBS_S3_CACHE_CONTROL", "s3-cache-control", "private, max-age=123"},
				{"FBS_PUBLIC_READ_SIGNING_SECRET", "public-read-signing-secret", strings.Repeat("s", 32)},
				{"FBS_PUBLIC_READ_DEFAULT_TTL", "public-read-default-ttl", "8m"},
				{"FBS_PUBLIC_READ_MAX_TTL", "public-read-max-ttl", "9h"},
				{"FBS_CORS_ALLOWED_ORIGINS", "cors-allowed-origins", "https://app.example.test,https://admin.example.test"},
			}
			for _, setting := range settings {
				t.Setenv(setting.env, setting.value)
				if source == "flags override environment" {
					os.Args = append(os.Args, "--"+setting.flag, setting.value)
					// Distinct, valid environment defaults expose ignored flags.
					switch setting.env {
					case "FBS_METADATA_CACHE_SIZE":
						t.Setenv(setting.env, "1MiB")
					case "FBS_PUBLIC_READ_SIGNING_SECRET":
						t.Setenv(setting.env, strings.Repeat("e", 32))
					default:
						if strings.HasSuffix(setting.env, "TIMEOUT") || strings.HasSuffix(setting.env, "TTL") || strings.HasSuffix(setting.env, "INTERVAL") {
							t.Setenv(setting.env, "1m")
						} else {
							t.Setenv(setting.env, "")
						}
					}
				}
			}
			got, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			want := Default()
			want.HTTPAddr = "127.0.0.1:9123"
			want.DBPath = "/tmp/config-wiring.db"
			want.DataDir = "/tmp/config-wiring-data"
			want.PublicBaseURL = "https://files.example.test"
			want.ReadTimeout = 11 * time.Second
			want.WriteTimeout = 22 * time.Second
			want.IdleTimeout = 33 * time.Second
			want.ShutdownTimeout = 44 * time.Second
			want.MultipartTTL = 5 * time.Hour
			want.MultipartCleanupInterval = 6 * time.Minute
			want.MetadataCacheSizeBytes = 7 * 1024 * 1024
			want.S3CacheControl = "private, max-age=123"
			want.PublicReadSigningSecret = strings.Repeat("s", 32)
			want.PublicReadDefaultTTL = 8 * time.Minute
			want.PublicReadMaxTTL = 9 * time.Hour
			want.CORSAllowedOrigins = []string{"https://app.example.test", "https://admin.example.test"}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Load() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestConfigValidationAcceptsCacheAndCleanupBounds(t *testing.T) {
	cfg := Default()
	cfg.MetadataCacheSizeBytes = 0
	cfg.MultipartCleanupInterval = time.Minute
	if err := cfg.Validate(); err != nil {
		t.Fatalf("zero cache and minimum cleanup interval: %v", err)
	}
}
