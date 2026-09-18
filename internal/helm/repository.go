package helm

import (
	"path"
	"strings"
)

// isGitURL determines if a URL is a Git repository
func isGitURL(repoURL string) bool {
	// Git URLs typically:
	// - Contain .git
	// - Start with git@
	// - Are GitHub/GitLab/Bitbucket URLs without /helm suffix
	// - Don't have http/https prefix for OCI registries (those are handled separately)

	lower := strings.ToLower(repoURL)

	// Explicit Git URLs
	if strings.HasSuffix(lower, ".git") || strings.HasPrefix(lower, "git@") {
		return true
	}

	// Common Git hosting platforms (if they don't look like OCI registries)
	gitPlatforms := []string{"github.com", "gitlab.com", "bitbucket.org", "gitea"}
	for _, platform := range gitPlatforms {
		if strings.Contains(lower, platform) {
			// Not an OCI URL (no oci:// prefix and has http/https)
			if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
				return true
			}
		}
	}

	return false
}

// isOCIURL determines if a URL points at an OCI registry. A classic Helm repository is
// served over HTTP, while an OCI reference is a registry host with a path, either bare
// ("ghcr.io/myorg/charts") or with the oci:// scheme ArgoCD accepts in an Application.
func isOCIURL(repoURL string) bool {
	lower := strings.ToLower(repoURL)

	return strings.HasPrefix(lower, "oci://") ||
		(!strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://"))
}

// gitTagVersions picks the chart versions out of the tags of a Git repository.
//
// A Git repository holds the chart as a directory, so its tags are tags of everything in
// it. Tags of a single chart are commonly prefixed with its name ("nginx-1.2.3"), which is
// how the versions of the wanted chart are told apart from the ones of its neighbours;
// repositories holding one chart tag it plainly ("v1.2.3", "release-1.2.3"). Tags that are
// no version at all are left out.
func gitTagVersions(tags []string, chartPath string) []string {
	// The chart is given as a path within the repository, and only its last element can
	// show up in a tag.
	chartPrefix := ""
	if chartPath != "" {
		chartPrefix = path.Base(strings.Trim(chartPath, "/")) + "-"
	}

	versions := make([]string, 0, len(tags))
	for _, tag := range tags {
		version, ok := gitTagVersion(tag, chartPrefix)
		if !ok {
			continue
		}

		versions = append(versions, version)
	}

	return versions
}

// gitTagVersion reduces a single Git tag to the version it carries, and reports whether it
// carries one at all. A tag naming another chart of the repository does not.
func gitTagVersion(tag, chartPrefix string) (string, bool) {
	version := tag
	if chartPrefix != "" && strings.HasPrefix(tag, chartPrefix) {
		version = strings.TrimPrefix(tag, chartPrefix)
	} else {
		version = strings.TrimPrefix(version, "release-")
		version = strings.TrimPrefix(version, "chart-")
	}

	// Whether what is left is a version is up to the semver parser: a leading "v" is fine
	// for it, and the tag of another chart is not.
	if !isSemver(version) {
		return "", false
	}

	return version, true
}
