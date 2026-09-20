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

// Notify-on constants: the widest version bump a run reports on
const (
	NotifyOnMajor = "major" // Report every newer version
	NotifyOnMinor = "minor" // Report versions with the same major
	NotifyOnPatch = "patch" // Report versions with the same major.minor
)

// Notification channel constants: the channels a report can be pushed to
const (
	NotificationChannelSlack   = "slack"
	NotificationChannelWebhook = "webhook"
)

// Fail-on constants: the lowest update severity that makes the run exit with code 2
const (
	FailOnNone  = "none"  // Never fail because of updates
	FailOnAny   = "any"   // Fail on any update, including unclassifiable ones
	FailOnPatch = "patch" // Fail on patch updates and above
	FailOnMinor = "minor" // Fail on minor updates and above
	FailOnMajor = "major" // Fail on major updates only
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
	ArgocdURL       string `mapstructure:"argocd_url"`
	ArgocdAuthToken string `mapstructure:"argocd_auth_token"` // API token, used instead of username/password when set
	ArgocdUsername  string `mapstructure:"argocd_username"`
	ArgocdPassword  string `mapstructure:"argocd_password"`
	ArgocdInsecure  bool   `mapstructure:"argocd_insecure"` // Skip TLS verification

	// Search scope
	Projects []string          `mapstructure:"projects"`  // List of projects to check, or ["*"] for all
	AppNames []string          `mapstructure:"app_names"` // List of app names to check, or ["*"] for all
	Labels   map[string]string `mapstructure:"labels"`    // Label filters

	// Notification settings
	NotificationChannel string `mapstructure:"notification_channel"` // "slack", "webhook", or empty

	// Slack settings
	SlackWebhook string `mapstructure:"slack_webhook"`

	// Generic Webhook settings
	WebhookURL string `mapstructure:"webhook_url"`

	// General settings
	Verbosity    string `mapstructure:"verbosity"`
	LogFormat    string `mapstructure:"log_format"`    // Log format: "json" or "text" (default: "json")
	SourceName   string `mapstructure:"source_name"`   // Optional: narrows a multi-source application down to the source of this name
	Concurrency  int    `mapstructure:"concurrency"`   // Number of concurrent workers for checking applications
	NotifyOn     string `mapstructure:"notify_on"`     // Widest bump to report: "major", "minor", "patch" (default: "major")
	OutputFormat string `mapstructure:"output_format"` // Output format: "table", "json", "markdown" (default: "table")
	FailOn       string `mapstructure:"fail_on"`       // Exit with code 2 on updates of this severity or higher: "none", "any", "patch", "minor", "major" (default: "none")
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
	viper.SetDefault("verbosity", VerbosityOff)
	viper.SetDefault("argocd_insecure", false)
	viper.SetDefault("concurrency", 10)

	// String defaults
	viper.SetDefault("source_name", "")
	viper.SetDefault("notify_on", NotifyOnMajor)
	viper.SetDefault("output_format", OutputFormatTable)
	viper.SetDefault("fail_on", FailOnNone)
	viper.SetDefault("log_format", LogFormatJSON)
	viper.SetDefault("argocd_url", "")
	viper.SetDefault("argocd_auth_token", "")
	viper.SetDefault("argocd_username", "")
	viper.SetDefault("argocd_password", "")
	viper.SetDefault("notification_channel", "")
	viper.SetDefault("slack_webhook", "")
	viper.SetDefault("webhook_url", "")

	// Array/slice defaults
	viper.SetDefault("projects", []string{"*"})
	viper.SetDefault("app_names", []string{"*"})

	// Map defaults
	viper.SetDefault("labels", map[string]string{})
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

	// ARGOCD_AUTH_TOKEN is the variable the ArgoCD CLI itself uses, so it is accepted
	// without the AG_ prefix on top of the usual AG_ARGOCD_AUTH_TOKEN.
	// Both spellings are bound because registerFlagAliases later turns the flag name
	// into the real key behind "argocd_auth_token".
	_ = viper.BindEnv("argocd_auth_token", "AG_ARGOCD_AUTH_TOKEN", "ARGOCD_AUTH_TOKEN")
	_ = viper.BindEnv("argocd-auth-token", "AG_ARGOCD_AUTH_TOKEN", "ARGOCD_AUTH_TOKEN")

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
	viper.RegisterAlias("argocd_auth_token", "argocd-auth-token")
	viper.RegisterAlias("argocd_username", "argocd-username")
	viper.RegisterAlias("argocd_password", "argocd-password")
	viper.RegisterAlias("argocd_insecure", "argocd-insecure")
	viper.RegisterAlias("app_names", "app-names")
	viper.RegisterAlias("notification_channel", "notification-channel")
	viper.RegisterAlias("notify_on", "notify-on")
	viper.RegisterAlias("output_format", "output-format")
	viper.RegisterAlias("log_format", "log-format")
	viper.RegisterAlias("fail_on", "fail-on")
}

// validateConfig validates the loaded configuration
func validateConfig(cfg *Config) error {
	// Validate required fields
	if cfg.ArgocdURL == "" {
		return fmt.Errorf("argocd_url is required")
	}
	// Authentication is either a token or a username/password pair.
	// A whitespace-only token counts as no token, and the trimmed value is what the client gets.
	cfg.ArgocdAuthToken = strings.TrimSpace(cfg.ArgocdAuthToken)
	if cfg.ArgocdAuthToken == "" {
		if cfg.ArgocdUsername == "" {
			return fmt.Errorf("argocd_username is required when argocd_auth_token is not set")
		}
		if cfg.ArgocdPassword == "" {
			return fmt.Errorf("argocd_password is required when argocd_auth_token is not set")
		}
	}

	// Validate notify-on
	if cfg.NotifyOn != "" && cfg.NotifyOn != NotifyOnMajor && cfg.NotifyOn != NotifyOnMinor && cfg.NotifyOn != NotifyOnPatch {
		return fmt.Errorf("notify_on must be one of: '%s', '%s', '%s' (got: '%s')", NotifyOnMajor, NotifyOnMinor, NotifyOnPatch, cfg.NotifyOn)
	}
	// Normalize empty to "major"
	if cfg.NotifyOn == "" {
		cfg.NotifyOn = NotifyOnMajor
	}

	// Validate output format
	if cfg.OutputFormat != "" && cfg.OutputFormat != OutputFormatTable && cfg.OutputFormat != OutputFormatJSON && cfg.OutputFormat != OutputFormatMarkdown {
		return fmt.Errorf("output_format must be one of: '%s', '%s', '%s' (got: '%s')", OutputFormatTable, OutputFormatJSON, OutputFormatMarkdown, cfg.OutputFormat)
	}
	// Normalize empty to "table"
	if cfg.OutputFormat == "" {
		cfg.OutputFormat = OutputFormatTable
	}

	// Validate fail-on policy
	switch cfg.FailOn {
	case "":
		// Normalize empty to "none": exit code 2 is opt-in
		cfg.FailOn = FailOnNone
	case FailOnNone, FailOnAny, FailOnPatch, FailOnMinor, FailOnMajor:
	default:
		return fmt.Errorf("fail_on must be one of: '%s', '%s', '%s', '%s', '%s' (got: '%s')", FailOnNone, FailOnAny, FailOnPatch, FailOnMinor, FailOnMajor, cfg.FailOn)
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
	case "":
		// No channel configured: the results only go to the console
	case NotificationChannelSlack:
		if cfg.SlackWebhook == "" {
			return fmt.Errorf("slack_webhook is required when notification_channel is '%s'", NotificationChannelSlack)
		}
	case NotificationChannelWebhook:
		if cfg.WebhookURL == "" {
			return fmt.Errorf("webhook_url is required when notification_channel is '%s'", NotificationChannelWebhook)
		}
	default:
		// Rejecting an unknown channel keeps a run configured for a removed one
		// (telegram, email, teams) from silently reporting to nobody.
		return fmt.Errorf("notification_channel must be one of: '%s', '%s' or empty (got: '%s')", NotificationChannelSlack, NotificationChannelWebhook, cfg.NotificationChannel)
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
