package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"argazer/internal/argocd"
	"argazer/internal/config"
	"argazer/internal/helm"
	"argazer/internal/notification"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupLogging(t *testing.T) {
	t.Run("full verbosity with JSON", func(t *testing.T) {
		logrus.SetOutput(os.Stderr)
		logger := setupLogging("full", "json")
		require.NotNil(t, logger)
		assert.Equal(t, logrus.DebugLevel, logrus.GetLevel())
	})

	t.Run("normal verbosity with JSON", func(t *testing.T) {
		logrus.SetOutput(os.Stderr)
		logger := setupLogging("normal", "json")
		require.NotNil(t, logger)
		assert.Equal(t, logrus.InfoLevel, logrus.GetLevel())
	})

	t.Run("off verbosity", func(t *testing.T) {
		logger := setupLogging("off", "json")
		require.NotNil(t, logger)
		assert.Equal(t, logrus.StandardLogger().Out, io.Discard)
	})

	t.Run("text format", func(t *testing.T) {
		logrus.SetOutput(os.Stderr)
		logger := setupLogging("normal", "text")
		require.NotNil(t, logger)
		assert.Equal(t, logrus.InfoLevel, logrus.GetLevel())
	})
}

func TestFindHelmSource(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	tests := []struct {
		name       string
		app        *argocd.Application
		sourceName string
		expected   bool
	}{
		{
			name: "single source with helm chart",
			app: &argocd.Application{
				Spec: argocd.ApplicationSpec{
					Source: &argocd.ApplicationSource{
						Chart:          "my-chart",
						RepoURL:        "https://charts.example.com",
						TargetRevision: "1.0.0",
					},
				},
			},
			sourceName: "",
			expected:   true,
		},
		{
			name: "single source without helm chart",
			app: &argocd.Application{
				Spec: argocd.ApplicationSpec{
					Source: &argocd.ApplicationSource{
						RepoURL:        "https://github.com/example/repo",
						TargetRevision: "main",
						Path:           "manifests",
					},
				},
			},
			sourceName: "",
			expected:   false,
		},
		{
			name: "multi-source with helm chart",
			app: &argocd.Application{
				Spec: argocd.ApplicationSpec{
					Sources: []argocd.ApplicationSource{
						{
							RepoURL:        "https://github.com/example/repo",
							TargetRevision: "main",
							Path:           "values",
						},
						{
							Name:           "chart-repo",
							Chart:          "my-chart",
							RepoURL:        "https://charts.example.com",
							TargetRevision: "2.0.0",
						},
					},
				},
			},
			sourceName: "chart-repo",
			expected:   true,
		},
		{
			name: "multi-source fallback to any helm source",
			app: &argocd.Application{
				Spec: argocd.ApplicationSpec{
					Sources: []argocd.ApplicationSource{
						{
							RepoURL:        "https://github.com/example/repo",
							TargetRevision: "main",
							Path:           "values",
						},
						{
							Chart:          "my-chart",
							RepoURL:        "https://charts.example.com",
							TargetRevision: "3.0.0",
						},
					},
				},
			},
			sourceName: "",
			expected:   true,
		},
		{
			name: "multi-source no helm charts",
			app: &argocd.Application{
				Spec: argocd.ApplicationSpec{
					Sources: []argocd.ApplicationSource{
						{
							RepoURL:        "https://github.com/example/repo1",
							TargetRevision: "main",
							Path:           "manifests",
						},
						{
							RepoURL:        "https://github.com/example/repo2",
							TargetRevision: "main",
							Path:           "values",
						},
					},
				},
			},
			sourceName: "",
			expected:   false,
		},
		{
			name: "multi-source named source not found",
			app: &argocd.Application{
				Spec: argocd.ApplicationSpec{
					Sources: []argocd.ApplicationSource{
						{
							Name:           "other-source",
							Chart:          "my-chart",
							RepoURL:        "https://charts.example.com",
							TargetRevision: "1.0.0",
						},
					},
				},
			},
			sourceName: "non-existent",
			expected:   true, // Falls back to finding any helm source
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findHelmSource(tt.app, tt.sourceName, logger)
			if tt.expected {
				assert.NotNil(t, result)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

// TestFindHelmSource_MultiSource checks which source of a multi-source application is
// taken for the Helm chart. The pattern to get right is a chart alongside a Git repository
// holding the values files: the chart has to win no matter which of the two is listed
// first, and a source that is nothing but a `ref` must never be picked.
func TestFindHelmSource_MultiSource(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	chart := argocd.ApplicationSource{
		Name:           "chart",
		Chart:          "my-chart",
		RepoURL:        "https://charts.example.com",
		TargetRevision: "1.2.3",
		Helm:           &argocd.ApplicationSourceHelm{},
	}
	valuesRef := argocd.ApplicationSource{
		Name:           "values",
		RepoURL:        "https://github.com/example/values.git",
		TargetRevision: "main",
		Ref:            "values",
		Helm:           &argocd.ApplicationSourceHelm{},
	}

	tests := []struct {
		name            string
		sources         []argocd.ApplicationSource
		sourceName      string
		expectedRepoURL string // empty means no Helm source is expected
		expectedChart   string
	}{
		{
			name:            "values ref listed before the chart is ignored",
			sources:         []argocd.ApplicationSource{valuesRef, chart},
			expectedRepoURL: chart.RepoURL,
			expectedChart:   "my-chart",
		},
		{
			name:            "values ref listed after the chart is ignored",
			sources:         []argocd.ApplicationSource{chart, valuesRef},
			expectedRepoURL: chart.RepoURL,
			expectedChart:   "my-chart",
		},
		{
			name:            "a values ref cannot be picked by name",
			sources:         []argocd.ApplicationSource{valuesRef, chart},
			sourceName:      "values",
			expectedRepoURL: chart.RepoURL,
			expectedChart:   "my-chart",
		},
		{
			name:            "the named chart wins over the other charts",
			sources:         []argocd.ApplicationSource{chart, {Name: "other-chart", Chart: "other", RepoURL: "https://other.example.com"}},
			sourceName:      "other-chart",
			expectedRepoURL: "https://other.example.com",
			expectedChart:   "other",
		},
		{
			name: "a chart in a Git repository is found when there is no chart source",
			sources: []argocd.ApplicationSource{
				valuesRef,
				{Name: "git-chart", RepoURL: "https://github.com/example/charts.git", Path: "charts/my-chart", Helm: &argocd.ApplicationSourceHelm{}},
			},
			expectedRepoURL: "https://github.com/example/charts.git",
		},
		{
			name:    "nothing but a values ref is no Helm source",
			sources: []argocd.ApplicationSource{valuesRef},
		},
		{
			name: "plain manifests are no Helm source",
			sources: []argocd.ApplicationSource{
				{RepoURL: "https://github.com/example/repo", Path: "manifests"},
				valuesRef,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &argocd.Application{
				Metadata: argocd.ApplicationMetadata{Name: "multi-source-app"},
				Spec:     argocd.ApplicationSpec{Sources: tt.sources},
			}

			source := findHelmSource(app, tt.sourceName, logger)

			if tt.expectedRepoURL == "" {
				assert.Nil(t, source)
				return
			}

			require.NotNil(t, source)
			assert.Equal(t, tt.expectedRepoURL, source.RepoURL)
			assert.Equal(t, tt.expectedChart, source.Chart)
			assert.Empty(t, source.Ref, "a source carrying only a ref holds no chart")
		})
	}
}

func TestOutputResults(t *testing.T) {
	// Test with various result scenarios
	tests := []struct {
		name    string
		results []ApplicationCheckResult
		formats []string
	}{
		{
			name:    "empty results",
			results: []ApplicationCheckResult{},
			formats: []string{"table", "json", "markdown"},
		},
		{
			name: "all up to date",
			results: []ApplicationCheckResult{
				{
					AppName:        "app1",
					Project:        "default",
					ChartName:      "chart1",
					CurrentVersion: "1.0.0",
					LatestVersion:  "1.0.0",
					RepoURL:        "https://charts.example.com",
					HasUpdate:      false,
				},
			},
			formats: []string{"table", "json", "markdown"},
		},
		{
			name: "with updates",
			results: []ApplicationCheckResult{
				{
					AppName:        "app1",
					Project:        "default",
					ChartName:      "chart1",
					CurrentVersion: "1.0.0",
					LatestVersion:  "2.0.0",
					RepoURL:        "https://charts.example.com",
					HasUpdate:      true,
				},
			},
			formats: []string{"table", "json", "markdown"},
		},
		{
			name: "with errors",
			results: []ApplicationCheckResult{
				{
					AppName:   "app1",
					Project:   "default",
					ChartName: "chart1",
					RepoURL:   "https://charts.example.com",
					Error:     "test error",
				},
			},
			formats: []string{"table", "json", "markdown"},
		},
		{
			name: "mixed results",
			results: []ApplicationCheckResult{
				{
					AppName:        "app1",
					Project:        "default",
					ChartName:      "chart1",
					CurrentVersion: "1.0.0",
					LatestVersion:  "1.0.0",
					RepoURL:        "https://charts.example.com",
					HasUpdate:      false,
				},
				{
					AppName:        "app2",
					Project:        "prod",
					ChartName:      "chart2",
					CurrentVersion: "1.0.0",
					LatestVersion:  "2.0.0",
					RepoURL:        "https://charts.example.com",
					HasUpdate:      true,
				},
				{
					AppName:   "app3",
					Project:   "dev",
					ChartName: "chart3",
					RepoURL:   "https://charts.example.com",
					Error:     "test error",
				},
			},
			formats: []string{"table", "json", "markdown"},
		},
		{
			name: "empty app names (non-helm apps)",
			results: []ApplicationCheckResult{
				{
					AppName: "",
				},
				{
					AppName:        "app1",
					Project:        "default",
					ChartName:      "chart1",
					CurrentVersion: "1.0.0",
					LatestVersion:  "1.0.0",
					RepoURL:        "https://charts.example.com",
					HasUpdate:      false,
				},
			},
			formats: []string{"table", "json", "markdown"},
		},
		{
			name: "with constraint info",
			results: []ApplicationCheckResult{
				{
					AppName:                    "app1",
					Project:                    "default",
					ChartName:                  "chart1",
					CurrentVersion:             "1.0.0",
					LatestVersion:              "1.0.0",
					RepoURL:                    "https://charts.example.com",
					HasUpdate:                  false,
					ConstraintApplied:          "minor",
					HasUpdateOutsideConstraint: true,
					LatestVersionAll:           "2.0.0",
				},
			},
			formats: []string{"table", "json", "markdown"},
		},
	}

	for _, tt := range tests {
		for _, format := range tt.formats {
			t.Run(tt.name+"_"+format, func(t *testing.T) {
				// Just ensure it doesn't panic
				assert.NotPanics(t, func() {
					err := outputResults(tt.results, format, io.Discard)
					assert.NoError(t, err)
				})
			})
		}
	}
}

func TestOutputResults_InvalidFormat(t *testing.T) {
	results := []ApplicationCheckResult{
		{
			AppName: "test",
			Project: "default",
		},
	}

	err := outputResults(results, "invalid", io.Discard)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown output format")
}

func TestRenderJSON(t *testing.T) {
	tests := []struct {
		name    string
		results []ApplicationCheckResult
	}{
		{
			name:    "empty results",
			results: []ApplicationCheckResult{},
		},
		{
			name: "with updates and up-to-date",
			results: []ApplicationCheckResult{
				{
					AppName:        "app1",
					Project:        "default",
					ChartName:      "chart1",
					CurrentVersion: "1.0.0",
					LatestVersion:  "2.0.0",
					RepoURL:        "https://charts.example.com",
					HasUpdate:      true,
				},
				{
					AppName:                    "app2",
					Project:                    "default",
					ChartName:                  "chart2",
					CurrentVersion:             "1.0.0",
					LatestVersion:              "1.0.0",
					RepoURL:                    "https://charts.example.com",
					HasUpdate:                  false,
					HasUpdateOutsideConstraint: true,
					LatestVersionAll:           "2.0.0",
					ConstraintApplied:          "minor",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				categorized := processResults(tt.results)
				err := renderJSON(categorized, io.Discard)
				assert.NoError(t, err)
			})
		})
	}
}

func TestRenderMarkdown(t *testing.T) {
	tests := []struct {
		name    string
		results []ApplicationCheckResult
	}{
		{
			name:    "empty results",
			results: []ApplicationCheckResult{},
		},
		{
			name: "with all sections",
			results: []ApplicationCheckResult{
				{
					AppName:        "app1",
					Project:        "default",
					ChartName:      "chart1",
					CurrentVersion: "1.0.0",
					LatestVersion:  "2.0.0",
					RepoURL:        "https://charts.example.com",
					HasUpdate:      true,
				},
				{
					AppName:                    "app2",
					Project:                    "default",
					ChartName:                  "chart2",
					CurrentVersion:             "1.0.0",
					LatestVersion:              "1.0.0",
					RepoURL:                    "https://charts.example.com",
					HasUpdate:                  false,
					HasUpdateOutsideConstraint: true,
					LatestVersionAll:           "2.0.0",
					ConstraintApplied:          "minor",
				},
				{
					AppName:   "app3",
					Project:   "default",
					ChartName: "chart3",
					RepoURL:   "https://charts.example.com",
					Error:     "test error",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				categorized := processResults(tt.results)
				err := renderMarkdown(categorized, io.Discard)
				assert.NoError(t, err)
			})
		})
	}
}

func TestBuildNotificationMessages(t *testing.T) {
	tests := []struct {
		name        string
		updates     []ApplicationCheckResult
		expectSplit bool
		minMessages int
		maxMessages int
	}{
		{
			name:        "empty updates",
			updates:     []ApplicationCheckResult{},
			expectSplit: false,
			minMessages: 1,
			maxMessages: 1,
		},
		{
			name: "single update",
			updates: []ApplicationCheckResult{
				{
					AppName:        "app1",
					Project:        "default",
					ChartName:      "chart1",
					CurrentVersion: "1.0.0",
					LatestVersion:  "2.0.0",
					RepoURL:        "https://charts.example.com",
				},
			},
			expectSplit: false,
			minMessages: 1,
			maxMessages: 1,
		},
		{
			name: "multiple updates",
			updates: []ApplicationCheckResult{
				{
					AppName:        "app1",
					Project:        "default",
					ChartName:      "chart1",
					CurrentVersion: "1.0.0",
					LatestVersion:  "2.0.0",
					RepoURL:        "https://charts.example.com",
				},
				{
					AppName:        "app2",
					Project:        "prod",
					ChartName:      "chart2",
					CurrentVersion: "1.5.0",
					LatestVersion:  "1.6.0",
					RepoURL:        "https://charts.example.com",
				},
			},
			expectSplit: false,
			minMessages: 1,
			maxMessages: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Convert to notification format
			var updates []notification.ApplicationUpdate
			for _, result := range tt.updates {
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

			formatter := notification.NewMessageFormatter()
			messages := formatter.FormatMessages(updates)
			assert.GreaterOrEqual(t, len(messages), tt.minMessages)
			assert.LessOrEqual(t, len(messages), tt.maxMessages)

			// Verify each message is not too long
			for _, msg := range messages {
				assert.LessOrEqual(t, len(msg), 4096, "Message should stay within the formatter's chunk limit")
			}
		})
	}
}

func TestCheckApplicationsConcurrently(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	cfg := &config.Config{
		Concurrency: 2,
	}

	// Test with empty app list
	apps := []*argocd.Application{}
	results := checkApplicationsConcurrently(context.Background(), apps, nil, cfg, logger)
	assert.Equal(t, 0, len(results))
}

func TestCheckApplicationsConcurrently_ZeroConcurrency(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	cfg := &config.Config{
		Concurrency: 0, // Should fallback to 10
	}

	apps := []*argocd.Application{}
	results := checkApplicationsConcurrently(context.Background(), apps, nil, cfg, logger)
	assert.Equal(t, 0, len(results))
}

func TestCheckApplicationsConcurrently_NegativeConcurrency(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	cfg := &config.Config{
		Concurrency: -5, // Should fallback to 10
	}

	apps := []*argocd.Application{}
	results := checkApplicationsConcurrently(context.Background(), apps, nil, cfg, logger)
	assert.Equal(t, 0, len(results))
}

func TestApplicationCheckResult(t *testing.T) {
	// Test struct creation
	result := ApplicationCheckResult{
		AppName:        "test-app",
		Project:        "test-project",
		ChartName:      "test-chart",
		CurrentVersion: "1.0.0",
		LatestVersion:  "2.0.0",
		RepoURL:        "https://charts.example.com",
		HasUpdate:      true,
		Error:          "",
	}

	assert.Equal(t, "test-app", result.AppName)
	assert.Equal(t, "test-project", result.Project)
	assert.Equal(t, "test-chart", result.ChartName)
	assert.Equal(t, "1.0.0", result.CurrentVersion)
	assert.Equal(t, "2.0.0", result.LatestVersion)
	assert.True(t, result.HasUpdate)
	assert.Empty(t, result.Error)
}

func TestScanResults(t *testing.T) {
	// Test struct creation
	stats := scanResults{
		total:    10,
		upToDate: 5,
		updates:  3,
		skipped:  2,
	}

	assert.Equal(t, 10, stats.total)
	assert.Equal(t, 5, stats.upToDate)
	assert.Equal(t, 3, stats.updates)
	assert.Equal(t, 2, stats.skipped)
}

func TestClients(t *testing.T) {
	// Test struct creation
	c := &clients{}
	assert.Nil(t, c.argocd)
	assert.Nil(t, c.helm)
	assert.Nil(t, c.notifier)
}

func TestBuildNotificationMessages_LongMessages(t *testing.T) {
	// Create many updates to force message splitting
	var updates []notification.ApplicationUpdate
	for i := 0; i < 100; i++ {
		updates = append(updates, notification.ApplicationUpdate{
			AppName:        "very-long-application-name-that-takes-space",
			Project:        "production-project-with-long-name",
			ChartName:      "chart-with-very-descriptive-name",
			CurrentVersion: "1.0.0",
			LatestVersion:  "2.0.0",
			RepoURL:        "https://charts.example.com/very/long/path/to/repository/that/takes/up/space",
		})
	}

	formatter := notification.NewMessageFormatter()
	messages := formatter.FormatMessages(updates)

	// Should split into multiple messages
	assert.Greater(t, len(messages), 1, "Should split large number of updates into multiple messages")

	// Each message should not exceed max length
	for _, msg := range messages {
		assert.LessOrEqual(t, len(msg), 4096)
	}
}

// MockNotifier is a mock implementation of the Notifier interface for testing
type MockNotifier struct {
	SendCalled bool
	SendError  error
}

func (m *MockNotifier) Send(ctx context.Context, subject, message string) error {
	m.SendCalled = true
	return m.SendError
}

func TestSendNotifications_NoUpdates(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	notifier := &MockNotifier{}

	results := []ApplicationCheckResult{
		{
			AppName:        "app1",
			Project:        "default",
			ChartName:      "chart1",
			CurrentVersion: "1.0.0",
			LatestVersion:  "1.0.0",
			HasUpdate:      false,
		},
	}

	err := sendNotifications(context.Background(), notifier, results, logger)
	require.NoError(t, err)
	assert.False(t, notifier.SendCalled, "Should not send notification when no updates")
}

func TestSendNotifications_WithUpdates(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	notifier := &MockNotifier{}

	results := []ApplicationCheckResult{
		{
			AppName:        "app1",
			Project:        "default",
			ChartName:      "chart1",
			CurrentVersion: "1.0.0",
			LatestVersion:  "2.0.0",
			HasUpdate:      true,
		},
	}

	err := sendNotifications(context.Background(), notifier, results, logger)
	require.NoError(t, err)
	assert.True(t, notifier.SendCalled, "Should send notification when updates available")
}

func TestSendNotifications_Error(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	notifier := &MockNotifier{SendError: assert.AnError}

	results := []ApplicationCheckResult{
		{
			AppName:        "app1",
			Project:        "default",
			ChartName:      "chart1",
			CurrentVersion: "1.0.0",
			LatestVersion:  "2.0.0",
			HasUpdate:      true,
		},
	}

	err := sendNotifications(context.Background(), notifier, results, logger)
	require.Error(t, err)
	assert.True(t, notifier.SendCalled, "Should attempt to send notification")
}

func TestCheckApplication_NonHelmApp(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	cfg := &config.Config{}

	app := &argocd.Application{
		Metadata: argocd.ApplicationMetadata{
			Name: "git-app",
		},
		Spec: argocd.ApplicationSpec{
			Project: "default",
			Source: &argocd.ApplicationSource{
				RepoURL:        "https://github.com/example/repo",
				TargetRevision: "main",
				Path:           "manifests",
			},
		},
	}

	result := checkApplication(context.Background(), app, nil, cfg, logger)
	assert.Equal(t, "", result.AppName, "Should return empty result for non-Helm app")
}

// TestRequiresManualUpdate makes sure an update ArgoCD applies on its own (a newer version
// inside the targetRevision range) is not reported as an update requiring attention, while
// versions beyond the range and newer versions for a pinned revision are.
func TestRequiresManualUpdate(t *testing.T) {
	tests := []struct {
		updateType string
		expected   bool
	}{
		{updateType: helm.UpdateTypeNone, expected: false},
		{updateType: helm.UpdateTypeInRange, expected: false},
		{updateType: helm.UpdateTypeOutOfRange, expected: true},
		{updateType: helm.UpdateTypePinned, expected: true},
		{updateType: "", expected: false},
	}

	for _, test := range tests {
		t.Run(test.updateType, func(t *testing.T) {
			assert.Equal(t, test.expected, requiresManualUpdate(test.updateType))
		})
	}
}

func TestCheckApplication_MultiSourceWithHelm(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	cfg := &config.Config{
		SourceName: "chart-source",
	}

	app := &argocd.Application{
		Metadata: argocd.ApplicationMetadata{
			Name: "multi-source-app",
		},
		Spec: argocd.ApplicationSpec{
			Project: "default",
			Sources: []argocd.ApplicationSource{
				{
					RepoURL:        "https://github.com/example/values",
					TargetRevision: "main",
					Path:           "values",
				},
				{
					Name:           "chart-source",
					Chart:          "my-chart",
					RepoURL:        "https://charts.example.com",
					TargetRevision: "1.0.0",
				},
			},
		},
	}

	// Test that it finds the helm source correctly
	helmSource := findHelmSource(app, cfg.SourceName, logger)
	require.NotNil(t, helmSource)
	assert.Equal(t, "my-chart", helmSource.Chart)
	assert.Equal(t, "1.0.0", helmSource.TargetRevision)
}

// stubChartVersions stands in for the ArgoCD client and records what it was asked about, so
// a test can tell which source of an application the versions were looked up for.
type stubChartVersions struct {
	versions []string
	requests []string
}

func (s *stubChartVersions) GetHelmChartVersions(_ context.Context, repoURL, chartName, _ string) ([]string, error) {
	s.requests = append(s.requests, fmt.Sprintf("helm %s %s", repoURL, chartName))
	return s.versions, nil
}

func (s *stubChartVersions) GetOCITags(_ context.Context, repoURL, chartName, _ string) ([]string, error) {
	s.requests = append(s.requests, fmt.Sprintf("oci %s %s", repoURL, chartName))
	return s.versions, nil
}

func (s *stubChartVersions) GetGitTags(_ context.Context, repoURL, _ string) ([]string, error) {
	s.requests = append(s.requests, fmt.Sprintf("git %s", repoURL))
	return s.versions, nil
}

// TestCheckApplication_MultiSourceValuesRef checks that an application whose values live in
// a Git repository alongside the chart is checked for versions of the chart, and that the
// repository and chart reported are the ones of the chart source rather than of the values.
func TestCheckApplication_MultiSourceValuesRef(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	versionSource := &stubChartVersions{versions: []string{"1.0.0", "1.1.0", "2.0.0"}}
	helmChecker, err := helm.NewChecker(versionSource, logger)
	require.NoError(t, err)

	app := &argocd.Application{
		Metadata: argocd.ApplicationMetadata{Name: "multi-source-app"},
		Spec: argocd.ApplicationSpec{
			Project: "default",
			Sources: []argocd.ApplicationSource{
				{
					RepoURL:        "https://github.com/example/values.git",
					TargetRevision: "main",
					Ref:            "values",
					Helm:           &argocd.ApplicationSourceHelm{},
				},
				{
					Chart:          "my-chart",
					RepoURL:        "https://charts.example.com",
					TargetRevision: "1.0.0",
					Helm:           &argocd.ApplicationSourceHelm{},
				},
			},
		},
	}

	cfg := &config.Config{NotifyOn: config.NotifyOnMajor}
	result := checkApplication(context.Background(), app, helmChecker, cfg, logger)

	assert.Equal(t, "multi-source-app", result.AppName)
	assert.Equal(t, "my-chart", result.ChartName)
	assert.Equal(t, "https://charts.example.com", result.RepoURL)
	assert.Equal(t, "1.0.0", result.CurrentVersion)
	assert.Equal(t, "2.0.0", result.LatestVersion)
	assert.Empty(t, result.Error)
	assert.Equal(t, []string{"helm https://charts.example.com my-chart"}, versionSource.requests,
		"only the chart source should be looked up")
}

func TestSendNotifications_MultipleMessages(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	notifier := &MockNotifier{}

	// Create many updates to force splitting
	var results []ApplicationCheckResult
	for i := 0; i < 50; i++ {
		results = append(results, ApplicationCheckResult{
			AppName:        "app-very-long-name-that-takes-up-space",
			Project:        "production-project-with-long-name",
			ChartName:      "chart-with-very-descriptive-name",
			CurrentVersion: "1.0.0",
			LatestVersion:  "2.0.0",
			RepoURL:        "https://charts.example.com/very/long/path/to/repository",
			HasUpdate:      true,
		})
	}

	err := sendNotifications(context.Background(), notifier, results, logger)
	require.NoError(t, err)
	assert.True(t, notifier.SendCalled)
}

// pinnedUpdate builds a result for an application pinned to currentVersion with a newer
// version available, i.e. an update someone has to apply by hand.
func pinnedUpdate(currentVersion, latestVersion string) ApplicationCheckResult {
	return ApplicationCheckResult{
		AppName:        "app",
		ChartName:      "chart",
		CurrentVersion: currentVersion,
		LatestVersion:  latestVersion,
		HasUpdate:      true,
		UpdateType:     helm.UpdateTypePinned,
	}
}

func TestDetermineExitCode(t *testing.T) {
	upToDate := ApplicationCheckResult{
		AppName:        "up-to-date-app",
		CurrentVersion: "1.0.0",
		LatestVersion:  "1.0.0",
		UpdateType:     helm.UpdateTypeNone,
	}
	inRange := ApplicationCheckResult{
		AppName:        "in-range-app",
		CurrentVersion: "^1.0.0",
		LatestVersion:  "1.4.0",
		UpdateType:     helm.UpdateTypeInRange,
	}
	failedApp := ApplicationCheckResult{
		AppName: "unreachable-app",
		Error:   "failed to fetch index.yaml",
	}

	tests := []struct {
		name     string
		results  []ApplicationCheckResult
		failOn   string
		expected int
	}{
		{
			name:     "no applications at all",
			failOn:   config.FailOnAny,
			expected: exitCodeClean,
		},
		{
			name:     "everything up to date",
			results:  []ApplicationCheckResult{upToDate, inRange},
			failOn:   config.FailOnAny,
			expected: exitCodeClean,
		},
		{
			name:     "updates are ignored when fail-on is none",
			results:  []ApplicationCheckResult{pinnedUpdate("1.0.0", "3.0.0")},
			failOn:   config.FailOnNone,
			expected: exitCodeClean,
		},
		{
			name:     "updates are ignored when fail-on is unset",
			results:  []ApplicationCheckResult{pinnedUpdate("1.0.0", "3.0.0")},
			failOn:   "",
			expected: exitCodeClean,
		},
		{
			name:     "an update ArgoCD applies itself does not fail the run",
			results:  []ApplicationCheckResult{inRange},
			failOn:   config.FailOnAny,
			expected: exitCodeClean,
		},
		{
			name:     "unchecked application fails the scan",
			results:  []ApplicationCheckResult{upToDate, failedApp},
			failOn:   config.FailOnNone,
			expected: exitCodeScanFailed,
		},
		{
			name:     "unchecked application wins over found updates",
			results:  []ApplicationCheckResult{pinnedUpdate("1.0.0", "2.0.0"), failedApp},
			failOn:   config.FailOnAny,
			expected: exitCodeScanFailed,
		},
		{
			name:     "non-Helm applications are neither updates nor failures",
			results:  []ApplicationCheckResult{{}, {}},
			failOn:   config.FailOnAny,
			expected: exitCodeClean,
		},
		{
			name:     "update found with fail-on any",
			results:  []ApplicationCheckResult{pinnedUpdate("1.0.0", "1.0.1")},
			failOn:   config.FailOnAny,
			expected: exitCodeUpdatesFound,
		},
		{
			name:     "unknown fail-on value never fails the run",
			results:  []ApplicationCheckResult{pinnedUpdate("1.0.0", "2.0.0")},
			failOn:   "everything",
			expected: exitCodeClean,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.expected, determineExitCode(test.results, test.failOn))
		})
	}
}

// TestDetermineExitCode_FailOnSeverity checks that a --fail-on value reacts to the updates
// of that severity and to the bigger ones, but not to the smaller ones.
func TestDetermineExitCode_FailOnSeverity(t *testing.T) {
	tests := []struct {
		name   string
		update ApplicationCheckResult
		failOn map[string]int
	}{
		{
			name:   "major update",
			update: pinnedUpdate("1.2.3", "2.0.0"),
			failOn: map[string]int{
				config.FailOnMajor: exitCodeUpdatesFound,
				config.FailOnMinor: exitCodeUpdatesFound,
				config.FailOnPatch: exitCodeUpdatesFound,
			},
		},
		{
			name:   "minor update",
			update: pinnedUpdate("1.2.3", "1.3.0"),
			failOn: map[string]int{
				config.FailOnMajor: exitCodeClean,
				config.FailOnMinor: exitCodeUpdatesFound,
				config.FailOnPatch: exitCodeUpdatesFound,
			},
		},
		{
			name:   "patch update",
			update: pinnedUpdate("1.2.3", "1.2.4"),
			failOn: map[string]int{
				config.FailOnMajor: exitCodeClean,
				config.FailOnMinor: exitCodeClean,
				config.FailOnPatch: exitCodeUpdatesFound,
			},
		},
		{
			name: "update beyond the target range is measured against the newest version overall",
			update: ApplicationCheckResult{
				AppName:          "ranged-app",
				CurrentVersion:   "~1.2.0",
				LatestVersion:    "1.2.9",
				LatestVersionAll: "2.1.0",
				HasUpdate:        true,
				UpdateType:       helm.UpdateTypeOutOfRange,
			},
			failOn: map[string]int{
				config.FailOnMajor: exitCodeUpdatesFound,
				config.FailOnMinor: exitCodeUpdatesFound,
				config.FailOnPatch: exitCodeUpdatesFound,
			},
		},
		{
			name:   "severity of an update for a branch revision cannot be told",
			update: pinnedUpdate("main", "2.0.0"),
			failOn: map[string]int{
				config.FailOnMajor: exitCodeClean,
				config.FailOnMinor: exitCodeClean,
				config.FailOnPatch: exitCodeClean,
				config.FailOnAny:   exitCodeUpdatesFound,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for failOn, expected := range test.failOn {
				assert.Equal(t, expected, determineExitCode([]ApplicationCheckResult{test.update}, failOn),
					"fail-on=%s", failOn)
			}
		})
	}
}

// TestDetermineExitCode_MixedSeverities makes sure the most severe update in the scan
// decides the outcome, no matter where it sits in the results.
func TestDetermineExitCode_MixedSeverities(t *testing.T) {
	results := []ApplicationCheckResult{
		pinnedUpdate("1.2.3", "1.2.4"),
		pinnedUpdate("1.2.3", "1.3.0"),
		pinnedUpdate("1.2.3", "1.2.5"),
	}

	assert.Equal(t, exitCodeUpdatesFound, determineExitCode(results, config.FailOnMinor))
	assert.Equal(t, exitCodeClean, determineExitCode(results, config.FailOnMajor))
}

func TestUpdateTarget(t *testing.T) {
	tests := []struct {
		name     string
		result   ApplicationCheckResult
		expected string
	}{
		{
			name:     "pinned revision uses the latest version",
			result:   pinnedUpdate("1.0.0", "1.5.0"),
			expected: "1.5.0",
		},
		{
			name: "revision beyond the range uses the latest version overall",
			result: ApplicationCheckResult{
				CurrentVersion:   "~1.2.0",
				LatestVersion:    "1.2.9",
				LatestVersionAll: "3.0.0",
				UpdateType:       helm.UpdateTypeOutOfRange,
			},
			expected: "3.0.0",
		},
		{
			name: "revision beyond the range without a known latest version overall",
			result: ApplicationCheckResult{
				CurrentVersion: "~1.2.0",
				LatestVersion:  "1.2.9",
				UpdateType:     helm.UpdateTypeOutOfRange,
			},
			expected: "1.2.9",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.expected, updateTarget(test.result))
		})
	}
}

func TestExitError(t *testing.T) {
	err := &exitError{code: exitCodeUpdatesFound, message: "updates were found"}

	assert.EqualError(t, err, "updates were found")

	var exitErr *exitError
	require.True(t, errors.As(fmt.Errorf("wrapped: %w", err), &exitErr))
	assert.Equal(t, exitCodeUpdatesFound, exitErr.code)
}
