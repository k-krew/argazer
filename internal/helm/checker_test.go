package helm

import (
	"context"
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
)

// stubVersionSource stands in for the ArgoCD client. Helm versions and OCI tags are keyed by
// repository URL and chart name, Git tags by repository URL alone, the way ArgoCD reports
// them.
type stubVersionSource struct {
	helmVersions map[string][]string
	ociTags      map[string][]string
	gitTags      map[string][]string
	err          error

	// Recorded arguments of the last call, so tests can check what was asked of ArgoCD.
	lastRepoURL   string
	lastChartName string
	lastProject   string

	// Calls per kind of repository, so tests can check where a repository was routed.
	helmCalls int
	ociCalls  int
	gitCalls  int
}

func (s *stubVersionSource) GetHelmChartVersions(_ context.Context, repoURL, chartName, project string) ([]string, error) {
	s.helmCalls++
	s.record(repoURL, chartName, project)

	if s.err != nil {
		return nil, s.err
	}

	return s.helmVersions[repoURL+"|"+chartName], nil
}

func (s *stubVersionSource) GetOCITags(_ context.Context, repoURL, chartName, project string) ([]string, error) {
	s.ociCalls++
	s.record(repoURL, chartName, project)

	if s.err != nil {
		return nil, s.err
	}

	return s.ociTags[repoURL+"|"+chartName], nil
}

func (s *stubVersionSource) GetGitTags(_ context.Context, repoURL, project string) ([]string, error) {
	s.gitCalls++
	s.record(repoURL, "", project)

	if s.err != nil {
		return nil, s.err
	}

	return s.gitTags[repoURL], nil
}

func (s *stubVersionSource) record(repoURL, chartName, project string) {
	s.lastRepoURL = repoURL
	s.lastChartName = chartName
	s.lastProject = project
}

// calls is how often ArgoCD was asked anything at all.
func (s *stubVersionSource) calls() int {
	return s.helmCalls + s.ociCalls + s.gitCalls
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
		helmVersions: map[string][]string{
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
		helmVersions: map[string][]string{
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
		helmVersions: map[string][]string{
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
		helmVersions: map[string][]string{
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
		helmVersions: map[string][]string{
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

// TestCheckerGetLatestVersion_FromOCIThroughArgoCD checks that an OCI registry is asked for
// through ArgoCD too, with the project of the application: a private registry is reachable
// with the credentials ArgoCD stores for it, and with those alone.
func TestCheckerGetLatestVersion_FromOCIThroughArgoCD(t *testing.T) {
	source := &stubVersionSource{
		ociTags: map[string][]string{
			"ghcr.io/myorg/charts|nginx": {"1.20.0", "latest", "1.21.0"},
		},
	}
	checker := newTestChecker(t, source)

	version, err := checker.GetLatestVersion(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
	if err != nil {
		t.Fatalf("GetLatestVersion failed: %v", err)
	}

	// A tag that is no version at all is skipped rather than being taken for the newest one.
	if version != "1.21.0" {
		t.Errorf("Expected version 1.21.0, got %s", version)
	}

	if source.ociCalls != 1 || source.calls() != 1 {
		t.Errorf("Expected exactly one OCI request to ArgoCD, got %d of %d calls", source.ociCalls, source.calls())
	}

	if source.lastProject != "team-a" {
		t.Errorf("Expected ArgoCD to be asked within project team-a, got %q", source.lastProject)
	}
}

// TestCheckerGetLatestVersion_OCISchemeIsRouted checks that the oci:// scheme an Application
// may carry is recognised as an OCI registry rather than a Helm repository.
func TestCheckerGetLatestVersion_OCISchemeIsRouted(t *testing.T) {
	source := &stubVersionSource{
		ociTags: map[string][]string{
			"oci://ghcr.io/myorg/charts|nginx": {"1.21.0"},
		},
	}
	checker := newTestChecker(t, source)

	version, err := checker.GetLatestVersion(context.Background(), "oci://ghcr.io/myorg/charts", "nginx", "team-a")
	if err != nil {
		t.Fatalf("GetLatestVersion failed: %v", err)
	}

	if version != "1.21.0" {
		t.Errorf("Expected version 1.21.0, got %s", version)
	}

	if source.ociCalls != 1 {
		t.Errorf("Expected the OCI request to ArgoCD, got %d OCI calls of %d", source.ociCalls, source.calls())
	}
}

// TestCheckerGetLatestVersionWithConstraint_FromOCI checks that OCI tags go through the same
// constraint logic as the versions of a Helm repository.
func TestCheckerGetLatestVersionWithConstraint_FromOCI(t *testing.T) {
	source := &stubVersionSource{
		ociTags: map[string][]string{
			"ghcr.io/myorg/charts|nginx": {"1.2.0", "1.5.0", "2.0.0"},
		},
	}
	checker := newTestChecker(t, source)

	result, err := checker.GetLatestVersionWithConstraint(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a", "1.2.0", "minor")
	if err != nil {
		t.Fatalf("GetLatestVersionWithConstraint failed: %v", err)
	}

	if result.LatestVersion != "1.5.0" {
		t.Errorf("LatestVersion = %s, expected 1.5.0", result.LatestVersion)
	}

	if result.LatestVersionAll != "2.0.0" {
		t.Errorf("LatestVersionAll = %s, expected 2.0.0", result.LatestVersionAll)
	}
}

// TestCheckerGetLatestVersion_OCIWithoutTags checks that an artifact ArgoCD reports no tags
// for is treated as a missing chart.
func TestCheckerGetLatestVersion_OCIWithoutTags(t *testing.T) {
	checker := newTestChecker(t, &stubVersionSource{})

	_, err := checker.GetLatestVersion(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
	if !errors.Is(err, ErrChartNotFound) {
		t.Errorf("Expected ErrChartNotFound, got: %v", err)
	}
}

// TestCheckerGetLatestVersion_OCIError checks that a failing OCI request is reported as a
// failure and not as an empty registry.
func TestCheckerGetLatestVersion_OCIError(t *testing.T) {
	sourceErr := errors.New("registry unavailable")
	checker := newTestChecker(t, &stubVersionSource{err: sourceErr})

	_, err := checker.GetLatestVersion(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
	if !errors.Is(err, sourceErr) {
		t.Errorf("Expected the ArgoCD error to be wrapped, got: %v", err)
	}

	if errors.Is(err, ErrChartNotFound) {
		t.Error("An ArgoCD failure must not be reported as a missing chart")
	}
}

// TestCheckerGetLatestVersion_FromGitThroughArgoCD checks that a Git repository is asked for
// through ArgoCD as well, and that the chart is not part of that question: ArgoCD lists the
// tags of the whole repository.
func TestCheckerGetLatestVersion_FromGitThroughArgoCD(t *testing.T) {
	source := &stubVersionSource{
		gitTags: map[string][]string{
			"https://github.com/myorg/charts.git": {"v1.2.3", "not-a-version", "v1.3.0"},
		},
	}
	checker := newTestChecker(t, source)

	version, err := checker.GetLatestVersion(context.Background(), "https://github.com/myorg/charts.git", "charts/nginx", "team-a")
	if err != nil {
		t.Fatalf("GetLatestVersion failed: %v", err)
	}

	if version != "v1.3.0" {
		t.Errorf("Expected version v1.3.0, got %s", version)
	}

	if source.gitCalls != 1 || source.calls() != 1 {
		t.Errorf("Expected exactly one Git request to ArgoCD, got %d of %d calls", source.gitCalls, source.calls())
	}

	if source.lastProject != "team-a" {
		t.Errorf("Expected ArgoCD to be asked within project team-a, got %q", source.lastProject)
	}
}

// TestCheckerGetLatestVersion_GitTagsOfTheChart checks that a repository holding several
// charts is read per chart: a tag naming another chart is not a version of this one.
func TestCheckerGetLatestVersion_GitTagsOfTheChart(t *testing.T) {
	source := &stubVersionSource{
		gitTags: map[string][]string{
			"https://github.com/myorg/charts.git": {"nginx-1.2.3", "redis-9.9.9"},
		},
	}
	checker := newTestChecker(t, source)

	version, err := checker.GetLatestVersion(context.Background(), "https://github.com/myorg/charts.git", "charts/nginx", "team-a")
	if err != nil {
		t.Fatalf("GetLatestVersion failed: %v", err)
	}

	if version != "1.2.3" {
		t.Errorf("Expected version 1.2.3, got %s", version)
	}
}

// TestCheckerGetLatestVersion_GitWithoutVersionTags checks that a repository whose tags hold
// no version of the chart is treated as not holding the chart.
func TestCheckerGetLatestVersion_GitWithoutVersionTags(t *testing.T) {
	source := &stubVersionSource{
		gitTags: map[string][]string{
			"https://github.com/myorg/charts.git": {"redis-9.9.9", "some-tag"},
		},
	}
	checker := newTestChecker(t, source)

	_, err := checker.GetLatestVersion(context.Background(), "https://github.com/myorg/charts.git", "charts/nginx", "team-a")
	if !errors.Is(err, ErrChartNotFound) {
		t.Errorf("Expected ErrChartNotFound, got: %v", err)
	}
}

// TestCheckerGetLatestVersionWithConstraint_FromGit checks that Git tags go through the same
// constraint logic as the versions of a Helm repository.
func TestCheckerGetLatestVersionWithConstraint_FromGit(t *testing.T) {
	source := &stubVersionSource{
		gitTags: map[string][]string{
			"https://github.com/myorg/charts.git": {"v1.2.0", "v1.5.0", "v2.0.0"},
		},
	}
	checker := newTestChecker(t, source)

	result, err := checker.GetLatestVersionWithConstraint(context.Background(), "https://github.com/myorg/charts.git", "charts/nginx", "team-a", "v1.2.0", "minor")
	if err != nil {
		t.Fatalf("GetLatestVersionWithConstraint failed: %v", err)
	}

	if result.LatestVersion != "v1.5.0" {
		t.Errorf("LatestVersion = %s, expected v1.5.0", result.LatestVersion)
	}

	if result.LatestVersionAll != "v2.0.0" {
		t.Errorf("LatestVersionAll = %s, expected v2.0.0", result.LatestVersionAll)
	}
}

// TestCheckerGetLatestVersion_GitError checks that a failing Git request is reported as a
// failure.
func TestCheckerGetLatestVersion_GitError(t *testing.T) {
	sourceErr := errors.New("repository not accessible")
	checker := newTestChecker(t, &stubVersionSource{err: sourceErr})

	_, err := checker.GetLatestVersion(context.Background(), "https://github.com/myorg/charts.git", "charts/nginx", "team-a")
	if !errors.Is(err, sourceErr) {
		t.Errorf("Expected the ArgoCD error to be wrapped, got: %v", err)
	}

	if errors.Is(err, ErrChartNotFound) {
		t.Error("An ArgoCD failure must not be reported as a missing chart")
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
