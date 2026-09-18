package helm

import (
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
)

// TestFindLatestSemverWithRange covers targetRevision values that are semver ranges
// ("~1.2.0", "^1.2.0", "1.2.*") instead of pinned versions. ArgoCD resolves such ranges
// on its own, so LatestVersion must be the newest version inside the range, while
// LatestVersionAll reports the newest version overall. UpdateType is "in-range" as long as
// the range covers the newest version and "out-of-range" once it does not. A range that no
// available version satisfies is only "out-of-range" when a version newer than the range
// exists; when every version is older there is nothing to update to, so it is "none".
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
		expectedUpdateType        string
	}{
		{
			name:                      "tilde range allows patch updates only",
			versions:                  []string{"1.2.0", "1.2.5", "1.3.0", "2.0.0"},
			currentVersion:            "~1.2.0",
			constraint:                "major",
			expectedLatest:            "1.2.5",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
			expectedUpdateType:        UpdateTypeOutOfRange,
		},
		{
			name:                      "caret range allows minor updates",
			versions:                  []string{"1.2.0", "1.2.5", "1.9.0", "2.0.0"},
			currentVersion:            "^1.2.0",
			constraint:                "major",
			expectedLatest:            "1.9.0",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
			expectedUpdateType:        UpdateTypeOutOfRange,
		},
		{
			name:                      "wildcard range pins major and minor",
			versions:                  []string{"1.1.0", "1.2.3", "1.2.9", "1.3.0"},
			currentVersion:            "1.2.*",
			constraint:                "major",
			expectedLatest:            "1.2.9",
			expectedLatestAll:         "1.3.0",
			expectedOutsideConstraint: true,
			expectedUpdateType:        UpdateTypeOutOfRange,
		},
		{
			name:                      "explicit range with two comparators",
			versions:                  []string{"1.1.0", "1.2.0", "1.3.7", "1.4.0", "2.0.0"},
			currentVersion:            ">= 1.2.0, < 1.4.0",
			constraint:                "major",
			expectedLatest:            "1.3.7",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
			expectedUpdateType:        UpdateTypeOutOfRange,
		},
		{
			name:                      "latest version is inside the range",
			versions:                  []string{"1.2.0", "1.3.0", "1.5.0"},
			currentVersion:            "^1.2.0",
			constraint:                "major",
			expectedLatest:            "1.5.0",
			expectedLatestAll:         "1.5.0",
			expectedOutsideConstraint: false,
			expectedUpdateType:        UpdateTypeInRange,
		},
		{
			name:                      "only version available already satisfies the range",
			versions:                  []string{"1.2.0"},
			currentVersion:            "~1.2.0",
			constraint:                "major",
			expectedLatest:            "1.2.0",
			expectedLatestAll:         "1.2.0",
			expectedOutsideConstraint: false,
			expectedUpdateType:        UpdateTypeInRange,
		},
		{
			name:                      "tilde range resolves to a newer patch",
			versions:                  []string{"1.2.0", "1.2.3", "1.2.5"},
			currentVersion:            "~1.2.0",
			constraint:                "major",
			expectedLatest:            "1.2.5",
			expectedLatestAll:         "1.2.5",
			expectedOutsideConstraint: false,
			expectedUpdateType:        UpdateTypeInRange,
		},
		{
			name:                      "no version satisfies the range and newer ones exist",
			versions:                  []string{"2.0.0", "3.0.0"},
			currentVersion:            "~1.2.0",
			constraint:                "major",
			expectedLatest:            "~1.2.0",
			expectedLatestAll:         "3.0.0",
			expectedOutsideConstraint: true,
			expectedUpdateType:        UpdateTypeOutOfRange,
		},
		{
			name:                      "no version satisfies the range and all of them are older",
			versions:                  []string{"1.0.0", "2.0.0"},
			currentVersion:            "~3.1.0",
			constraint:                "major",
			expectedLatest:            "~3.1.0",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: false,
			expectedUpdateType:        UpdateTypeNone,
		},
		{
			name:                      "explicit range above every available version",
			versions:                  []string{"1.0.0", "2.0.0"},
			currentVersion:            ">= 3.0.0, < 4.0.0",
			constraint:                "major",
			expectedLatest:            ">= 3.0.0, < 4.0.0",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: false,
			expectedUpdateType:        UpdateTypeNone,
		},
		{
			name:                      "wildcard range above every available version",
			versions:                  []string{"1.0.0", "1.1.0"},
			currentVersion:            "1.2.*",
			constraint:                "major",
			expectedLatest:            "1.2.*",
			expectedLatestAll:         "1.1.0",
			expectedOutsideConstraint: false,
			expectedUpdateType:        UpdateTypeNone,
		},
		{
			name:                      "caret range above every available version",
			versions:                  []string{"v1.0.0", "v2.3.4"},
			currentVersion:            "^3.0.0",
			constraint:                "major",
			expectedLatest:            "^3.0.0",
			expectedLatestAll:         "v2.3.4",
			expectedOutsideConstraint: false,
			expectedUpdateType:        UpdateTypeNone,
		},
		{
			name:                      "original v prefix is preserved",
			versions:                  []string{"v1.2.0", "v1.5.0", "v2.0.0"},
			currentVersion:            "^1.2.0",
			constraint:                "major",
			expectedLatest:            "v1.5.0",
			expectedLatestAll:         "v2.0.0",
			expectedOutsideConstraint: true,
			expectedUpdateType:        UpdateTypeOutOfRange,
		},
		{
			name:                      "unparseable versions are skipped",
			versions:                  []string{"1.2.0", "latest", "1.2.8", "dev", "2.0.0"},
			currentVersion:            "~1.2.0",
			constraint:                "major",
			expectedLatest:            "1.2.8",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
			expectedUpdateType:        UpdateTypeOutOfRange,
		},
		{
			name:                      "pre-releases do not satisfy a stable range",
			versions:                  []string{"1.2.0", "1.2.5", "1.3.0-rc.1"},
			currentVersion:            "~1.2.0",
			constraint:                "major",
			expectedLatest:            "1.2.5",
			expectedLatestAll:         "1.3.0-rc.1",
			expectedOutsideConstraint: true,
			expectedUpdateType:        UpdateTypeOutOfRange,
		},
		{
			name:                      "range takes precedence over the cli constraint",
			versions:                  []string{"1.2.0", "1.2.5", "1.9.0", "2.0.0"},
			currentVersion:            "^1.2.0",
			constraint:                "patch",
			expectedLatest:            "1.9.0",
			expectedLatestAll:         "2.0.0",
			expectedOutsideConstraint: true,
			expectedUpdateType:        UpdateTypeOutOfRange,
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

			if result.UpdateType != test.expectedUpdateType {
				t.Errorf("UpdateType = %s, expected %s", result.UpdateType, test.expectedUpdateType)
			}
		})
	}
}

// TestFindLatestSemverUpdateTypeForPinnedRevision covers the UpdateType of a pinned
// targetRevision. Such a revision only moves when someone edits the Application manifest,
// so an update inside the CLI constraint is reported as "pinned", while an update that only
// exists outside the constraint keeps the application classified as "none".
func TestFindLatestSemverUpdateTypeForPinnedRevision(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	tests := []struct {
		name               string
		versions           []string
		currentVersion     string
		constraint         string
		expectedLatest     string
		expectedUpdateType string
	}{
		{
			name:               "patch update within constraint",
			versions:           []string{"1.2.0", "1.2.5"},
			currentVersion:     "1.2.0",
			constraint:         "patch",
			expectedLatest:     "1.2.5",
			expectedUpdateType: UpdateTypePinned,
		},
		{
			name:               "major update with major constraint",
			versions:           []string{"1.2.0", "2.0.0"},
			currentVersion:     "1.2.0",
			constraint:         "major",
			expectedLatest:     "2.0.0",
			expectedUpdateType: UpdateTypePinned,
		},
		{
			name:               "already on the latest version",
			versions:           []string{"1.0.0", "1.2.0"},
			currentVersion:     "1.2.0",
			constraint:         "major",
			expectedLatest:     "1.2.0",
			expectedUpdateType: UpdateTypeNone,
		},
		{
			name:               "update exists only outside the constraint",
			versions:           []string{"1.2.0", "1.3.0", "2.0.0"},
			currentVersion:     "1.2.0",
			constraint:         "patch",
			expectedLatest:     "1.2.0",
			expectedUpdateType: UpdateTypeNone,
		},
		{
			name:               "no version matches the constraint",
			versions:           []string{"2.0.0", "3.0.0"},
			currentVersion:     "1.2.0",
			constraint:         "minor",
			expectedLatest:     "1.2.0",
			expectedUpdateType: UpdateTypeNone,
		},
		{
			name:               "only older versions are available",
			versions:           []string{"1.0.0", "1.1.0"},
			currentVersion:     "1.2.0",
			constraint:         "minor",
			expectedLatest:     "1.2.0",
			expectedUpdateType: UpdateTypeNone,
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

			if result.UpdateType != test.expectedUpdateType {
				t.Errorf("UpdateType = %s, expected %s", result.UpdateType, test.expectedUpdateType)
			}
		})
	}
}

// TestRangeLowerBound checks the lowest version each supported range syntax can match, which
// is what separates versions rejected for being below a range from versions above it.
func TestRangeLowerBound(t *testing.T) {
	tests := []struct {
		rangeExpression string
		expected        string
	}{
		{rangeExpression: "~1.2.0", expected: "1.2.0"},
		{rangeExpression: "^1.2.0", expected: "1.2.0"},
		{rangeExpression: "~1.2", expected: "1.2.0"},
		{rangeExpression: "1.2.*", expected: "1.2.0"},
		{rangeExpression: "1.2.x", expected: "1.2.0"},
		{rangeExpression: ">= 1.2.0, < 1.4.0", expected: "1.2.0"},
		{rangeExpression: ">=1.2.0 <1.4.0", expected: "1.2.0"},
		{rangeExpression: "1.2.0 - 1.4.0", expected: "1.2.0"},
		{rangeExpression: "v3.1.0", expected: "3.1.0"},
		{rangeExpression: "~1.2.0-rc.1", expected: "1.2.0-rc.1"},
		{rangeExpression: "*", expected: ""},
		{rangeExpression: "main", expected: ""},
	}

	for _, test := range tests {
		t.Run(test.rangeExpression, func(t *testing.T) {
			lowerBound := rangeLowerBound(test.rangeExpression)

			if test.expected == "" {
				if lowerBound != nil {
					t.Fatalf("rangeLowerBound(%q) = %s, expected no lower bound", test.rangeExpression, lowerBound)
				}
				return
			}

			if lowerBound == nil {
				t.Fatalf("rangeLowerBound(%q) = nil, expected %s", test.rangeExpression, test.expected)
			}

			if lowerBound.String() != test.expected {
				t.Errorf("rangeLowerBound(%q) = %s, expected %s", test.rangeExpression, lowerBound, test.expected)
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
// every available version. Such a revision is not a range either, so ArgoCD never moves it
// on its own and the update is reported as "pinned".
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

			if result.UpdateType != UpdateTypePinned {
				t.Errorf("UpdateType = %s, expected %s", result.UpdateType, UpdateTypePinned)
			}
		})
	}
}
