package helm

import (
	"context"
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
)

// stubVersionSource stands in for the ArgoCD client, keyed by repository URL and chart name.
type stubVersionSource struct {
	versions map[string][]string
	err      error

	// Recorded arguments of the last call, so tests can check what was asked of ArgoCD.
	lastRepoURL   string
	lastChartName string
	lastProject   string
	calls         int
}

func (s *stubVersionSource) GetHelmChartVersions(_ context.Context, repoURL, chartName, project string) ([]string, error) {
	s.calls++
	s.lastRepoURL = repoURL
	s.lastChartName = chartName
	s.lastProject = project

	if s.err != nil {
		return nil, s.err
	}

	return s.versions[repoURL+"|"+chartName], nil
}

func newTestChecker(t *testing.T, source ChartVersionSource) *Checker {
	t.Helper()

	checker, err := NewChecker(source, logrus.NewEntry(logrus.New()))
	if err != nil {
		t.Fatalf("Failed to create checker: %v", err)
	}

	return checker
}

func TestNewChecker(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	checker, err := NewChecker(&stubVersionSource{}, logger)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if checker == nil {
		t.Fatal("Expected checker to be initialized")
	}

	if checker.versionSource == nil {
		t.Error("Expected version source to be set")
	}
}

func TestNewChecker_WithoutVersionSource(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	if _, err := NewChecker(nil, logger); err == nil {
		t.Fatal("Expected an error when no chart version source is given, got nil")
	}
}

// TestCheckerGetLatestVersion_FromArgoCD checks that versions of a classic Helm repository
// come from ArgoCD, queried with the repository URL, chart name and project of the
// application. The project is what lets ArgoCD resolve project-scoped credentials.
func TestCheckerGetLatestVersion_FromArgoCD(t *testing.T) {
	source := &stubVersionSource{
		versions: map[string][]string{
			"https://charts.example.com|nginx": {"1.19.5", "1.21.0", "1.20.0"},
		},
	}
	checker := newTestChecker(t, source)

	version, err := checker.GetLatestVersion(context.Background(), "https://charts.example.com", "nginx", "team-a")
	if err != nil {
		t.Fatalf("GetLatestVersion failed: %v", err)
	}

	if version != "1.21.0" {
		t.Errorf("Expected version 1.21.0, got %s", version)
	}

	if source.lastRepoURL != "https://charts.example.com" || source.lastChartName != "nginx" {
		t.Errorf("Expected ArgoCD to be asked for nginx in https://charts.example.com, got %s in %s", source.lastChartName, source.lastRepoURL)
	}

	if source.lastProject != "team-a" {
		t.Errorf("Expected ArgoCD to be asked within project team-a, got %q", source.lastProject)
	}
}

// TestCheckerGetLatestVersion_ChartNotFound checks that a chart ArgoCD does not report is
// treated as missing from the repository.
func TestCheckerGetLatestVersion_ChartNotFound(t *testing.T) {
	source := &stubVersionSource{
		versions: map[string][]string{
			"https://charts.example.com|redis": {"7.0.0"},
		},
	}
	checker := newTestChecker(t, source)

	_, err := checker.GetLatestVersion(context.Background(), "https://charts.example.com", "nginx", "team-a")
	if err == nil {
		t.Fatal("Expected error for chart not found, got nil")
	}

	if !errors.Is(err, ErrChartNotFound) {
		t.Errorf("Expected ErrChartNotFound, got: %v", err)
	}
}

// TestCheckerGetLatestVersion_SourceError checks that an unreachable ArgoCD is reported
// instead of being mistaken for an empty repository.
func TestCheckerGetLatestVersion_SourceError(t *testing.T) {
	sourceErr := errors.New("argocd is unavailable")
	checker := newTestChecker(t, &stubVersionSource{err: sourceErr})

	_, err := checker.GetLatestVersion(context.Background(), "https://charts.example.com", "nginx", "team-a")
	if err == nil {
		t.Fatal("Expected error when ArgoCD fails, got nil")
	}

	if !errors.Is(err, sourceErr) {
		t.Errorf("Expected the ArgoCD error to be wrapped, got: %v", err)
	}

	if errors.Is(err, ErrChartNotFound) {
		t.Error("An ArgoCD failure must not be reported as a missing chart")
	}
}

// TestCheckerGetLatestVersion_InvalidVersions checks that versions ArgoCD reports which are
// not semver are ignored.
func TestCheckerGetLatestVersion_InvalidVersions(t *testing.T) {
	source := &stubVersionSource{
		versions: map[string][]string{
			"https://charts.example.com|nginx": {"latest", "dev", "1.2.3", "invalid-version"},
		},
	}
	checker := newTestChecker(t, source)

	version, err := checker.GetLatestVersion(context.Background(), "https://charts.example.com", "nginx", "team-a")
	if err != nil {
		t.Fatalf("GetLatestVersion failed: %v", err)
	}

	if version != "1.2.3" {
		t.Errorf("Expected version 1.2.3, got %s", version)
	}
}

// TestCheckerGetLatestVersionWithConstraint_FromArgoCD checks that the constraint is applied
// to the versions ArgoCD reports.
func TestCheckerGetLatestVersionWithConstraint_FromArgoCD(t *testing.T) {
	source := &stubVersionSource{
		versions: map[string][]string{
			"https://charts.example.com|nginx": {"1.2.0", "1.5.0", "2.0.0"},
		},
	}
	checker := newTestChecker(t, source)

	result, err := checker.GetLatestVersionWithConstraint(context.Background(), "https://charts.example.com", "nginx", "team-a", "1.2.0", "minor")
	if err != nil {
		t.Fatalf("GetLatestVersionWithConstraint failed: %v", err)
	}

	if source.lastProject != "team-a" {
		t.Errorf("Expected ArgoCD to be asked within project team-a, got %q", source.lastProject)
	}

	if result.LatestVersion != "1.5.0" {
		t.Errorf("LatestVersion = %s, expected 1.5.0", result.LatestVersion)
	}

	if result.LatestVersionAll != "2.0.0" {
		t.Errorf("LatestVersionAll = %s, expected 2.0.0", result.LatestVersionAll)
	}

	if !result.HasUpdateOutsideConstraint {
		t.Error("Expected an update outside the constraint to be reported")
	}
}

// TestCheckerGetLatestVersionWithConstraint_ChartNotFound checks that a missing chart is
// reported by the constraint-aware path as well.
func TestCheckerGetLatestVersionWithConstraint_ChartNotFound(t *testing.T) {
	checker := newTestChecker(t, &stubVersionSource{})

	_, err := checker.GetLatestVersionWithConstraint(context.Background(), "https://charts.example.com", "nginx", "team-a", "1.0.0", "major")
	if !errors.Is(err, ErrChartNotFound) {
		t.Errorf("Expected ErrChartNotFound, got: %v", err)
	}
}

// TestCheckerGetLatestVersion_EmptyProject checks that an application without an explicit
// project still reaches ArgoCD, which then treats the repository as globally registered.
func TestCheckerGetLatestVersion_EmptyProject(t *testing.T) {
	source := &stubVersionSource{
		versions: map[string][]string{
			"https://charts.example.com|nginx": {"1.20.0", "1.21.0"},
		},
	}
	checker := newTestChecker(t, source)

	version, err := checker.GetLatestVersion(context.Background(), "https://charts.example.com", "nginx", "")
	if err != nil {
		t.Fatalf("GetLatestVersion failed: %v", err)
	}

	if version != "1.21.0" {
		t.Errorf("Expected version 1.21.0, got %s", version)
	}

	if source.lastProject != "" {
		t.Errorf("Expected an empty project to be passed through, got %q", source.lastProject)
	}
}

// TestCheckerGetLatestVersion_ProjectNotAskedForOCI checks that OCI registries are handled
// by the OCI checker and never reach ArgoCD, so no project is needed for them.
func TestCheckerGetLatestVersion_ProjectNotAskedForOCI(t *testing.T) {
	source := &stubVersionSource{}
	checker := newTestChecker(t, source)

	// The registry does not exist, only the routing decision matters here.
	_, _ = checker.GetLatestVersion(context.Background(), "registry.example.com/charts", "nginx", "team-a")

	if source.calls != 0 {
		t.Errorf("Expected ArgoCD not to be asked for an OCI repository, got %d calls", source.calls)
	}
}

func TestFindLatestSemver(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	tests := []struct {
		name     string
		versions []string
		expect   string
		hasError bool
	}{
		{"simple versions", []string{"1.0.0", "1.0.1", "1.0.2"}, "1.0.2", false},
		{"mixed order", []string{"2.0.0", "1.0.0", "1.5.0"}, "2.0.0", false},
		{"single version", []string{"1.0.0"}, "1.0.0", false},
		{"empty list", []string{}, "", true},
		{"with v prefix", []string{"v1.0.0", "v1.0.1", "v2.0.0"}, "v2.0.0", false},
		{"mixed valid and invalid", []string{"1.0.0", "invalid", "2.0.0", "latest"}, "2.0.0", false},
		{"all invalid", []string{"invalid", "latest", "dev"}, "", true},
		{"pre-release versions", []string{"1.0.0", "1.0.0-alpha", "1.0.0-beta", "2.0.0"}, "2.0.0", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := findLatestSemver(test.versions, logger)
			if test.hasError {
				if err == nil {
					t.Errorf("Expected error for versions %v, got none", test.versions)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error for versions %v: %v", test.versions, err)
				}
				if result != test.expect {
					t.Errorf("findLatestSemver(%v) = %s, expected %s", test.versions, result, test.expect)
				}
			}
		})
	}
}

func TestFindLatestSemverWithConstraint(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	tests := []struct {
		name                      string
		versions                  []string
		currentVersion            string
		constraint                string
		expectedLatest            string
		expectedLatestAll         string
		expectedOutsideConstraint bool
		hasError                  bool
	}{
		{
			name:                      "major constraint - all versions",
			versions:                  []string{"1.0.0", "1.5.0", "2.0.0", "2.1.0"},
			currentVersion:            "1.2.0",
			constraint:                "major",
			expectedLatest:            "2.1.0",
			expectedLatestAll:         "2.1.0",
			expectedOutsideConstraint: false,
			hasError:                  false,
		},
		{
			name:                      "minor constraint - same major only",
			versions:                  []string{"1.0.0", "1.5.0", "2.0.0", "2.1.0"},
			currentVersion:            "1.2.0",
			constraint:                "minor",
			expectedLatest:            "1.5.0",
			expectedLatestAll:         "2.1.0",
			expectedOutsideConstraint: true,
			hasError:                  false,
		},
		{
			name:                      "patch constraint - same major.minor only",
			versions:                  []string{"1.2.0", "1.2.5", "1.3.0", "2.0.0"},
			currentVersion:            "1.2.3",
			constraint:                "patch",
			expectedLatest:            "1.2.5",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
			hasError:                  false,
		},
		{
			name:                      "patch constraint - no updates in constraint",
			versions:                  []string{"1.2.0", "1.2.1", "1.3.0", "2.0.0"},
			currentVersion:            "1.2.3",
			constraint:                "patch",
			expectedLatest:            "1.2.3",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
			hasError:                  false,
		},
		{
			name:                      "minor constraint - no updates in constraint",
			versions:                  []string{"1.0.0", "1.1.0", "2.0.0", "3.0.0"},
			currentVersion:            "1.5.0",
			constraint:                "minor",
			expectedLatest:            "1.5.0",
			expectedLatestAll:         "3.0.0",
			expectedOutsideConstraint: true,
			hasError:                  false,
		},
		{
			name:                      "empty constraint defaults to major",
			versions:                  []string{"1.0.0", "2.0.0", "3.0.0"},
			currentVersion:            "1.0.0",
			constraint:                "",
			expectedLatest:            "3.0.0",
			expectedLatestAll:         "3.0.0",
			expectedOutsideConstraint: false,
			hasError:                  false,
		},
		{
			name:                      "invalid current version falls back to major",
			versions:                  []string{"1.0.0", "2.0.0"},
			currentVersion:            "invalid",
			constraint:                "minor",
			expectedLatest:            "2.0.0",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: false,
			hasError:                  false,
		},
		{
			name:                      "with v prefix",
			versions:                  []string{"v1.0.0", "v1.5.0", "v2.0.0"},
			currentVersion:            "v1.2.0",
			constraint:                "minor",
			expectedLatest:            "v1.5.0",
			expectedLatestAll:         "v2.0.0",
			expectedOutsideConstraint: true,
			hasError:                  false,
		},
		{
			name:                      "mixed valid and invalid versions",
			versions:                  []string{"1.0.0", "invalid", "1.5.0", "latest", "2.0.0"},
			currentVersion:            "1.2.0",
			constraint:                "minor",
			expectedLatest:            "1.5.0",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
			hasError:                  false,
		},
		{
			name:           "empty versions list",
			versions:       []string{},
			currentVersion: "1.0.0",
			constraint:     "major",
			hasError:       true,
		},
		{
			name:           "all invalid versions",
			versions:       []string{"invalid", "latest", "dev"},
			currentVersion: "1.0.0",
			constraint:     "major",
			hasError:       true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := findLatestSemverWithConstraint(test.versions, test.currentVersion, test.constraint, logger)

			if test.hasError {
				if err == nil {
					t.Errorf("Expected error for versions %v with constraint %s, got none", test.versions, test.constraint)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if result.LatestVersion != test.expectedLatest {
				t.Errorf("LatestVersion = %s, expected %s", result.LatestVersion, test.expectedLatest)
			}

			if result.LatestVersionAll != test.expectedLatestAll {
				t.Errorf("LatestVersionAll = %s, expected %s", result.LatestVersionAll, test.expectedLatestAll)
			}

			if result.HasUpdateOutsideConstraint != test.expectedOutsideConstraint {
				t.Errorf("HasUpdateOutsideConstraint = %v, expected %v", result.HasUpdateOutsideConstraint, test.expectedOutsideConstraint)
			}
		})
	}
}
