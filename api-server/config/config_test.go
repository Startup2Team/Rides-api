package config

import "testing"

// TestGetEnvList covers the CORS multi-origin parsing (ADMIN_ORIGIN) — must
// stay backward compatible with a single bare value (no comma) since that's
// what production has run with until now.
func TestGetEnvList(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		setEnv   bool
		fallback []string
		want     []string
	}{
		{
			name:     "single value (backward compat)",
			envValue: "https://admin.rides.rw",
			setEnv:   true,
			want:     []string{"https://admin.rides.rw"},
		},
		{
			name:     "multiple comma-separated origins",
			envValue: "https://admin.rides.rw,https://rides.rw,https://www.rides.rw",
			setEnv:   true,
			want:     []string{"https://admin.rides.rw", "https://rides.rw", "https://www.rides.rw"},
		},
		{
			name:     "whitespace around commas is trimmed",
			envValue: "https://admin.rides.rw, https://rides.rw , https://www.rides.rw",
			setEnv:   true,
			want:     []string{"https://admin.rides.rw", "https://rides.rw", "https://www.rides.rw"},
		},
		{
			name:     "trailing comma drops the blank entry",
			envValue: "https://admin.rides.rw,",
			setEnv:   true,
			want:     []string{"https://admin.rides.rw"},
		},
		{
			name:     "unset env var returns fallback",
			setEnv:   false,
			fallback: []string{"https://fallback.example"},
			want:     []string{"https://fallback.example"},
		},
		{
			name:     "blank env var returns fallback",
			envValue: "",
			setEnv:   true,
			fallback: nil,
			want:     nil,
		},
		{
			name:     "only commas/whitespace returns fallback",
			envValue: " , , ",
			setEnv:   true,
			fallback: []string{"https://fallback.example"},
			want:     []string{"https://fallback.example"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv("TEST_ENV_LIST_KEY", tt.envValue)
			}
			got := getEnvList("TEST_ENV_LIST_KEY", tt.fallback)
			if !equalStringSlices(got, tt.want) {
				t.Fatalf("getEnvList() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestConfig_IsAllowedOrigin(t *testing.T) {
	cfg := &Config{AdminOrigins: []string{"https://admin.rides.rw", "https://rides.rw", "https://www.rides.rw"}}

	tests := []struct {
		origin string
		want   bool
	}{
		{"https://admin.rides.rw", true},
		{"https://rides.rw", true},
		{"https://www.rides.rw", true},
		{"https://evil.example", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := cfg.IsAllowedOrigin(tt.origin); got != tt.want {
			t.Errorf("IsAllowedOrigin(%q) = %v, want %v", tt.origin, got, tt.want)
		}
	}

	var nilCfg *Config
	if nilCfg.IsAllowedOrigin("https://admin.rides.rw") {
		t.Error("IsAllowedOrigin on a nil *Config must return false, not panic")
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
