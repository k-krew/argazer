package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Output format constants
const (
	OutputFormatTable    = "table"
	OutputFormatJSON     = "json"
	OutputFormatMarkdown = "markdown"
)

// Version constraint constants
const (
	VersionConstraintMajor = "major"
	VersionConstraintMinor = "minor"
	VersionConstraintPatch = "patch"
)

// Log format constants
const (
	LogFormatJSON = "json"
	LogFormatText = "text"
)

// Verbosity level constants
const (
	VerbosityFull   = "full"   // All logs including debug
	VerbosityNormal = "normal" // Only necessary logs (info and above)
	VerbosityOff    = "off"    // No logs, only result output
)

// Config holds the application configuration
type Config struct {
	// ArgoCD connection settings
	ArgocdURL      string `mapstructure:"argocd_url"`
	ArgocdUsername string `mapstructure:"argocd_username"`
	ArgocdPassword string `mapstructure:"argocd_password"`
	ArgocdInsecure bool   `mapstructure:"argocd_insecure"` // Skip TLS verification

	// Search scope
	Projects []string          `mapstructure:"projects"`  // List of projects to check, or ["*"] for all
	AppNames []string          `mapstructure:"app_names"` // List of app names to check, or ["*"] for all
	Labels   map[string]string `mapstructure:"labels"`    // Label filters

	// Notification settings
	NotificationChannel string `mapstructure:"notification_channel"` // "telegram", "email", "slack", "teams", "webhook", or empty

	// Telegram settings
	TelegramWebhook string `mapstructure:"telegram_webhook"`
	TelegramChatID  string `mapstructure:"telegram_chat_id"`

	// Email settings
	EmailSmtpHost     string   `mapstructure:"email_smtp_host"`
	EmailSmtpPort     int      `mapstructure:"email_smtp_port"`
	EmailSmtpUsername string   `mapstructure:"email_smtp_username"`
	EmailSmtpPassword string   `mapstructure:"email_smtp_password"`
	EmailFrom         string   `mapstructure:"email_from"`
	EmailTo           []string `mapstructure:"email_to"`
	EmailUseTLS       bool     `mapstructure:"email_use_tls"`

	// Slack settings
	SlackWebhook string `mapstructure:"slack_webhook"`

	// Microsoft Teams settings
	TeamsWebhook string `mapstructure:"teams_webhook"`

	// Generic Webhook settings
	WebhookURL string `mapstructure:"webhook_url"`

	// General settings
	Verbosity         string `mapstructure:"verbosity"`
	LogFormat         string `mapstructure:"log_format"`         // Log format: "json" or "text" (default: "json")
	SourceName        string `mapstructure:"source_name"`        // Name of the source to check in multi-source applications
	Concurrency       int    `mapstructure:"concurrency"`        // Number of concurrent workers for checking applications
	VersionConstraint string `mapstructure:"version_constraint"` // Version constraint: "major", "minor", "patch" (default: "major")
	OutputFormat      string `mapstructure:"output_format"`      // Output format: "table", "json", "markdown" (default: "table")

	// Repository authentication
	RepositoryAuth []RepositoryAuth `mapstructure:"repository_auth"`
}

// RepositoryAuth holds authentication for a specific repository or registry
type RepositoryAuth struct {
	URL      string `mapstructure:"url"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

// Load loads configuration from various sources
func Load() (*Config, error) {
	setDefaults()

	if err := loadConfigFile(); err != nil {
		return nil, err
	}

	setupEnvironment()
	registerFlagAliases()

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// setDefaults sets default values for all configuration fields
func setDefaults() {
	// Boolean and numeric defaults
	viper.SetDefault("verbosity", VerbosityNormal)
	viper.SetDefault("argocd_insecure", false)
	viper.SetDefault("email_smtp_port", 587)
	viper.SetDefault("email_use_tls", true)
	viper.SetDefault("concurrency", 10)

	// String defaults
	viper.SetDefault("source_name", "chart-repo")
	viper.SetDefault("version_constraint", VersionConstraintMajor)
	viper.SetDefault("output_format", OutputFormatTable)
	viper.SetDefault("log_format", LogFormatJSON)
	viper.SetDefault("argocd_url", "")
	viper.SetDefault("argocd_username", "")
	viper.SetDefault("argocd_password", "")
	viper.SetDefault("notification_channel", "")
	viper.SetDefault("telegram_webhook", "")
	viper.SetDefault("telegram_chat_id", "")
	viper.SetDefault("email_smtp_host", "")
	viper.SetDefault("email_smtp_username", "")
	viper.SetDefault("email_smtp_password", "")
	viper.SetDefault("email_from", "")
	viper.SetDefault("slack_webhook", "")
	viper.SetDefault("teams_webhook", "")
	viper.SetDefault("webhook_url", "")

	// Array/slice defaults
	viper.SetDefault("projects", []string{"*"})
	viper.SetDefault("app_names", []string{"*"})
	viper.SetDefault("email_to", []string{})

	// Map defaults
	viper.SetDefault("labels", map[string]string{})
	viper.SetDefault("repository_auth", []RepositoryAuth{})
}

// loadConfigFile loads configuration from file (if specified or found in default paths)
func loadConfigFile() error {
	// Check if a specific config file was provided via --config flag
	configFile := viper.GetString("config")
	if configFile != "" {
		// Use the specified config file
		viper.SetConfigFile(configFile)
		if err := viper.ReadInConfig(); err != nil {
			return fmt.Errorf("error reading config file %s: %w", configFile, err)
		}
	} else {
		// Set config file name and paths for default locations
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
		viper.AddConfigPath("/etc/argazer")
		viper.AddConfigPath("$HOME/.argazer")

		// Read config file if it exists
		if err := viper.ReadInConfig(); err != nil {
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				return fmt.Errorf("error reading config file: %w", err)
			}
			// Config file not found, continue with defaults and env vars
		}
	}

	return nil
}

// setupEnvironment configures environment variable handling
func setupEnvironment() {
	// Set up environment variable prefix and replacer
	// AutomaticEnv() automatically binds all config fields to environment variables
	// with the AG_ prefix (e.g., AG_ARGOCD_URL maps to argocd_url)
	viper.SetEnvPrefix("AG")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	viper.AutomaticEnv()

	// Handle labels from environment variable BEFORE unmarshal
	// Format: AG_LABELS=key1=value1,key2=value2
	// Check if labels is set as a string (from env var) and convert it to a map
	if viper.IsSet("labels") {
		// Try to get it as a string first (from env var)
		if labelsStr, ok := viper.Get("labels").(string); ok && labelsStr != "" {
			// Parse the string and set it back as a map
			labelsMap := parseLabelsFromString(labelsStr)
			viper.Set("labels", labelsMap)
		}
	}
}

// registerFlagAliases registers aliases to map config keys (with underscores) to flag names (with dashes)
func registerFlagAliases() {
	// RegisterAlias(alias, key) makes the alias name point to the key
	// When unmarshal looks for "argocd_url", it will find the value stored under "argocd-url"
	viper.RegisterAlias("argocd_url", "argocd-url")
	viper.RegisterAlias("argocd_username", "argocd-username")
	viper.RegisterAlias("argocd_password", "argocd-password")
	viper.RegisterAlias("argocd_insecure", "argocd-insecure")
	viper.RegisterAlias("app_names", "app-names")
	viper.RegisterAlias("notification_channel", "notification-channel")
	viper.RegisterAlias("version_constraint", "version-constraint")
	viper.RegisterAlias("output_format", "output-format")
	viper.RegisterAlias("log_format", "log-format")
}

// validateConfig validates the loaded configuration
func validateConfig(cfg *Config) error {
	// Validate required fields
	if cfg.ArgocdURL == "" {
		return fmt.Errorf("argocd_url is required")
	}
	if cfg.ArgocdUsername == "" {
		return fmt.Errorf("argocd_username is required")
	}
	if cfg.ArgocdPassword == "" {
		return fmt.Errorf("argocd_password is required")
	}

	// Validate version constraint
	if cfg.VersionConstraint != "" && cfg.VersionConstraint != VersionConstraintMajor && cfg.VersionConstraint != VersionConstraintMinor && cfg.VersionConstraint != VersionConstraintPatch {
		return fmt.Errorf("version_constraint must be one of: '%s', '%s', '%s' (got: '%s')", VersionConstraintMajor, VersionConstraintMinor, VersionConstraintPatch, cfg.VersionConstraint)
	}
	// Normalize empty to "major"
	if cfg.VersionConstraint == "" {
		cfg.VersionConstraint = VersionConstraintMajor
	}

	// Validate output format
	if cfg.OutputFormat != "" && cfg.OutputFormat != OutputFormatTable && cfg.OutputFormat != OutputFormatJSON && cfg.OutputFormat != OutputFormatMarkdown {
		return fmt.Errorf("output_format must be one of: '%s', '%s', '%s' (got: '%s')", OutputFormatTable, OutputFormatJSON, OutputFormatMarkdown, cfg.OutputFormat)
	}
	// Normalize empty to "table"
	if cfg.OutputFormat == "" {
		cfg.OutputFormat = OutputFormatTable
	}

	// Validate log format
	if cfg.LogFormat != "" && cfg.LogFormat != LogFormatJSON && cfg.LogFormat != LogFormatText {
		return fmt.Errorf("log_format must be one of: '%s', '%s' (got: '%s')", LogFormatJSON, LogFormatText, cfg.LogFormat)
	}
	// Normalize empty to "json"
	if cfg.LogFormat == "" {
		cfg.LogFormat = LogFormatJSON
	}

	// Validate verbosity
	if cfg.Verbosity != "" && cfg.Verbosity != VerbosityFull && cfg.Verbosity != VerbosityNormal && cfg.Verbosity != VerbosityOff {
		return fmt.Errorf("verbosity must be one of: '%s', '%s', '%s' (got: '%s')", VerbosityFull, VerbosityNormal, VerbosityOff, cfg.Verbosity)
	}
	if cfg.Verbosity == "" {
		cfg.Verbosity = VerbosityNormal
	}

	// Validate notification channel settings
	switch cfg.NotificationChannel {
	case "telegram":
		if cfg.TelegramWebhook == "" {
			return fmt.Errorf("telegram_webhook is required when notification_channel is 'telegram'")
		}
		if cfg.TelegramChatID == "" {
			return fmt.Errorf("telegram_chat_id is required when notification_channel is 'telegram'")
		}
	case "email":
		if cfg.EmailSmtpHost == "" {
			return fmt.Errorf("email_smtp_host is required when notification_channel is 'email'")
		}
		if cfg.EmailFrom == "" {
			return fmt.Errorf("email_from is required when notification_channel is 'email'")
		}
		if len(cfg.EmailTo) == 0 {
			return fmt.Errorf("email_to is required when notification_channel is 'email'")
		}
	case "slack":
		if cfg.SlackWebhook == "" {
			return fmt.Errorf("slack_webhook is required when notification_channel is 'slack'")
		}
	case "teams":
		if cfg.TeamsWebhook == "" {
			return fmt.Errorf("teams_webhook is required when notification_channel is 'teams'")
		}
	case "webhook":
		if cfg.WebhookURL == "" {
			return fmt.Errorf("webhook_url is required when notification_channel is 'webhook'")
		}
	}

	return nil
}

// parseLabelsFromString parses a comma-separated key=value string into a map
// Example: "key1=value1,key2=value2" -> map[string]string{"key1": "value1", "key2": "value2"}
func parseLabelsFromString(labelsStr string) map[string]string {
	labels := make(map[string]string)
	if labelsStr == "" {
		return labels
	}

	pairs := strings.Split(labelsStr, ",")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			if key != "" {
				labels[key] = value
			}
		}
	}

	return labels
}
