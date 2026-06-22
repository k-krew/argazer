package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	cmdpkg "argazer/cmd"
	"argazer/internal/argocd"
	"argazer/internal/auth"
	"argazer/internal/config"
	"argazer/internal/helm"
	"argazer/internal/notification"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	var rootCmd = &cobra.Command{
		Use:   "argazer",
		Short: "ArgoCD Application Gazer - Monitor Helm chart versions in ArgoCD applications",
		Long: `Argazer connects to ArgoCD via API and checks all applications for Helm chart updates.
It can filter by projects, application names, and labels, and send notifications via Telegram, Email, Slack, Microsoft Teams, or generic webhooks.`,
		RunE: run,
	}

	// Add version command
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("argazer version %s (commit: %s)\n", version, commit)
		},
	})

	// Add configure command
	rootCmd.AddCommand(cmdpkg.NewConfigureCmd())

	// Add flags
	rootCmd.Flags().StringP("config", "c", "", "Configuration file path")
	rootCmd.Flags().String("argocd-url", "", "ArgoCD server URL")
	rootCmd.Flags().String("argocd-username", "", "ArgoCD username")
	rootCmd.Flags().String("argocd-password", "", "ArgoCD password")
	rootCmd.Flags().Bool("argocd-insecure", false, "Skip TLS verification")
	rootCmd.Flags().StringSlice("projects", []string{"*"}, "Projects to check (comma-separated, or '*' for all)")
	rootCmd.Flags().StringSlice("app-names", []string{"*"}, "Application names to check (comma-separated, or '*' for all)")
	rootCmd.Flags().String("notification-channel", "", "Notification channel: 'telegram', 'email', 'slack', 'teams', 'webhook', or empty for console only")
	rootCmd.Flags().Int("concurrency", 10, "Number of concurrent workers for checking applications")
	rootCmd.Flags().String("version-constraint", "major", "Version constraint: 'major' (all), 'minor' (same major), 'patch' (same major.minor)")
	rootCmd.Flags().StringP("output-format", "o", "table", "Output format: 'table', 'json', or 'markdown'")
	rootCmd.Flags().StringP("log-format", "l", "json", "Log format: 'json' or 'text'")
	rootCmd.Flags().StringP("verbosity", "v", "normal", "Verbosity level: 'full' (all logs), 'normal' (necessary logs), 'off' (only results)")

	// Bind flags to viper
	if err := viper.BindPFlags(rootCmd.Flags()); err != nil {
		logrus.WithError(err).Fatal("Failed to bind flags")
	}

	if err := rootCmd.Execute(); err != nil {
		logrus.Fatal(err)
	}
}

func run(cmd *cobra.Command, args []string) error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Set up logging
	logger := setupLogging(cfg.Verbosity, cfg.LogFormat)

	logger.WithFields(logrus.Fields{
		"argocd_url":   cfg.ArgocdURL,
		"projects":     cfg.Projects,
		"app_names":    cfg.AppNames,
		"labels":       cfg.Labels,
		"notification": cfg.NotificationChannel,
		"version":      version,
	}).Info("Starting Argazer")

	// Set up context with signal handling for graceful shutdown
	ctx, cancel := setupSignalHandler(logger)
	defer cancel()

	// Initialize clients
	clients, err := initializeClients(ctx, cfg, logger)
	if err != nil {
		return err
	}

	// Fetch applications from ArgoCD
	apps, err := fetchApplications(ctx, clients.argocd, cfg, logger)
	if err != nil {
		return err
	}

	// Check applications for updates (with concurrency)
	results := checkApplicationsConcurrently(ctx, apps, clients.helm, cfg, logger)

	// Output results to console
	if err := outputResults(results, cfg.OutputFormat, os.Stdout); err != nil {
		return fmt.Errorf("failed to output results: %w", err)
	}

	// Send notifications if configured
	if clients.notifier != nil {
		if err := sendNotifications(ctx, clients.notifier, results, logger); err != nil {
			logger.WithError(err).Warn("Failed to send notifications")
		}
	}

	logger.WithField("total_checked", len(results)).Info("Argazer completed")

	return nil
}

// clients holds all initialized clients
type clients struct {
	argocd   *argocd.Client
	helm     *helm.Checker
	notifier notification.Notifier
}

// initializeClients creates all required clients (ArgoCD, Helm, Notifier)
// Context is reserved for future use when client initialization becomes cancellable
func initializeClients(_ context.Context, cfg *config.Config, logger *logrus.Entry) (*clients, error) {
	c := &clients{}

	// Create authentication provider
	authLogger := logger.WithField("component", "auth")

	// Convert config auth to auth provider format
	var configAuth []auth.ConfigAuth
	for _, ra := range cfg.RepositoryAuth {
		configAuth = append(configAuth, auth.ConfigAuth{
			URL:      ra.URL,
			Username: ra.Username,
			Password: ra.Password,
		})
	}

	authProvider, err := auth.NewProvider(configAuth, authLogger)
	if err != nil {
		return nil, fmt.Errorf("failed to create auth provider: %w", err)
	}

	// Create ArgoCD API client
	argoLogger := logger.WithField("component", "argocd")
	argoClient, err := argocd.NewClient(cfg.ArgocdURL, cfg.ArgocdUsername, cfg.ArgocdPassword, cfg.ArgocdInsecure, argoLogger)
	if err != nil {
		return nil, fmt.Errorf("failed to create ArgoCD client: %w", err)
	}
	c.argocd = argoClient

	// Create helm checker
	helmLogger := logger.WithField("component", "helm")
	helmChecker, err := helm.NewChecker(authProvider, helmLogger)
	if err != nil {
		return nil, fmt.Errorf("failed to create helm checker: %w", err)
	}
	c.helm = helmChecker

	// Create notifier based on configuration
	if cfg.NotificationChannel != "" {
		notifierLogger := logger.WithField("component", "notifier")
		var notifier notification.Notifier

		switch cfg.NotificationChannel {
		case "telegram":
			notifier = notification.NewTelegramNotifier(cfg.TelegramWebhook, cfg.TelegramChatID, notifierLogger)
			logger.Info("Using Telegram notifications")
		case "email":
			notifier = notification.NewEmailNotifier(
				cfg.EmailSmtpHost,
				cfg.EmailSmtpPort,
				cfg.EmailSmtpUsername,
				cfg.EmailSmtpPassword,
				cfg.EmailFrom,
				cfg.EmailTo,
				cfg.EmailUseTLS,
				notifierLogger,
			)
			logger.Info("Using Email notifications")
		case "slack":
			notifier = notification.NewSlackNotifier(cfg.SlackWebhook, notifierLogger)
			logger.Info("Using Slack notifications")
		case "teams":
			notifier = notification.NewTeamsNotifier(cfg.TeamsWebhook, notifierLogger)
			logger.Info("Using Microsoft Teams notifications")
		case "webhook":
			notifier = notification.NewWebhookNotifier(cfg.WebhookURL, notifierLogger)
			logger.Info("Using generic webhook notifications")
		default:
			logger.Warnf("Unknown notification channel: %s", cfg.NotificationChannel)
		}

		c.notifier = notifier
	}

	return c, nil
}

// fetchApplications retrieves applications from ArgoCD based on filters
func fetchApplications(ctx context.Context, client *argocd.Client, cfg *config.Config, logger *logrus.Entry) ([]*v1alpha1.Application, error) {
	apps, err := client.ListApplications(ctx, argocd.FilterOptions{
		Projects: cfg.Projects,
		AppNames: cfg.AppNames,
		Labels:   cfg.Labels,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list applications: %w", err)
	}

	logger.WithField("count", len(apps)).Info("Found applications")
	return apps, nil
}

// ApplicationCheckResult holds the result of checking an application
type ApplicationCheckResult struct {
	AppName                    string `json:"app_name"`
	Project                    string `json:"project"`
	ChartName                  string `json:"chart_name"`
	CurrentVersion             string `json:"current_version"`
	LatestVersion              string `json:"latest_version"`
	RepoURL                    string `json:"repo_url"`
	HasUpdate                  bool   `json:"has_update"`
	Error                      string `json:"error,omitempty"`               // Changed from error to string for proper JSON serialization
	ConstraintApplied          string `json:"constraint_applied"`            // Version constraint used: "major", "minor", or "patch"
	HasUpdateOutsideConstraint bool   `json:"has_update_outside_constraint"` // True if updates exist outside the constraint
	LatestVersionAll           string `json:"latest_version_all,omitempty"`  // Latest version without constraint (if different)
}

// checkApplicationsConcurrently checks multiple applications in parallel using a worker pool
func checkApplicationsConcurrently(ctx context.Context, apps []*v1alpha1.Application, helmChecker *helm.Checker, cfg *config.Config, logger *logrus.Entry) []ApplicationCheckResult {
	numWorkers := cfg.Concurrency
	if numWorkers <= 0 {
		numWorkers = 10 // Fallback to default
	}

	logger.WithField("concurrency", numWorkers).Debug("Starting concurrent application checks")

	// Create channels for work distribution
	appChan := make(chan *v1alpha1.Application, len(apps))
	resultChan := make(chan ApplicationCheckResult, len(apps))

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			workerLogger := logger.WithField("worker_id", workerID)
			for app := range appChan {
				result := checkApplication(ctx, app, helmChecker, cfg, workerLogger)
				resultChan <- result
			}
		}(i)
	}

	// Send applications to workers
	for _, app := range apps {
		appChan <- app
	}
	close(appChan)

	// Wait for all workers to finish
	wg.Wait()
	close(resultChan)

	// Collect results
	results := make([]ApplicationCheckResult, 0, len(apps))
	for result := range resultChan {
		results = append(results, result)
	}

	return results
}

// checkApplication checks a single application for Helm chart updates
// Returns an ApplicationCheckResult with an empty AppName if the application should be skipped (non-Helm app)
func checkApplication(ctx context.Context, app *v1alpha1.Application, helmChecker *helm.Checker, cfg *config.Config, logger *logrus.Entry) ApplicationCheckResult {
	appLogger := logger.WithFields(logrus.Fields{
		"app_name": app.Name,
		"project":  app.Spec.Project,
	})

	appLogger.Info("Processing application")

	// Find Helm source
	helmSource := findHelmSource(app, cfg.SourceName, appLogger)
	if helmSource == nil {
		appLogger.Info("Application does not use Helm charts, skipping")
		// Return empty result with no AppName - signals to skip this app
		// This will be filtered out during result processing
		return ApplicationCheckResult{}
	}

	// Determine chart name: for Helm repos use Chart field, for Git repos use Path
	chartName := helmSource.Chart
	if chartName == "" && helmSource.Path != "" {
		// Git-based Helm source - use the path as chart name
		chartName = helmSource.Path
	}

	result := ApplicationCheckResult{
		AppName:           app.Name,
		Project:           app.Spec.Project,
		ChartName:         chartName,
		CurrentVersion:    helmSource.TargetRevision,
		RepoURL:           helmSource.RepoURL,
		ConstraintApplied: cfg.VersionConstraint,
	}

	appLogger = appLogger.WithFields(logrus.Fields{
		"chart_name":    chartName,
		"chart_version": helmSource.TargetRevision,
		"repo_url":      helmSource.RepoURL,
		"constraint":    cfg.VersionConstraint,
	})

	appLogger.Info("Found Helm-based application")

	// Check for newer version with constraint
	constraintResult, err := helmChecker.GetLatestVersionWithConstraint(
		ctx,
		helmSource.RepoURL,
		chartName,
		helmSource.TargetRevision,
		cfg.VersionConstraint,
	)
	if err != nil {
		appLogger.WithError(err).Error("Failed to check Helm version")
		result.Error = err.Error()
		return result
	}

	result.LatestVersion = constraintResult.LatestVersion
	result.LatestVersionAll = constraintResult.LatestVersionAll
	result.HasUpdateOutsideConstraint = constraintResult.HasUpdateOutsideConstraint

	if constraintResult.LatestVersion != helmSource.TargetRevision {
		appLogger.WithFields(logrus.Fields{
			"current_version":               helmSource.TargetRevision,
			"latest_version":                constraintResult.LatestVersion,
			"latest_version_all":            constraintResult.LatestVersionAll,
			"has_update_outside_constraint": constraintResult.HasUpdateOutsideConstraint,
		}).Warn("Update available!")
		result.HasUpdate = true
	} else {
		if constraintResult.HasUpdateOutsideConstraint {
			appLogger.WithFields(logrus.Fields{
				"current_version":    helmSource.TargetRevision,
				"latest_version_all": constraintResult.LatestVersionAll,
				"constraint":         cfg.VersionConstraint,
			}).Info("Application is up to date within constraint, but updates exist outside constraint")
		} else {
			appLogger.Info("Application is up to date")
		}
	}

	return result
}

// findHelmSource finds the Helm source in an ArgoCD application
func findHelmSource(app *v1alpha1.Application, sourceName string, logger *logrus.Entry) *v1alpha1.ApplicationSource {
	// Helper function to check if a source is Helm-based
	isHelmSource := func(source *v1alpha1.ApplicationSource) bool {
		// Check if it's a Helm repository source (has Chart field)
		if source.Chart != "" {
			return true
		}
		// Check if it's a Git repository with Helm (has Helm parameters)
		if source.Helm != nil {
			return true
		}
		return false
	}

	// Check if it's a single source application with Helm
	if app.Spec.Source != nil && isHelmSource(app.Spec.Source) {
		return app.Spec.Source
	}

	// Check multi-source applications
	if app.Spec.Sources != nil {
		// If sourceName is specified, look for that specific source first
		if sourceName != "" {
			for i := range app.Spec.Sources {
				source := &app.Spec.Sources[i]
				// Match by name AND ensure it's a Helm chart
				if source.Name == sourceName && isHelmSource(source) {
					logger.WithFields(logrus.Fields{
						"app":         app.Name,
						"source_name": source.Name,
						"chart":       source.Chart,
						"repo":        source.RepoURL,
					}).Debug("Found matching Helm source by name")
					return source
				}
			}
		}

		// Fallback: find any Helm source
		for i := range app.Spec.Sources {
			source := &app.Spec.Sources[i]
			if isHelmSource(source) {
				logger.WithFields(logrus.Fields{
					"app":         app.Name,
					"source_name": source.Name,
					"chart":       source.Chart,
					"repo":        source.RepoURL,
				}).Debug("Found Helm source (fallback)")
				return source
			}
		}
	}

	return nil
}

// scanResults holds statistics about the scan
type scanResults struct {
	total    int
	upToDate int
	updates  int
	skipped  int
}

// categorizedResults holds the processed and categorized check results
type categorizedResults struct {
	updatesAvailable       []ApplicationCheckResult
	upToDateWithConstraint []ApplicationCheckResult
	upToDateNoConstraint   []ApplicationCheckResult
	errors                 []ApplicationCheckResult
	stats                  scanResults
}

// processResults categorizes and processes the raw check results
// Results with empty AppName are skipped (these are non-Helm applications)
func processResults(results []ApplicationCheckResult) categorizedResults {
	cat := categorizedResults{
		stats: scanResults{},
	}

	for _, result := range results {
		// Skip results with no AppName - these are non-Helm apps intentionally filtered out
		if result.AppName == "" {
			continue
		}

		cat.stats.total++

		if result.Error != "" {
			cat.stats.skipped++
			cat.errors = append(cat.errors, result)
		} else if result.HasUpdate {
			cat.stats.updates++
			cat.updatesAvailable = append(cat.updatesAvailable, result)
		} else {
			cat.stats.upToDate++
			if result.HasUpdateOutsideConstraint {
				cat.upToDateWithConstraint = append(cat.upToDateWithConstraint, result)
			} else {
				cat.upToDateNoConstraint = append(cat.upToDateNoConstraint, result)
			}
		}
	}

	return cat
}

// outputResults displays the results to console in the specified format
func outputResults(results []ApplicationCheckResult, format string, w io.Writer) error {
	categorized := processResults(results)

	switch format {
	case config.OutputFormatJSON:
		return renderJSON(categorized, w)
	case config.OutputFormatMarkdown:
		return renderMarkdown(categorized, w)
	case config.OutputFormatTable:
		return renderTable(categorized, w)
	default:
		return fmt.Errorf("unknown output format: %s", format)
	}
}

// renderTable displays results in a formatted table (original format)
func renderTable(cat categorizedResults, w io.Writer) error {
	// Display summary
	if _, err := fmt.Fprintln(w, "\n"+strings.Repeat("=", 80)); err != nil {
		return fmt.Errorf("failed to write table: %w", err)
	}
	fmt.Fprintln(w, "ARGAZER SCAN RESULTS")
	fmt.Fprintln(w, strings.Repeat("=", 80))
	fmt.Fprintf(w, "\nTotal applications checked: %d\n\n", cat.stats.total)
	fmt.Fprintf(w, "Up to date: %d\n", cat.stats.upToDate)
	fmt.Fprintf(w, "Updates available: %d\n", cat.stats.updates)
	fmt.Fprintf(w, "Skipped: %d\n\n", cat.stats.skipped)

	// Display updates
	if cat.stats.updates > 0 {
		fmt.Fprintln(w, strings.Repeat("-", 80))
		fmt.Fprintln(w, "APPLICATIONS WITH UPDATES AVAILABLE:")
		fmt.Fprintln(w, strings.Repeat("-", 80))

		for _, result := range cat.updatesAvailable {
			fmt.Fprintf(w, "\nApplication: %s\n", result.AppName)
			fmt.Fprintf(w, "  Project: %s\n", result.Project)
			fmt.Fprintf(w, "  Chart: %s\n", result.ChartName)
			fmt.Fprintf(w, "  Current Version: %s\n", result.CurrentVersion)
			fmt.Fprintf(w, "  Latest Version: %s\n", result.LatestVersion)
			if result.ConstraintApplied != "major" && result.ConstraintApplied != "" {
				fmt.Fprintf(w, "  Version Constraint: %s\n", result.ConstraintApplied)
			}
			if result.HasUpdateOutsideConstraint && result.LatestVersionAll != "" {
				fmt.Fprintf(w, "  Note: Version %s available outside constraint\n", result.LatestVersionAll)
			}
			fmt.Fprintf(w, "  Repository: %s\n", result.RepoURL)
		}
	}

	// Display apps that are up to date but have updates outside constraint
	if len(cat.upToDateWithConstraint) > 0 {
		fmt.Fprintln(w, "\n"+strings.Repeat("-", 80))
		fmt.Fprintln(w, "UP TO DATE (with updates outside constraint):")
		fmt.Fprintln(w, strings.Repeat("-", 80))

		for _, result := range cat.upToDateWithConstraint {
			fmt.Fprintf(w, "\nApplication: %s\n", result.AppName)
			fmt.Fprintf(w, "  Project: %s\n", result.Project)
			fmt.Fprintf(w, "  Chart: %s\n", result.ChartName)
			fmt.Fprintf(w, "  Current Version: %s\n", result.CurrentVersion)
			fmt.Fprintf(w, "  Status: Up to date within '%s' constraint\n", result.ConstraintApplied)
			if result.LatestVersionAll != "" {
				fmt.Fprintf(w, "  Note: Version %s available outside constraint\n", result.LatestVersionAll)
			}
			fmt.Fprintf(w, "  Repository: %s\n", result.RepoURL)
		}
	}

	// Display skipped applications
	if cat.stats.skipped > 0 {
		fmt.Fprintln(w, "\n"+strings.Repeat("-", 80))
		fmt.Fprintln(w, "APPLICATIONS SKIPPED (Unable to check):")
		fmt.Fprintln(w, strings.Repeat("-", 80))

		for _, result := range cat.errors {
			fmt.Fprintf(w, "\nApplication: %s\n", result.AppName)
			fmt.Fprintf(w, "  Project: %s\n", result.Project)
			fmt.Fprintf(w, "  Chart: %s\n", result.ChartName)
			fmt.Fprintf(w, "  Repository: %s\n", result.RepoURL)
			fmt.Fprintf(w, "  Reason: %s\n", result.Error)
		}
	}

	fmt.Fprintln(w, "\n"+strings.Repeat("=", 80)+"\n")
	return nil
}

// renderJSON displays results in JSON format
func renderJSON(cat categorizedResults, w io.Writer) error {
	// Create JSON output structure
	type JSONOutput struct {
		Summary struct {
			Total            int `json:"total"`
			UpToDate         int `json:"up_to_date"`
			UpdatesAvailable int `json:"updates_available"`
			Skipped          int `json:"skipped"`
		} `json:"summary"`
		UpdatesAvailable        []ApplicationCheckResult `json:"updates_available"`
		UpToDateWithConstraint  []ApplicationCheckResult `json:"up_to_date_with_constraint"`
		UpToDateNoUpdateOutside []ApplicationCheckResult `json:"up_to_date"`
		Errors                  []ApplicationCheckResult `json:"errors"`
	}

	output := JSONOutput{
		UpdatesAvailable:        cat.updatesAvailable,
		UpToDateWithConstraint:  cat.upToDateWithConstraint,
		UpToDateNoUpdateOutside: cat.upToDateNoConstraint,
		Errors:                  cat.errors,
	}

	output.Summary.Total = cat.stats.total
	output.Summary.UpToDate = cat.stats.upToDate
	output.Summary.UpdatesAvailable = cat.stats.updates
	output.Summary.Skipped = cat.stats.skipped

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		return fmt.Errorf("failed to encode JSON: %w", err)
	}

	return nil
}

// renderMarkdown displays results in Markdown format
func renderMarkdown(cat categorizedResults, w io.Writer) error {
	// Display summary
	if _, err := fmt.Fprintln(w, "# Argazer Scan Results"); err != nil {
		return fmt.Errorf("failed to write markdown: %w", err)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Summary")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "- **Total applications checked:** %d\n", cat.stats.total)
	fmt.Fprintf(w, "- **Up to date:** %d\n", cat.stats.upToDate)
	fmt.Fprintf(w, "- **Updates available:** %d\n", cat.stats.updates)
	fmt.Fprintf(w, "- **Skipped:** %d\n\n", cat.stats.skipped)

	// Display updates
	if cat.stats.updates > 0 {
		fmt.Fprintln(w, "## Applications with Updates Available")
		fmt.Fprintln(w)

		for _, result := range cat.updatesAvailable {
			fmt.Fprintf(w, "### %s\n\n", result.AppName)
			fmt.Fprintf(w, "| Field | Value |\n")
			fmt.Fprintf(w, "|-------|-------|\n")
			fmt.Fprintf(w, "| **Project** | %s |\n", result.Project)
			fmt.Fprintf(w, "| **Chart** | %s |\n", result.ChartName)
			fmt.Fprintf(w, "| **Current Version** | %s |\n", result.CurrentVersion)
			fmt.Fprintf(w, "| **Latest Version** | %s |\n", result.LatestVersion)
			if result.ConstraintApplied != "major" && result.ConstraintApplied != "" {
				fmt.Fprintf(w, "| **Version Constraint** | %s |\n", result.ConstraintApplied)
			}
			if result.HasUpdateOutsideConstraint && result.LatestVersionAll != "" {
				fmt.Fprintf(w, "| **Latest Version (all)** | %s |\n", result.LatestVersionAll)
			}
			fmt.Fprintf(w, "| **Repository** | %s |\n\n", result.RepoURL)
		}
	}

	// Display apps that are up to date but have updates outside constraint
	if len(cat.upToDateWithConstraint) > 0 {
		fmt.Fprintln(w, "## Up to Date (with updates outside constraint)")
		fmt.Fprintln(w)

		for _, result := range cat.upToDateWithConstraint {
			fmt.Fprintf(w, "### %s\n\n", result.AppName)
			fmt.Fprintf(w, "| Field | Value |\n")
			fmt.Fprintf(w, "|-------|-------|\n")
			fmt.Fprintf(w, "| **Project** | %s |\n", result.Project)
			fmt.Fprintf(w, "| **Chart** | %s |\n", result.ChartName)
			fmt.Fprintf(w, "| **Current Version** | %s |\n", result.CurrentVersion)
			fmt.Fprintf(w, "| **Status** | Up to date within '%s' constraint |\n", result.ConstraintApplied)
			if result.LatestVersionAll != "" {
				fmt.Fprintf(w, "| **Latest Version (all)** | %s |\n", result.LatestVersionAll)
			}
			fmt.Fprintf(w, "| **Repository** | %s |\n\n", result.RepoURL)
		}
	}

	// Display skipped applications
	if cat.stats.skipped > 0 {
		fmt.Fprintln(w, "## Applications Skipped")
		fmt.Fprintln(w)

		for _, result := range cat.errors {
			fmt.Fprintf(w, "### %s\n\n", result.AppName)
			fmt.Fprintf(w, "| Field | Value |\n")
			fmt.Fprintf(w, "|-------|-------|\n")
			fmt.Fprintf(w, "| **Project** | %s |\n", result.Project)
			fmt.Fprintf(w, "| **Chart** | %s |\n", result.ChartName)
			fmt.Fprintf(w, "| **Repository** | %s |\n", result.RepoURL)
			fmt.Fprintf(w, "| **Error** | %s |\n\n", result.Error)
		}
	}

	return nil
}

// sendNotifications sends notifications via the configured notifier
func sendNotifications(ctx context.Context, notifier notification.Notifier, results []ApplicationCheckResult, logger *logrus.Entry) error {
	// Check if there are updates in a single loop
	var updatesAvailable []ApplicationCheckResult
	for _, result := range results {
		if result.HasUpdate {
			updatesAvailable = append(updatesAvailable, result)
		}
	}

	if len(updatesAvailable) == 0 {
		logger.Info("No updates available, skipping notification")
		return nil
	}

	// Convert to notification format
	var updates []notification.ApplicationUpdate
	for _, result := range updatesAvailable {
		updates = append(updates, notification.ApplicationUpdate{
			AppName:                    result.AppName,
			Project:                    result.Project,
			ChartName:                  result.ChartName,
			CurrentVersion:             result.CurrentVersion,
			LatestVersion:              result.LatestVersion,
			RepoURL:                    result.RepoURL,
			ConstraintApplied:          result.ConstraintApplied,
			HasUpdateOutsideConstraint: result.HasUpdateOutsideConstraint,
			LatestVersionAll:           result.LatestVersionAll,
		})
	}

	// Build notification messages using the formatter
	formatter := notification.NewMessageFormatter()
	messages := formatter.FormatMessages(updates)

	logger.WithField("message_count", len(messages)).Info("Sending notifications")

	// Send all messages
	for i, msg := range messages {
		subject := fmt.Sprintf("Argazer Notification: %d Helm Chart Update(s) Available", len(updatesAvailable))
		if len(messages) > 1 {
			subject = fmt.Sprintf("Argazer Notification [%d/%d]: %d Update(s)", i+1, len(messages), len(updatesAvailable))
		}

		if err := notifier.Send(ctx, subject, msg); err != nil {
			return fmt.Errorf("failed to send notification %d/%d: %w", i+1, len(messages), err)
		}
	}

	logger.Info("Successfully sent all notifications")
	return nil
}

// setupLogging configures the logging system
func setupLogging(verbosity string, format string) *logrus.Entry {
	switch verbosity {
	case config.VerbosityFull:
		logrus.SetLevel(logrus.DebugLevel)
	case config.VerbosityOff:
		logrus.SetOutput(io.Discard)
	default:
		logrus.SetLevel(logrus.InfoLevel)
	}

	// Set formatter based on configuration
	if format == config.LogFormatText {
		logrus.SetFormatter(&logrus.TextFormatter{
			FullTimestamp: true,
		})
	} else {
		logrus.SetFormatter(&logrus.JSONFormatter{})
	}

	// Return a base logger entry
	return logrus.WithField("service", "argazer")
}

// setupSignalHandler creates a context that is cancelled on SIGINT or SIGTERM
// This allows for graceful shutdown of the application
func setupSignalHandler(logger *logrus.Entry) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-signalChan
		logger.WithField("signal", sig.String()).Info("Received shutdown signal, initiating graceful shutdown...")
		cancel()
	}()

	return ctx, cancel
}
