package helm

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
)

// ChartVersionSource reports what versions of a chart a repository holds. It is implemented
// by the ArgoCD client: ArgoCD already holds the repository credentials, so Argazer reads
// versions through its API instead of reaching the repository itself. An empty result means
// the repository is reachable but holds no such chart.
//
// project is the ArgoCD project of the application that uses the chart. It is required to
// reach repositories whose credentials are scoped to a project instead of registered
// globally; for globally registered repositories it can be empty.
type ChartVersionSource interface {
	// GetHelmChartVersions returns the chart versions of a classic Helm repository.
	GetHelmChartVersions(ctx context.Context, repoURL, chartName, project string) ([]string, error)
	// GetOCITags returns the tags of the OCI artifact holding the chart.
	GetOCITags(ctx context.Context, repoURL, chartName, project string) ([]string, error)
	// GetGitTags returns the tags of a Git repository holding charts.
	GetGitTags(ctx context.Context, repoURL, project string) ([]string, error)
}

// Checker checks Helm repositories for new chart versions
type Checker struct {
	versionSource ChartVersionSource
	logger        *logrus.Entry
}

// NewChecker creates a new Helm checker. Versions of every kind of repository come from
// versionSource, usually the ArgoCD client.
func NewChecker(versionSource ChartVersionSource, logger *logrus.Entry) (*Checker, error) {
	if versionSource == nil {
		return nil, fmt.Errorf("chart version source is required")
	}

	return &Checker{
		versionSource: versionSource,
		logger:        logger,
	}, nil
}

// GetLatestVersion gets the latest version of a Helm chart from a repository.
// project is the ArgoCD project of the application, needed for project-scoped repositories.
func (c *Checker) GetLatestVersion(ctx context.Context, repoURL, chartName, project string) (string, error) {
	versions, err := c.chartVersions(ctx, repoURL, chartName, project)
	if err != nil {
		return "", err
	}

	latestVersion, err := findLatestSemver(versions, c.logger)
	if err != nil {
		return "", fmt.Errorf("failed to determine latest version: %w", err)
	}

	c.logger.WithFields(logrus.Fields{
		"repo":           repoURL,
		"chart":          chartName,
		"latest_version": latestVersion,
	}).Debug("Found latest version")

	return latestVersion, nil
}

// GetLatestVersionWithConstraint gets the latest version respecting the version constraint.
// project is the ArgoCD project of the application, needed for project-scoped repositories.
func (c *Checker) GetLatestVersionWithConstraint(ctx context.Context, repoURL, chartName, project, currentVersion, constraint string) (*VersionConstraintResult, error) {
	versions, err := c.chartVersions(ctx, repoURL, chartName, project)
	if err != nil {
		return nil, err
	}

	result, err := findLatestSemverWithConstraint(versions, currentVersion, constraint, c.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to determine latest version: %w", err)
	}

	c.logger.WithFields(logrus.Fields{
		"repo":                          repoURL,
		"chart":                         chartName,
		"current_version":               currentVersion,
		"latest_version":                result.LatestVersion,
		"latest_version_all":            result.LatestVersionAll,
		"constraint":                    constraint,
		"has_update_outside_constraint": result.HasUpdateOutsideConstraint,
	}).Debug("Found latest version with constraint")

	return result, nil
}

// chartVersions returns the versions available for a chart, asking ArgoCD in the way that
// fits the kind of repository the chart comes from. Whatever the kind, ArgoCD is the one
// talking to the repository, so private Git repositories and OCI registries need no
// credentials in Argazer.
func (c *Checker) chartVersions(ctx context.Context, repoURL, chartName, project string) ([]string, error) {
	logger := c.logger.WithFields(logrus.Fields{
		"repo":    repoURL,
		"chart":   chartName,
		"project": project,
	})

	var versions []string

	switch {
	case isGitURL(repoURL):
		logger.Debug("Asking ArgoCD for the tags of a Git repository")

		tags, err := c.versionSource.GetGitTags(ctx, repoURL, project)
		if err != nil {
			return nil, fmt.Errorf("failed to get Git tags from ArgoCD: %w", err)
		}

		// A chart in Git is a directory, and its releases are tags of the whole
		// repository, so the tags belonging to other charts have to be left out.
		versions = gitTagVersions(tags, chartName)

	case isOCIURL(repoURL):
		logger.Debug("Asking ArgoCD for the tags of an OCI artifact")

		// An OCI tag is the chart version, and tags that are no version at all (such as
		// "latest") are dropped later, when the versions are parsed.
		tags, err := c.versionSource.GetOCITags(ctx, repoURL, chartName, project)
		if err != nil {
			return nil, fmt.Errorf("failed to get OCI tags from ArgoCD: %w", err)
		}

		versions = tags

	default:
		logger.Debug("Asking ArgoCD for the chart versions of a Helm repository")

		helmVersions, err := c.versionSource.GetHelmChartVersions(ctx, repoURL, chartName, project)
		if err != nil {
			return nil, fmt.Errorf("failed to get chart versions from ArgoCD: %w", err)
		}

		versions = helmVersions
	}

	// ArgoCD answers with everything a repository holds, so a chart it says nothing about
	// is a chart the repository does not have.
	if len(versions) == 0 {
		return nil, fmt.Errorf("%w: %s in %s", ErrChartNotFound, chartName, repoURL)
	}

	return versions, nil
}
