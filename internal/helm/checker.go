package helm

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
)

// ChartVersionSource reports the versions available for a chart in a Helm repository.
// It is implemented by the ArgoCD client: ArgoCD already holds the repository
// credentials, so Argazer reads versions through its API instead of pulling index.yaml
// itself. An empty result means the repository holds no such chart.
//
// project is the ArgoCD project of the application that uses the chart. It is required to
// reach repositories whose credentials are scoped to a project instead of registered
// globally; for globally registered repositories it can be empty.
type ChartVersionSource interface {
	GetHelmChartVersions(ctx context.Context, repoURL, chartName, project string) ([]string, error)
}

// Checker checks Helm repositories for new chart versions
type Checker struct {
	versionSource ChartVersionSource
	ociChecker    *OCIChecker
	gitClient     *GitClient
	logger        *logrus.Entry
}

// NewChecker creates a new Helm checker. Versions of classic Helm repositories come from
// versionSource, usually the ArgoCD client.
func NewChecker(versionSource ChartVersionSource, logger *logrus.Entry) (*Checker, error) {
	if versionSource == nil {
		return nil, fmt.Errorf("chart version source is required")
	}

	return &Checker{
		versionSource: versionSource,
		ociChecker:    NewOCIChecker(logger.WithField("type", "oci")),
		gitClient:     NewGitClient("", "", logger.WithField("type", "git")),
		logger:        logger,
	}, nil
}

// GetLatestVersion gets the latest version of a Helm chart from a repository.
// project is the ArgoCD project of the application, needed for project-scoped repositories.
func (c *Checker) GetLatestVersion(ctx context.Context, repoURL, chartName, project string) (string, error) {
	// Check if this is a Git repository
	if isGitURL(repoURL) {
		c.logger.WithFields(logrus.Fields{
			"repo":  repoURL,
			"chart": chartName,
		}).Info("Detected Git repository, using Git checker")

		// Use chartName as the path within the repo
		return c.gitClient.GetLatestVersion(ctx, repoURL, chartName)
	}

	// Check if this is an OCI repository (no http/https prefix)
	if !strings.HasPrefix(repoURL, "http://") && !strings.HasPrefix(repoURL, "https://") {
		c.logger.WithFields(logrus.Fields{
			"repo":  repoURL,
			"chart": chartName,
		}).Info("Detected OCI repository, using OCI checker")
		return c.ociChecker.GetLatestVersion(ctx, repoURL, chartName)
	}
	return c.getLatestVersionFromRepo(ctx, repoURL, chartName, project)
}

// GetLatestVersionWithConstraint gets the latest version respecting the version constraint.
// project is the ArgoCD project of the application, needed for project-scoped repositories.
func (c *Checker) GetLatestVersionWithConstraint(ctx context.Context, repoURL, chartName, project, currentVersion, constraint string) (*VersionConstraintResult, error) {
	// Check if this is a Git repository
	if isGitURL(repoURL) {
		c.logger.WithFields(logrus.Fields{
			"repo":       repoURL,
			"chart":      chartName,
			"constraint": constraint,
		}).Info("Detected Git repository, using Git checker with constraint")

		// Get all versions from Git tags
		versions, err := c.gitClient.GetAllVersions(ctx, repoURL, chartName)
		if err != nil {
			return nil, err
		}

		// Apply constraint logic
		return findLatestSemverWithConstraint(versions, currentVersion, constraint, c.logger)
	}

	// Check if this is an OCI repository (no http/https prefix)
	if !strings.HasPrefix(repoURL, "http://") && !strings.HasPrefix(repoURL, "https://") {
		c.logger.WithFields(logrus.Fields{
			"repo":  repoURL,
			"chart": chartName,
		}).Info("Detected OCI repository, using OCI checker")
		// Use OCI checker with constraint support
		return c.ociChecker.GetLatestVersionWithConstraint(ctx, repoURL, chartName, currentVersion, constraint)
	}

	return c.getLatestVersionFromRepoWithConstraint(ctx, repoURL, chartName, project, currentVersion, constraint)
}

// getChartVersionsFromRepo returns all versions ArgoCD reports for a chart in a Helm
// repository.
func (c *Checker) getChartVersionsFromRepo(ctx context.Context, repoURL, chartName, project string) ([]string, error) {
	c.logger.WithFields(logrus.Fields{
		"repo":    repoURL,
		"chart":   chartName,
		"project": project,
	}).Debug("Asking ArgoCD for the chart versions of a Helm repository")

	versions, err := c.versionSource.GetHelmChartVersions(ctx, repoURL, chartName, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get chart versions from ArgoCD: %w", err)
	}

	// ArgoCD answers with the charts of the whole repository, so a chart it does not
	// mention is a chart the repository does not have.
	if len(versions) == 0 {
		return nil, fmt.Errorf("%w: %s in %s", ErrChartNotFound, chartName, repoURL)
	}

	return versions, nil
}

func (c *Checker) getLatestVersionFromRepo(ctx context.Context, repoURL, chartName, project string) (string, error) {
	// Fetch all versions
	versions, err := c.getChartVersionsFromRepo(ctx, repoURL, chartName, project)
	if err != nil {
		return "", err
	}

	// Use shared utility function for finding latest semantic version
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

// getLatestVersionFromRepoWithConstraint gets the latest version with constraint support
func (c *Checker) getLatestVersionFromRepoWithConstraint(ctx context.Context, repoURL, chartName, project, currentVersion, constraint string) (*VersionConstraintResult, error) {
	// Fetch all versions using shared helper
	versions, err := c.getChartVersionsFromRepo(ctx, repoURL, chartName, project)
	if err != nil {
		return nil, err
	}

	// Apply constraint filtering
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
