package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateArgocdEnv clears every environment variable that can satisfy the ArgoCD
// connection settings, so values exported on a developer machine or in CI cannot leak
// into a test. ARGOCD_AUTH_TOKEN matters most: it has no AG_ prefix, it is commonly
// exported for the ArgoCD CLI, and it would otherwise mask a missing username/password.
// Viper treats an empty variable as unset, so setting the variables to "" is enough.
func isolateArgocdEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"AG_ARGOCD_URL",
		"AG_ARGOCD_USERNAME",
		"AG_ARGOCD_PASSWORD",
		"AG_ARGOCD_AUTH_TOKEN",
		"ARGOCD_AUTH_TOKEN",
	} {
		t.Setenv(key, "")
	}
}

func TestParseLabelsFromString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: map[string]string{},
		},
		{
			name:  "single label",
			input: "env=prod",
			expected: map[string]string{
				"env": "prod",
			},
		},
		{
			name:  "multiple labels",
			input: "env=prod,team=platform,region=us-east",
			expected: map[string]string{
				"env":    "prod",
				"team":   "platform",
				"region": "us-east",
			},
		},
		{
			name:  "labels with spaces",
			input: " env = prod , team = platform ",
			expected: map[string]string{
				"env":  "prod",
				"team": "platform",
			},
		},
		{
			name:  "empty value",
			input: "env=,team=platform",
			expected: map[string]string{
				"env":  "",
				"team": "platform",
			},
		},
		{
			name:     "invalid format - no equals",
			input:    "env,team=platform",
			expected: map[string]string{"team": "platform"},
		},
		{
			name:     "empty key",
			input:    "=value,team=platform",
			expected: map[string]string{"team": "platform"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseLabelsFromString(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestLoad_RequiredFields(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		username    string
		password    string
		expectedErr string
	}{
		{
			name:        "missing argocd_url",
			username:    "admin",
			password:    "password",
			expectedErr: "argocd_url is required",
		},
		{
			name:        "missing argocd_username",
			url:         "https://argocd.example.com",
			password:    "password",
			expectedErr: "argocd_username is required",
		},
		{
			name:        "missing argocd_password",
			url:         "https://argocd.example.com",
			username:    "admin",
			expectedErr: "argocd_password is required",
		},
		{
			name:        "missing credentials and token",
			url:         "https://argocd.example.com",
			expectedErr: "argocd_username is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer viper.Reset()

			viper.Reset()
			isolateArgocdEnv(t)
			t.Setenv("AG_ARGOCD_URL", tt.url)
			t.Setenv("AG_ARGOCD_USERNAME", tt.username)
			t.Setenv("AG_ARGOCD_PASSWORD", tt.password)

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

// TestLoad_AuthToken covers the token as a full replacement for username/password.
func TestLoad_AuthToken(t *testing.T) {
	tests := []struct {
		name     string
		envVar   string
		username string
		password string
	}{
		{
			name:   "token only, prefixed env var",
			envVar: "AG_ARGOCD_AUTH_TOKEN",
		},
		{
			name:   "token only, ArgoCD CLI env var",
			envVar: "ARGOCD_AUTH_TOKEN",
		},
		{
			name:     "token alongside credentials",
			envVar:   "AG_ARGOCD_AUTH_TOKEN",
			username: "admin",
			password: "password",
		},
		{
			name:     "token with username but no password",
			envVar:   "AG_ARGOCD_AUTH_TOKEN",
			username: "admin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer viper.Reset()

			viper.Reset()
			isolateArgocdEnv(t)
			t.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
			t.Setenv(tt.envVar, "token-abc123")
			t.Setenv("AG_ARGOCD_USERNAME", tt.username)
			t.Setenv("AG_ARGOCD_PASSWORD", tt.password)

			cfg, err := Load()
			require.NoError(t, err)
			assert.Equal(t, "token-abc123", cfg.ArgocdAuthToken)
			assert.Equal(t, tt.username, cfg.ArgocdUsername)
			assert.Equal(t, tt.password, cfg.ArgocdPassword)
		})
	}
}

// TestLoad_AuthTokenPrefixWins makes sure the Argazer-specific variable takes precedence
// over the generic one shared with the ArgoCD CLI.
func TestLoad_AuthTokenPrefixWins(t *testing.T) {
	defer viper.Reset()

	viper.Reset()
	isolateArgocdEnv(t)
	t.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
	t.Setenv("AG_ARGOCD_AUTH_TOKEN", "argazer-token")
	t.Setenv("ARGOCD_AUTH_TOKEN", "cli-token")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "argazer-token", cfg.ArgocdAuthToken)
}

// TestLoad_AuthTokenTrimmed documents that surrounding whitespace never reaches the
// ArgoCD client, and that a token made of whitespace only is treated as no token at all.
func TestLoad_AuthTokenTrimmed(t *testing.T) {
	tests := []struct {
		name          string
		token         string
		expectedToken string
		expectedErr   string
	}{
		{
			name:          "padded token is trimmed",
			token:         "  token-abc123\n",
			expectedToken: "token-abc123",
		},
		{
			name:        "whitespace-only token is not a token",
			token:       "   ",
			expectedErr: "argocd_username is required when argocd_auth_token is not set",
		},
		{
			name:        "tab-only token is not a token",
			token:       "\t",
			expectedErr: "argocd_username is required when argocd_auth_token is not set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer viper.Reset()

			viper.Reset()
			isolateArgocdEnv(t)
			t.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
			t.Setenv("AG_ARGOCD_AUTH_TOKEN", tt.token)

			cfg, err := Load()
			if tt.expectedErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedToken, cfg.ArgocdAuthToken)
		})
	}
}

// TestLoad_NoAuthToken keeps username/password mandatory when no token is provided.
func TestLoad_NoAuthToken(t *testing.T) {
	defer viper.Reset()

	viper.Reset()
	isolateArgocdEnv(t)
	t.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
	t.Setenv("AG_ARGOCD_USERNAME", "admin")
	t.Setenv("AG_ARGOCD_PASSWORD", "password")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.ArgocdAuthToken)
	assert.Equal(t, "admin", cfg.ArgocdUsername)
}

func TestLoad_TelegramValidation(t *testing.T) {
	tests := []struct {
		name        string
		webhook     string
		chatID      string
		expectedErr string
	}{
		{
			name:        "missing webhook",
			webhook:     "",
			chatID:      "12345",
			expectedErr: "telegram_webhook is required",
		},
		{
			name:        "missing chat_id",
			webhook:     "https://api.telegram.org/bot123/sendMessage",
			chatID:      "",
			expectedErr: "telegram_chat_id is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer viper.Reset()

			viper.Reset()
			isolateArgocdEnv(t)
			t.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
			t.Setenv("AG_ARGOCD_USERNAME", "admin")
			t.Setenv("AG_ARGOCD_PASSWORD", "password")
			t.Setenv("AG_NOTIFICATION_CHANNEL", "telegram")
			t.Setenv("AG_TELEGRAM_WEBHOOK", tt.webhook)
			t.Setenv("AG_TELEGRAM_CHAT_ID", tt.chatID)

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestLoad_EmailValidation(t *testing.T) {
	tests := []struct {
		name        string
		smtpHost    string
		from        string
		to          string
		expectedErr string
	}{
		{
			name:        "missing smtp_host",
			smtpHost:    "",
			from:        "sender@example.com",
			to:          "recipient@example.com",
			expectedErr: "email_smtp_host is required",
		},
		{
			name:        "missing from",
			smtpHost:    "smtp.example.com",
			from:        "",
			to:          "recipient@example.com",
			expectedErr: "email_from is required",
		},
		{
			name:        "missing to",
			smtpHost:    "smtp.example.com",
			from:        "sender@example.com",
			to:          "",
			expectedErr: "email_to is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer viper.Reset()

			viper.Reset()
			isolateArgocdEnv(t)
			t.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
			t.Setenv("AG_ARGOCD_USERNAME", "admin")
			t.Setenv("AG_ARGOCD_PASSWORD", "password")
			t.Setenv("AG_NOTIFICATION_CHANNEL", "email")
			t.Setenv("AG_EMAIL_SMTP_HOST", tt.smtpHost)
			t.Setenv("AG_EMAIL_FROM", tt.from)
			t.Setenv("AG_EMAIL_TO", tt.to)

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestLoad_Success(t *testing.T) {
	defer viper.Reset()

	viper.Reset()
	isolateArgocdEnv(t)
	t.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
	t.Setenv("AG_ARGOCD_USERNAME", "admin")
	t.Setenv("AG_ARGOCD_PASSWORD", "password123")
	t.Setenv("AG_ARGOCD_INSECURE", "true")
	t.Setenv("AG_VERBOSITY", "full")
	t.Setenv("AG_CONCURRENCY", "20")
	t.Setenv("AG_SOURCE_NAME", "my-chart")
	t.Setenv("AG_LABELS", "env=prod,team=platform")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "https://argocd.example.com", cfg.ArgocdURL)
	assert.Equal(t, "admin", cfg.ArgocdUsername)
	assert.Equal(t, "password123", cfg.ArgocdPassword)
	assert.True(t, cfg.ArgocdInsecure)
	assert.Equal(t, "full", cfg.Verbosity)
	assert.Equal(t, 20, cfg.Concurrency)
	assert.Equal(t, "my-chart", cfg.SourceName)
	assert.Equal(t, map[string]string{"env": "prod", "team": "platform"}, cfg.Labels)
}

func TestLoad_Defaults(t *testing.T) {
	defer viper.Reset()

	viper.Reset()
	isolateArgocdEnv(t)
	t.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
	t.Setenv("AG_ARGOCD_USERNAME", "admin")
	t.Setenv("AG_ARGOCD_PASSWORD", "password")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Check defaults
	assert.Equal(t, "normal", cfg.Verbosity)
	assert.False(t, cfg.ArgocdInsecure)
	assert.Equal(t, 10, cfg.Concurrency)
	assert.Equal(t, "chart-repo", cfg.SourceName)
	assert.Equal(t, []string{"*"}, cfg.Projects)
	assert.Equal(t, []string{"*"}, cfg.AppNames)
	assert.Equal(t, map[string]string{}, cfg.Labels)
	assert.Equal(t, FailOnNone, cfg.FailOn)
}

func TestLoad_FailOnValidation(t *testing.T) {
	tests := []struct {
		name        string
		failOn      string
		expectedErr string
	}{
		{name: "none", failOn: FailOnNone},
		{name: "any", failOn: FailOnAny},
		{name: "patch", failOn: FailOnPatch},
		{name: "minor", failOn: FailOnMinor},
		{name: "major", failOn: FailOnMajor},
		{name: "invalid value", failOn: "everything", expectedErr: "fail_on must be one of"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer viper.Reset()

			viper.Reset()
			isolateArgocdEnv(t)
			t.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
			t.Setenv("AG_ARGOCD_USERNAME", "admin")
			t.Setenv("AG_ARGOCD_PASSWORD", "password")
			t.Setenv("AG_FAIL_ON", tt.failOn)

			cfg, err := Load()
			if tt.expectedErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.failOn, cfg.FailOn)
		})
	}
}

// TestLoad_FailOnEmpty makes sure an explicitly empty value behaves like "none", so a run
// without a quality gate never exits with code 2.
func TestLoad_FailOnEmpty(t *testing.T) {
	defer viper.Reset()

	viper.Reset()
	isolateArgocdEnv(t)
	t.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
	t.Setenv("AG_ARGOCD_USERNAME", "admin")
	t.Setenv("AG_ARGOCD_PASSWORD", "password")
	t.Setenv("AG_FAIL_ON", "")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, FailOnNone, cfg.FailOn)
}
