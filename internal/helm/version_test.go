package helm

import (
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
)

// TestFindLatestSemverWithRange covers targetRevision values that are semver ranges
// ("~1.2.0", "^1.2.0", "1.2.*") instead of pinned versions. ArgoCD resolves such ranges
// on its own, so LatestVersion must be the newest version inside the range, while
// LatestVersionAll reports the newest version overall.
func TestFindLatestSemverWithRange(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	tests := []struct {
		name                      string
		versions                  []string
		currentVersion            string
		constraint                string
		expectedLatest            string
		expectedLatestAll         string
		expectedOutsideConstraint bool
	}{
		{
			name:                      "tilde range allows patch updates only",
			versions:                  []string{"1.2.0", "1.2.5", "1.3.0", "2.0.0"},
			currentVersion:            "~1.2.0",
			constraint:                "major",
			expectedLatest:            "1.2.5",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
		},
		{
			name:                      "caret range allows minor updates",
			versions:                  []string{"1.2.0", "1.2.5", "1.9.0", "2.0.0"},
			currentVersion:            "^1.2.0",
			constraint:                "major",
			expectedLatest:            "1.9.0",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
		},
		{
			name:                      "wildcard range pins major and minor",
			versions:                  []string{"1.1.0", "1.2.3", "1.2.9", "1.3.0"},
			currentVersion:            "1.2.*",
			constraint:                "major",
			expectedLatest:            "1.2.9",
			expectedLatestAll:         "1.3.0",
			expectedOutsideConstraint: true,
		},
		{
			name:                      "explicit range with two comparators",
			versions:                  []string{"1.1.0", "1.2.0", "1.3.7", "1.4.0", "2.0.0"},
			currentVersion:            ">= 1.2.0, < 1.4.0",
			constraint:                "major",
			expectedLatest:            "1.3.7",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
		},
		{
			name:                      "latest version is inside the range",
			versions:                  []string{"1.2.0", "1.3.0", "1.5.0"},
			currentVersion:            "^1.2.0",
			constraint:                "major",
			expectedLatest:            "1.5.0",
			expectedLatestAll:         "1.5.0",
			expectedOutsideConstraint: false,
		},
		{
			name:                      "only version available already satisfies the range",
			versions:                  []string{"1.2.0"},
			currentVersion:            "~1.2.0",
			constraint:                "major",
			expectedLatest:            "1.2.0",
			expectedLatestAll:         "1.2.0",
			expectedOutsideConstraint: false,
		},
		{
			name:                      "no version satisfies the range",
			versions:                  []string{"1.0.0", "2.0.0"},
			currentVersion:            "~3.1.0",
			constraint:                "major",
			expectedLatest:            "~3.1.0",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
		},
		{
			name:                      "original v prefix is preserved",
			versions:                  []string{"v1.2.0", "v1.5.0", "v2.0.0"},
			currentVersion:            "^1.2.0",
			constraint:                "major",
			expectedLatest:            "v1.5.0",
			expectedLatestAll:         "v2.0.0",
			expectedOutsideConstraint: true,
		},
		{
			name:                      "unparseable versions are skipped",
			versions:                  []string{"1.2.0", "latest", "1.2.8", "dev", "2.0.0"},
			currentVersion:            "~1.2.0",
			constraint:                "major",
			expectedLatest:            "1.2.8",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
		},
		{
			name:                      "pre-releases do not satisfy a stable range",
			versions:                  []string{"1.2.0", "1.2.5", "1.3.0-rc.1"},
			currentVersion:            "~1.2.0",
			constraint:                "major",
			expectedLatest:            "1.2.5",
			expectedLatestAll:         "1.3.0-rc.1",
			expectedOutsideConstraint: true,
		},
		{
			name:                      "range takes precedence over the cli constraint",
			versions:                  []string{"1.2.0", "1.2.5", "1.9.0", "2.0.0"},
			currentVersion:            "^1.2.0",
			constraint:                "patch",
			expectedLatest:            "1.9.0",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := findLatestSemverWithConstraint(test.versions, test.currentVersion, test.constraint, logger)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
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

func TestFindLatestSemverWithRangeErrors(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	tests := []struct {
		name           string
		versions       []string
		currentVersion string
		expectedErr    error
	}{
		{
			name:           "no versions available",
			versions:       []string{},
			currentVersion: "^1.2.0",
		},
		{
			name:           "no parseable versions available",
			versions:       []string{"latest", "dev", "main"},
			currentVersion: "^1.2.0",
			expectedErr:    ErrNoValidVersions,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := findLatestSemverWithConstraint(test.versions, test.currentVersion, "major", logger)
			if err == nil {
				t.Fatalf("Expected an error, got none")
			}
			if test.expectedErr != nil && !errors.Is(err, test.expectedErr) {
				t.Errorf("Expected error %v, got %v", test.expectedErr, err)
			}
		})
	}
}

// TestFindLatestSemverWithNonSemverRevision makes sure that a targetRevision which is
// neither a version nor a range (a Git branch, for example) still falls back to checking
// every available version.
func TestFindLatestSemverWithNonSemverRevision(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	for _, revision := range []string{"main", "master", "HEAD", "release-candidate"} {
		t.Run(revision, func(t *testing.T) {
			result, err := findLatestSemverWithConstraint([]string{"1.0.0", "2.0.0"}, revision, "minor", logger)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if result.LatestVersion != "2.0.0" {
				t.Errorf("LatestVersion = %s, expected 2.0.0", result.LatestVersion)
			}

			if result.LatestVersionAll != "2.0.0" {
				t.Errorf("LatestVersionAll = %s, expected 2.0.0", result.LatestVersionAll)
			}

			if result.HasUpdateOutsideConstraint {
				t.Error("HasUpdateOutsideConstraint = true, expected false")
			}
		})
	}
}
