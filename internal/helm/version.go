package helm

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/sirupsen/logrus"
)

// Update types describe whether an available update is applied by ArgoCD on its own or
// requires someone to change the Application manifest.
const (
	// UpdateTypeNone means no update is available within the policy of targetRevision.
	UpdateTypeNone = "none"
	// UpdateTypeInRange means a newer version satisfies the targetRevision range, so ArgoCD
	// deploys it without any manifest change.
	UpdateTypeInRange = "in-range"
	// UpdateTypeOutOfRange means a newer version exists beyond what the targetRevision range
	// allows, so the range has to be widened manually.
	UpdateTypeOutOfRange = "out-of-range"
	// UpdateTypePinned means targetRevision is a fixed revision and a newer version is
	// available, so the pinned value has to be bumped manually.
	UpdateTypePinned = "pinned"
)

// VersionConstraintResult holds the result of version constraint filtering
type VersionConstraintResult struct {
	LatestVersion              string // Latest version within constraint
	LatestVersionAll           string // Latest version without constraint
	HasUpdateOutsideConstraint bool   // True if newer versions exist outside constraint
	UpdateType                 string // One of "none", "in-range", "out-of-range", "pinned"
}

// findLatestSemver determines the latest semantic version from a list of version strings.
// It filters out any strings that cannot be parsed as valid semantic versions,
// ensuring only valid versions are compared.
func findLatestSemver(versions []string, logger *logrus.Entry) (string, error) {
	if len(versions) == 0 {
		return "", fmt.Errorf("no versions provided")
	}

	// Parse all versions and keep track of their original strings
	validVersions := parseVersions(versions, logger)
	if len(validVersions) == 0 {
		return "", ErrNoValidVersions
	}

	sortVersionsDesc(validVersions)

	// Return the original string representation of the highest version
	return validVersions[0].original, nil
}

// versionPair keeps a parsed version alongside the string it came from, so the
// original representation (e.g. with a "v" prefix) can be returned unchanged.
type versionPair struct {
	original string
	parsed   *semver.Version
}

// parseVersions parses the given strings, skipping the ones that are not valid semver.
func parseVersions(versions []string, logger *logrus.Entry) []versionPair {
	var parsedVersions []versionPair

	for _, v := range versions {
		parsed, err := semver.NewVersion(v)
		if err != nil {
			logger.WithFields(logrus.Fields{
				"version": v,
				"error":   err.Error(),
			}).Debug("Skipping invalid semantic version")
			continue
		}
		parsedVersions = append(parsedVersions, versionPair{
			original: v,
			parsed:   parsed,
		})
	}

	return parsedVersions
}

// sortVersionsDesc sorts versions from newest to oldest.
func sortVersionsDesc(versions []versionPair) {
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].parsed.Compare(versions[j].parsed) > 0
	})
}

// findLatestSemverWithConstraint finds the latest version respecting the given constraint.
//
// currentVersion is the ArgoCD targetRevision and may be either a pinned version
// ("1.2.3") or a semver range ("~1.2.0", "^1.2.0", "1.2.*"). For a pinned version the
// constraint argument ("patch", "minor", "major") defines what counts as an in-policy
// update. For a range, the range itself is the policy: ArgoCD resolves it on its own,
// so the latest version satisfying it is what will actually be deployed. UpdateType lets
// callers tell the updates ArgoCD applies itself from the ones needing a manifest change.
func findLatestSemverWithConstraint(versions []string, currentVersion, constraint string, logger *logrus.Entry) (*VersionConstraintResult, error) {
	if len(versions) == 0 {
		return nil, fmt.Errorf("no versions provided")
	}

	// Parse current version
	current, err := semver.NewVersion(currentVersion)
	if err != nil {
		// Not a pinned version: it may still be a range such as "~1.2.0" or "1.2.*"
		if versionRange, rangeErr := semver.NewConstraint(currentVersion); rangeErr == nil {
			logger.WithFields(logrus.Fields{
				"current_version": currentVersion,
			}).Debug("Current version is a semver range, resolving latest version within the range")
			return findLatestSemverInRange(versions, currentVersion, versionRange, logger)
		}

		// Neither a version nor a range (e.g. a branch name), fall back to no constraint
		logger.WithFields(logrus.Fields{
			"current_version": currentVersion,
			"error":           err.Error(),
		}).Warn("Current version is not valid semver, checking all versions")
		latest, err := findLatestSemver(versions, logger)
		if err != nil {
			return nil, err
		}
		// The revision is not a range, so ArgoCD keeps deploying it as is and will never pick
		// up a newer chart version on its own: treat it like a pinned revision.
		return &VersionConstraintResult{
			LatestVersion:              latest,
			LatestVersionAll:           latest,
			HasUpdateOutsideConstraint: false,
			UpdateType:                 UpdateTypePinned,
		}, nil
	}

	// Parse all versions and filter by constraint
	allValidVersions := parseVersions(versions, logger)
	if len(allValidVersions) == 0 {
		return nil, ErrNoValidVersions
	}

	var constrainedVersions []versionPair

	for _, v := range allValidVersions {
		// Apply constraint filter
		matchesConstraint := false
		switch constraint {
		case "patch":
			// Same major and minor
			matchesConstraint = v.parsed.Major() == current.Major() && v.parsed.Minor() == current.Minor()
		case "minor":
			// Same major only
			matchesConstraint = v.parsed.Major() == current.Major()
		case "major", "":
			// All versions
			matchesConstraint = true
		}

		if matchesConstraint {
			constrainedVersions = append(constrainedVersions, v)
		}
	}

	sortVersionsDesc(allValidVersions)
	latestAll := allValidVersions[0].original

	result := &VersionConstraintResult{
		LatestVersionAll:           latestAll,
		HasUpdateOutsideConstraint: false,
	}

	if len(constrainedVersions) == 0 {
		// No versions match constraint, return current as latest within constraint
		result.LatestVersion = currentVersion
		result.HasUpdateOutsideConstraint = latestAll != currentVersion
		result.UpdateType = UpdateTypeNone
		return result, nil
	}

	sortVersionsDesc(constrainedVersions)
	latestConstrained := constrainedVersions[0].original

	// Only return a newer version if it's actually newer than current
	latestConstrainedVer := constrainedVersions[0].parsed
	if latestConstrainedVer.Compare(current) > 0 {
		result.LatestVersion = latestConstrained
		result.UpdateType = UpdateTypePinned
	} else {
		// All constrained versions are older or equal to current
		result.LatestVersion = currentVersion
		result.UpdateType = UpdateTypeNone
	}

	// Check if there are newer versions outside constraint
	if constraint != "major" && constraint != "" {
		latestAllVer := allValidVersions[0].parsed
		result.HasUpdateOutsideConstraint = latestAllVer.Compare(current) > 0 && latestAllVer.Compare(latestConstrainedVer) > 0
	}

	return result, nil
}

// findLatestSemverInRange resolves a semver range (the ArgoCD targetRevision) against the
// available versions. LatestVersion is the newest version satisfying the range, i.e. the one
// ArgoCD will deploy on its own, while LatestVersionAll reports the newest version overall so
// callers can tell whether an update exists beyond what the range allows.
func findLatestSemverInRange(versions []string, rangeExpression string, versionRange *semver.Constraints, logger *logrus.Entry) (*VersionConstraintResult, error) {
	allValidVersions := parseVersions(versions, logger)
	if len(allValidVersions) == 0 {
		return nil, ErrNoValidVersions
	}

	var inRangeVersions []versionPair
	for _, v := range allValidVersions {
		if versionRange.Check(v.parsed) {
			inRangeVersions = append(inRangeVersions, v)
		}
	}

	sortVersionsDesc(allValidVersions)
	latestAll := allValidVersions[0]

	result := &VersionConstraintResult{
		LatestVersionAll:           latestAll.original,
		HasUpdateOutsideConstraint: false,
	}

	if len(inRangeVersions) == 0 {
		// Nothing satisfies the range. Widening it is only worth reporting when a version
		// newer than the range exists; if every available version is older (a range pointing
		// at a chart version that was never published), there is nothing to update to.
		result.LatestVersion = rangeExpression
		if lowerBound := rangeLowerBound(rangeExpression); lowerBound != nil && latestAll.parsed.LessThan(lowerBound) {
			logger.WithFields(logrus.Fields{
				"current_version": rangeExpression,
				"latest_version":  latestAll.original,
			}).Debug("No available version satisfies the range, all of them are older than the range")
			result.UpdateType = UpdateTypeNone
			return result, nil
		}

		logger.WithFields(logrus.Fields{
			"current_version": rangeExpression,
			"latest_version":  latestAll.original,
		}).Debug("No available version satisfies the range")
		result.HasUpdateOutsideConstraint = true
		result.UpdateType = UpdateTypeOutOfRange
		return result, nil
	}

	sortVersionsDesc(inRangeVersions)
	latestInRange := inRangeVersions[0]

	result.LatestVersion = latestInRange.original
	result.HasUpdateOutsideConstraint = latestAll.parsed.Compare(latestInRange.parsed) > 0

	// The version deployed for a range is chosen by ArgoCD, so whatever the range resolves to
	// needs no attention; only a version beyond the range requires widening it manually.
	if result.HasUpdateOutsideConstraint {
		result.UpdateType = UpdateTypeOutOfRange
	} else {
		result.UpdateType = UpdateTypeInRange
	}

	return result, nil
}

// versionLiteralPattern matches the version literals inside a range expression, including
// wildcards ("1.2.*", "1.2.x") and partial versions ("~1.2").
var versionLiteralPattern = regexp.MustCompile(`v?\d+(?:\.[0-9xX*]+){0,2}(?:[-+][0-9A-Za-z.-]+)?`)

// rangeLowerBound reports the lowest version a range expression can possibly match, which is
// the smallest version literal it mentions: "~3.1.0" cannot match anything below 3.1.0 and
// ">= 1.2.0, < 1.4.0" cannot match anything below 1.2.0. It is used to tell whether versions
// rejected by a range sit above or below it. Nil is returned when the expression holds no
// parseable literal, in which case callers cannot draw that conclusion.
func rangeLowerBound(rangeExpression string) *semver.Version {
	var lowest *semver.Version

	for _, literal := range versionLiteralPattern.FindAllString(rangeExpression, -1) {
		parsed, err := semver.NewVersion(normalizeVersionLiteral(literal))
		if err != nil {
			continue
		}
		if lowest == nil || parsed.LessThan(lowest) {
			lowest = parsed
		}
	}

	return lowest
}

// normalizeVersionLiteral turns a version literal taken from a range expression into a
// parseable version by replacing wildcard segments with zeros ("1.2.*" becomes "1.2.0").
func normalizeVersionLiteral(literal string) string {
	core, suffix := literal, ""
	if i := strings.IndexAny(literal, "-+"); i >= 0 {
		core, suffix = literal[:i], literal[i:]
	}

	return strings.NewReplacer("x", "0", "X", "0", "*", "0").Replace(core) + suffix
}
