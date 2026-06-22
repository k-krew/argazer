package config

import (
	"os"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	// Reset viper for each test
	defer viper.Reset()

	tests := []struct {
		name        string
		setup       func()
		expectedErr string
	}{
		{
			name: "missing argocd_url",
			setup: func() {
				viper.Reset()
				os.Setenv("AG_ARGOCD_USERNAME", "admin")
				os.Setenv("AG_ARGOCD_PASSWORD", "password")
			},
			expectedErr: "argocd_url is required",
		},
		{
			name: "missing argocd_username",
			setup: func() {
				viper.Reset()
				os.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
				os.Setenv("AG_ARGOCD_PASSWORD", "password")
				os.Unsetenv("AG_ARGOCD_USERNAME")
			},
			expectedErr: "argocd_username is required",
		},
		{
			name: "missing argocd_password",
			setup: func() {
				viper.Reset()
				os.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
				os.Setenv("AG_ARGOCD_USERNAME", "admin")
				os.Unsetenv("AG_ARGOCD_PASSWORD")
			},
			expectedErr: "argocd_password is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			defer func() {
				os.Unsetenv("AG_ARGOCD_URL")
				os.Unsetenv("AG_ARGOCD_USERNAME")
				os.Unsetenv("AG_ARGOCD_PASSWORD")
			}()

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestLoad_TelegramValidation(t *testing.T) {
	defer viper.Reset()

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
			viper.Reset()
			os.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
			os.Setenv("AG_ARGOCD_USERNAME", "admin")
			os.Setenv("AG_ARGOCD_PASSWORD", "password")
			os.Setenv("AG_NOTIFICATION_CHANNEL", "telegram")
			if tt.webhook != "" {
				os.Setenv("AG_TELEGRAM_WEBHOOK", tt.webhook)
			}
			if tt.chatID != "" {
				os.Setenv("AG_TELEGRAM_CHAT_ID", tt.chatID)
			}

			defer func() {
				os.Unsetenv("AG_ARGOCD_URL")
				os.Unsetenv("AG_ARGOCD_USERNAME")
				os.Unsetenv("AG_ARGOCD_PASSWORD")
				os.Unsetenv("AG_NOTIFICATION_CHANNEL")
				os.Unsetenv("AG_TELEGRAM_WEBHOOK")
				os.Unsetenv("AG_TELEGRAM_CHAT_ID")
			}()

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestLoad_EmailValidation(t *testing.T) {
	defer viper.Reset()

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
			viper.Reset()
			os.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
			os.Setenv("AG_ARGOCD_USERNAME", "admin")
			os.Setenv("AG_ARGOCD_PASSWORD", "password")
			os.Setenv("AG_NOTIFICATION_CHANNEL", "email")
			if tt.smtpHost != "" {
				os.Setenv("AG_EMAIL_SMTP_HOST", tt.smtpHost)
			}
			if tt.from != "" {
				os.Setenv("AG_EMAIL_FROM", tt.from)
			}
			if tt.to != "" {
				os.Setenv("AG_EMAIL_TO", tt.to)
			}

			defer func() {
				os.Unsetenv("AG_ARGOCD_URL")
				os.Unsetenv("AG_ARGOCD_USERNAME")
				os.Unsetenv("AG_ARGOCD_PASSWORD")
				os.Unsetenv("AG_NOTIFICATION_CHANNEL")
				os.Unsetenv("AG_EMAIL_SMTP_HOST")
				os.Unsetenv("AG_EMAIL_FROM")
				os.Unsetenv("AG_EMAIL_TO")
			}()

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestLoad_Success(t *testing.T) {
	defer viper.Reset()

	viper.Reset()
	os.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
	os.Setenv("AG_ARGOCD_USERNAME", "admin")
	os.Setenv("AG_ARGOCD_PASSWORD", "password123")
	os.Setenv("AG_ARGOCD_INSECURE", "true")
	os.Setenv("AG_VERBOSITY", "full")
	os.Setenv("AG_CONCURRENCY", "20")
	os.Setenv("AG_SOURCE_NAME", "my-chart")
	os.Setenv("AG_LABELS", "env=prod,team=platform")

	defer func() {
		os.Unsetenv("AG_ARGOCD_URL")
		os.Unsetenv("AG_ARGOCD_USERNAME")
		os.Unsetenv("AG_ARGOCD_PASSWORD")
		os.Unsetenv("AG_ARGOCD_INSECURE")
		os.Unsetenv("AG_VERBOSITY")
		os.Unsetenv("AG_CONCURRENCY")
		os.Unsetenv("AG_SOURCE_NAME")
		os.Unsetenv("AG_LABELS")
	}()

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
	os.Setenv("AG_ARGOCD_URL", "https://argocd.example.com")
	os.Setenv("AG_ARGOCD_USERNAME", "admin")
	os.Setenv("AG_ARGOCD_PASSWORD", "password")

	defer func() {
		os.Unsetenv("AG_ARGOCD_URL")
		os.Unsetenv("AG_ARGOCD_USERNAME")
		os.Unsetenv("AG_ARGOCD_PASSWORD")
	}()

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
}
