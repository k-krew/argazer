package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// OCIChecker checks OCI-based Helm repositories for new chart versions
type OCIChecker struct {
	httpClient *http.Client
	logger     *logrus.Entry
}

// NewOCIChecker creates a new OCI checker
func NewOCIChecker(logger *logrus.Entry) *OCIChecker {
	return &OCIChecker{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// TagsResponse represents the response from Docker Registry API v2 tags list endpoint
type TagsResponse struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

// getTagsFromOCI fetches all available tags for a chart from an OCI registry
func (o *OCIChecker) getTagsFromOCI(ctx context.Context, repoURL, chartName string) ([]string, error) {
	o.logger.WithFields(logrus.Fields{
		"repo":  repoURL,
		"chart": chartName,
	}).Debug("Checking OCI registry for tags")

	// Parse OCI registry URL and build repository path
	registry, repoPath := parseOCIURL(repoURL)

	// Build full repository path: repoPath/chartName
	var fullRepoPath string
	if repoPath != "" {
		fullRepoPath = fmt.Sprintf("%s/%s", repoPath, chartName)
	} else {
		fullRepoPath = chartName
	}

	o.logger.WithFields(logrus.Fields{
		"registry":       registry,
		"repo_path":      repoPath,
		"full_repo_path": fullRepoPath,
	}).Debug("Parsed OCI URL")

	// Determine the scheme - default to https unless explicitly http for localhost/testing
	scheme := "https"
	if strings.HasPrefix(registry, "localhost") || strings.HasPrefix(registry, "127.0.0.1") {
		// Allow http for localhost/testing
		scheme = "http"
	}

	// Build Docker Registry API v2 endpoint
	tagsURL := fmt.Sprintf("%s://%s/v2/%s/tags/list", scheme, registry, fullRepoPath)

	o.logger.WithField("url", tagsURL).Debug("Fetching tags from OCI registry")

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", tagsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("User-Agent", "argazer/1.0")
	req.Header.Set("Accept", "application/json")

	// Make request
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tags from OCI registry: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			o.logger.WithError(err).Warn("Failed to close response body")
		}
	}()

	// Check response status
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w for %s (status %d): the registry does not allow anonymous reads", ErrAuthenticationFailed, registry, resp.StatusCode)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s/%s", ErrChartNotFound, registry, chartName)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OCI registry returned status %d", resp.StatusCode)
	}

	// Parse response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var tagsResp TagsResponse
	if err := json.Unmarshal(body, &tagsResp); err != nil {
		return nil, fmt.Errorf("failed to parse tags response: %w", err)
	}

	if len(tagsResp.Tags) == 0 {
		return nil, fmt.Errorf("no tags found for chart %s in OCI registry", chartName)
	}

	o.logger.WithFields(logrus.Fields{
		"chart":      chartName,
		"tags_count": len(tagsResp.Tags),
		"tags":       tagsResp.Tags,
	}).Debug("Retrieved tags from OCI registry")

	// Filter out common non-version tags before finding latest
	var candidateTags []string
	excludedTags := map[string]bool{
		"latest": true,
		"dev":    true,
		"main":   true,
		"master": true,
		"stable": true,
	}

	for _, tag := range tagsResp.Tags {
		if !excludedTags[tag] {
			candidateTags = append(candidateTags, tag)
		}
	}

	if len(candidateTags) == 0 {
		return nil, fmt.Errorf("%w: all tags were filtered out", ErrNoValidVersions)
	}

	return candidateTags, nil
}

// GetLatestVersion gets the latest version of a Helm chart from an OCI registry
func (o *OCIChecker) GetLatestVersion(ctx context.Context, repoURL, chartName string) (string, error) {
	// Fetch all tags using shared helper
	candidateTags, err := o.getTagsFromOCI(ctx, repoURL, chartName)
	if err != nil {
		return "", err
	}

	// Use shared utility function to find the latest semantic version
	// This will parse each tag with semver and filter out invalid ones
	latestVersion, err := findLatestSemver(candidateTags, o.logger)
	if err != nil {
		return "", fmt.Errorf("failed to determine latest version: %w", err)
	}

	o.logger.WithFields(logrus.Fields{
		"chart":          chartName,
		"latest_version": latestVersion,
	}).Debug("Found latest version in OCI registry")

	return latestVersion, nil
}

// GetLatestVersionWithConstraint gets the latest version respecting the version constraint
func (o *OCIChecker) GetLatestVersionWithConstraint(ctx context.Context, repoURL, chartName, currentVersion, constraint string) (*VersionConstraintResult, error) {
	o.logger.WithFields(logrus.Fields{
		"repo":       repoURL,
		"chart":      chartName,
		"constraint": constraint,
	}).Debug("Checking OCI registry for latest version with constraint")

	// Fetch all tags using shared helper
	candidateTags, err := o.getTagsFromOCI(ctx, repoURL, chartName)
	if err != nil {
		return nil, err
	}

	// Apply constraint filtering
	result, err := findLatestSemverWithConstraint(candidateTags, currentVersion, constraint, o.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to determine latest version: %w", err)
	}

	o.logger.WithFields(logrus.Fields{
		"chart":                         chartName,
		"current_version":               currentVersion,
		"latest_version":                result.LatestVersion,
		"latest_version_all":            result.LatestVersionAll,
		"constraint":                    constraint,
		"has_update_outside_constraint": result.HasUpdateOutsideConstraint,
	}).Debug("Found latest version in OCI registry with constraint")

	return result, nil
}

// parseOCIURL parses an OCI registry URL into registry and repository path
// Examples:
//   - "ghcr.io/myorg/charts" -> registry: "ghcr.io", repoPath: "myorg/charts"
//   - "harbor.company.com/helm" -> registry: "harbor.company.com", repoPath: "helm"
//   - "registry.example.com" -> registry: "registry.example.com", repoPath: ""
func parseOCIURL(repoURL string) (registry string, repoPath string) {
	// Remove any trailing slashes
	repoURL = strings.TrimSuffix(repoURL, "/")

	// Split by first slash
	parts := strings.SplitN(repoURL, "/", 2)
	registry = parts[0]

	if len(parts) > 1 {
		repoPath = parts[1]
	}

	return registry, repoPath
}
