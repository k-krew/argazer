package helm

import (
	"fmt"
	"sort"

	"github.com/Masterminds/semver/v3"
	"github.com/sirupsen/logrus"
)

// VersionConstraintResult holds the result of version constraint filtering
type VersionConstraintResult struct {
	LatestVersion              string // Latest version within constraint
	LatestVersionAll           string // Latest version without constraint
	HasUpdateOutsideConstraint bool   // True if newer versions exist outside constraint
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
// so the latest version satisfying it is what will actually be deployed.
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
		return &VersionConstraintResult{
			LatestVersion:              latest,
			LatestVersionAll:           latest,
			HasUpdateOutsideConstraint: false,
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
		return result, nil
	}

	sortVersionsDesc(constrainedVersions)
	latestConstrained := constrainedVersions[0].original

	// Only return a newer version if it's actually newer than current
	latestConstrainedVer := constrainedVersions[0].parsed
	if latestConstrainedVer.Compare(current) > 0 {
		result.LatestVersion = latestConstrained
	} else {
		// All constrained versions are older or equal to current
		result.LatestVersion = currentVersion
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
		// Nothing satisfies the range, so every available version is out of policy
		logger.WithFields(logrus.Fields{
			"current_version": rangeExpression,
			"latest_version":  latestAll.original,
		}).Debug("No available version satisfies the range")
		result.LatestVersion = rangeExpression
		result.HasUpdateOutsideConstraint = true
		return result, nil
	}

	sortVersionsDesc(inRangeVersions)
	latestInRange := inRangeVersions[0]

	result.LatestVersion = latestInRange.original
	result.HasUpdateOutsideConstraint = latestAll.parsed.Compare(latestInRange.parsed) > 0

	return result, nil
}
