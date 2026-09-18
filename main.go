package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	cmdpkg "argazer/cmd"
	"argazer/internal/argocd"
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
It can filter by projects, application names, and labels, and send notifications via Telegram, Email, Slack, Microsoft Teams, or generic webhooks.

Exit codes: 0 - nothing to report, 1 - the scan could not be completed, 2 - updates matching --fail-on were found.`,
		RunE: run,
		// Failures are reported by the logger and turned into an exit code below
		SilenceErrors: true,
		SilenceUsage:  true,
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
	rootCmd.Flags().String("argocd-auth-token", "", "ArgoCD API token (alternative to username/password, also read from ARGOCD_AUTH_TOKEN)")
	rootCmd.Flags().Bool("argocd-insecure", false, "Skip TLS verification")
	rootCmd.Flags().StringSlice("projects", []string{"*"}, "Projects to check (comma-separated, or '*' for all)")
	rootCmd.Flags().StringSlice("app-names", []string{"*"}, "Application names to check (comma-separated, or '*' for all)")
	rootCmd.Flags().String("notification-channel", "", "Notification channel: 'telegram', 'email', 'slack', 'teams', 'webhook', or empty for console only")
	rootCmd.Flags().Int("concurrency", 10, "Number of concurrent workers for checking applications")
	rootCmd.Flags().String("version-constraint", "major", "Version constraint: 'major' (all), 'minor' (same major), 'patch' (same major.minor)")
	rootCmd.Flags().StringP("output-format", "o", "table", "Output format: 'table', 'json', or 'markdown'")
	rootCmd.Flags().StringP("log-format", "l", "json", "Log format: 'json' or 'text'")
	rootCmd.Flags().StringP("verbosity", "v", "normal", "Verbosity level: 'full' (all logs), 'normal' (necessary logs), 'off' (only results)")
	rootCmd.Flags().String("fail-on", config.FailOnNone, "Exit with code 2 when updates of this severity or higher are found: 'none' (never), 'any', 'patch', 'minor', 'major'")

	// Bind flags to viper
	if err := viper.BindPFlags(rootCmd.Flags()); err != nil {
		logrus.WithError(err).Fatal("Failed to bind flags")
	}

	if err := rootCmd.Execute(); err != nil {
		var exitErr *exitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.code)
		}
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

	switch code := determineExitCode(results, cfg.FailOn); code {
	case exitCodeScanFailed:
		logger.Warn("Some applications could not be checked, exiting with code 1")
		return &exitError{code: code, message: "some applications could not be checked"}
	case exitCodeUpdatesFound:
		logger.WithField("fail_on", cfg.FailOn).Warn("Updates matching --fail-on were found, exiting with code 2")
		return &exitError{code: code, message: fmt.Sprintf("updates matching --fail-on=%s were found", cfg.FailOn)}
	}

	return nil
}

// Exit codes returned to the shell, so Argazer can act as a CI/CD quality gate.
const (
	exitCodeClean        = 0 // Nothing to report
	exitCodeScanFailed   = 1 // The scan itself could not be completed
	exitCodeUpdatesFound = 2 // Updates matching --fail-on were found
)

// exitError carries an exit code out of run. Any other error leaving run exits with
// exitCodeScanFailed.
type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string {
	return e.message
}

// determineExitCode maps the outcome of a scan to a process exit code. An incomplete scan
// wins over found updates: a run that could not check everything says nothing reliable
// about the applications it did not reach.
func determineExitCode(results []ApplicationCheckResult, failOn string) int {
	cat := processResults(results)

	if cat.stats.skipped > 0 {
		return exitCodeScanFailed
	}

	if failsOn(cat.updatesAvailable, failOn) {
		return exitCodeUpdatesFound
	}

	return exitCodeClean
}

// bumpSeverity ranks how big an update is. A bump that cannot be classified gets the
// lowest rank, so it never reaches a threshold.
var bumpSeverity = map[string]int{
	helm.BumpPatch: 1,
	helm.BumpMinor: 2,
	helm.BumpMajor: 3,
}

// failOnThreshold is the lowest severity each --fail-on value reacts to: asking to fail on
// minor updates also covers the major ones.
var failOnThreshold = map[string]int{
	config.FailOnPatch: bumpSeverity[helm.BumpPatch],
	config.FailOnMinor: bumpSeverity[helm.BumpMinor],
	config.FailOnMajor: bumpSeverity[helm.BumpMajor],
}

// failsOn reports whether any of the available updates is severe enough for the given
// --fail-on value. Updates whose severity cannot be determined (a targetRevision pointing
// at a branch, for instance) only count for 'any'.
func failsOn(updates []ApplicationCheckResult, failOn string) bool {
	if len(updates) == 0 || failOn == "" || failOn == config.FailOnNone {
		return false
	}

	if failOn == config.FailOnAny {
		return true
	}

	threshold, ok := failOnThreshold[failOn]
	if !ok {
		return false
	}

	for _, update := range updates {
		if bumpSeverity[helm.BumpType(update.CurrentVersion, updateTarget(update))] >= threshold {
			return true
		}
	}

	return false
}

// updateTarget returns the version an application would end up on once the update is
// applied. For a revision beyond the target range that is the newest version overall,
// since the range has to be widened past everything it allows.
func updateTarget(result ApplicationCheckResult) string {
	if result.UpdateType == helm.UpdateTypeOutOfRange && result.LatestVersionAll != "" {
		return result.LatestVersionAll
	}

	return result.LatestVersion
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

	// Create ArgoCD API client
	argoLogger := logger.WithField("component", "argocd")
	argoClient, err := argocd.NewClient(cfg.ArgocdURL, cfg.ArgocdUsername, cfg.ArgocdPassword, cfg.ArgocdAuthToken, cfg.ArgocdInsecure, argoLogger)
	if err != nil {
		return nil, fmt.Errorf("failed to create ArgoCD client: %w", err)
	}
	c.argocd = argoClient

	// Create helm checker, which reads chart versions through the ArgoCD API
	helmLogger := logger.WithField("component", "helm")
	helmChecker, err := helm.NewChecker(argoClient, helmLogger)
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
func fetchApplications(ctx context.Context, client *argocd.Client, cfg *config.Config, logger *logrus.Entry) ([]*argocd.Application, error) {
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
	UpdateType                 string `json:"update_type,omitempty"`         // "none", "in-range", "out-of-range" or "pinned"
}

// checkApplicationsConcurrently checks multiple applications in parallel using a worker pool
func checkApplicationsConcurrently(ctx context.Context, apps []*argocd.Application, helmChecker *helm.Checker, cfg *config.Config, logger *logrus.Entry) []ApplicationCheckResult {
	numWorkers := cfg.Concurrency
	if numWorkers <= 0 {
		numWorkers = 10 // Fallback to default
	}

	logger.WithField("concurrency", numWorkers).Debug("Starting concurrent application checks")

	// Create channels for work distribution
	appChan := make(chan *argocd.Application, len(apps))
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
func checkApplication(ctx context.Context, app *argocd.Application, helmChecker *helm.Checker, cfg *config.Config, logger *logrus.Entry) ApplicationCheckResult {
	appLogger := logger.WithFields(logrus.Fields{
		"app_name": app.Metadata.Name,
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
		AppName:           app.Metadata.Name,
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
		app.Spec.Project,
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
	result.UpdateType = constraintResult.UpdateType

	result.HasUpdate = requiresManualUpdate(constraintResult.UpdateType)

	switch {
	case result.HasUpdate:
		appLogger.WithFields(logrus.Fields{
			"current_version":               helmSource.TargetRevision,
			"latest_version":                constraintResult.LatestVersion,
			"latest_version_all":            constraintResult.LatestVersionAll,
			"has_update_outside_constraint": constraintResult.HasUpdateOutsideConstraint,
			"update_type":                   constraintResult.UpdateType,
		}).Warn("Update available!")
	case constraintResult.UpdateType == helm.UpdateTypeInRange:
		appLogger.WithFields(logrus.Fields{
			"current_version": helmSource.TargetRevision,
			"latest_version":  constraintResult.LatestVersion,
		}).Info("Latest version satisfies the target range, ArgoCD applies it automatically")
	case constraintResult.HasUpdateOutsideConstraint:
		appLogger.WithFields(logrus.Fields{
			"current_version":    helmSource.TargetRevision,
			"latest_version_all": constraintResult.LatestVersionAll,
			"constraint":         cfg.VersionConstraint,
		}).Info("Application is up to date within constraint, but updates exist outside constraint")
	default:
		appLogger.Info("Application is up to date")
	}

	return result
}

// requiresManualUpdate reports whether an update needs someone to edit the Application
// manifest. An update inside the targetRevision range is deployed by ArgoCD on its own, so
// only pinned revisions and versions beyond the range require attention.
func requiresManualUpdate(updateType string) bool {
	return updateType == helm.UpdateTypeOutOfRange || updateType == helm.UpdateTypePinned
}

// findHelmSource finds the Helm source in an ArgoCD application
func findHelmSource(app *argocd.Application, sourceName string, logger *logrus.Entry) *argocd.ApplicationSource {
	// Helper function to check if a source is Helm-based
	isHelmSource := func(source *argocd.ApplicationSource) bool {
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

	// A source that carries a ref and no chart of its own holds the values files the other
	// sources point at (the `ref: values` pattern). ArgoCD renders nothing from it, and it
	// has no chart version to look up, so it is never the Helm source.
	isValuesRef := func(source *argocd.ApplicationSource) bool {
		return source.Ref != "" && source.Chart == ""
	}

	found := func(source *argocd.ApplicationSource, message string) *argocd.ApplicationSource {
		logger.WithFields(logrus.Fields{
			"app":         app.Metadata.Name,
			"source_name": source.Name,
			"chart":       source.Chart,
			"repo":        source.RepoURL,
		}).Debug(message)
		return source
	}

	// Check if it's a single source application with Helm
	if app.Spec.Source != nil && isHelmSource(app.Spec.Source) {
		return app.Spec.Source
	}

	// Multi-source applications: a source picked by --source-name wins, then the source
	// that is a Helm chart, and only then a Helm chart kept in a Git repository. Reaching
	// for the chart before anything else is what keeps a Git source carrying Helm options,
	// listed ahead of the chart, from being taken for the chart itself.
	if sourceName != "" {
		for i := range app.Spec.Sources {
			source := &app.Spec.Sources[i]
			if source.Name == sourceName && isHelmSource(source) && !isValuesRef(source) {
				return found(source, "Found matching Helm source by name")
			}
		}
	}

	for i := range app.Spec.Sources {
		if source := &app.Spec.Sources[i]; source.Chart != "" {
			return found(source, "Found the Helm chart source")
		}
	}

	for i := range app.Spec.Sources {
		source := &app.Spec.Sources[i]
		if isHelmSource(source) && !isValuesRef(source) {
			return found(source, "Found a Helm chart in a Git repository")
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
