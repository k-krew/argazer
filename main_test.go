package main

import (
	"context"
	"io"
	"os"
	"testing"

	"argazer/internal/config"
	"argazer/internal/notification"

	"github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		app        *v1alpha1.Application
		sourceName string
		expected   bool
	}{
		{
			name: "single source with helm chart",
			app: &v1alpha1.Application{
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
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
			app: &v1alpha1.Application{
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
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
			app: &v1alpha1.Application{
				Spec: v1alpha1.ApplicationSpec{
					Sources: []v1alpha1.ApplicationSource{
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
			app: &v1alpha1.Application{
				Spec: v1alpha1.ApplicationSpec{
					Sources: []v1alpha1.ApplicationSource{
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
			app: &v1alpha1.Application{
				Spec: v1alpha1.ApplicationSpec{
					Sources: []v1alpha1.ApplicationSource{
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
			app: &v1alpha1.Application{
				Spec: v1alpha1.ApplicationSpec{
					Sources: []v1alpha1.ApplicationSource{
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
				assert.LessOrEqual(t, len(msg), 4096, "Message should not exceed Telegram limit")
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
	apps := []*v1alpha1.Application{}
	results := checkApplicationsConcurrently(context.Background(), apps, nil, cfg, logger)
	assert.Equal(t, 0, len(results))
}

func TestCheckApplicationsConcurrently_ZeroConcurrency(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	cfg := &config.Config{
		Concurrency: 0, // Should fallback to 10
	}

	apps := []*v1alpha1.Application{}
	results := checkApplicationsConcurrently(context.Background(), apps, nil, cfg, logger)
	assert.Equal(t, 0, len(results))
}

func TestCheckApplicationsConcurrently_NegativeConcurrency(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	cfg := &config.Config{
		Concurrency: -5, // Should fallback to 10
	}

	apps := []*v1alpha1.Application{}
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

	app := &v1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name: "git-app",
		},
		Spec: v1alpha1.ApplicationSpec{
			Project: "default",
			Source: &v1alpha1.ApplicationSource{
				RepoURL:        "https://github.com/example/repo",
				TargetRevision: "main",
				Path:           "manifests",
			},
		},
	}

	result := checkApplication(context.Background(), app, nil, cfg, logger)
	assert.Equal(t, "", result.AppName, "Should return empty result for non-Helm app")
}

func TestCheckApplication_MultiSourceWithHelm(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	cfg := &config.Config{
		SourceName: "chart-source",
	}

	app := &v1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name: "multi-source-app",
		},
		Spec: v1alpha1.ApplicationSpec{
			Project: "default",
			Sources: []v1alpha1.ApplicationSource{
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
