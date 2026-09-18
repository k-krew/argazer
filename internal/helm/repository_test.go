package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsGitURL(t *testing.T) {
	tests := []struct {
		name     string
		repoURL  string
		expected bool
	}{
		{"git suffix", "https://github.com/myorg/charts.git", true},
		{"ssh url", "git@github.com:myorg/charts.git", true},
		{"github without suffix", "https://github.com/myorg/charts", true},
		{"gitlab", "https://gitlab.com/myorg/charts", true},
		{"bitbucket", "https://bitbucket.org/myorg/charts", true},
		{"self-hosted gitea", "https://gitea.example.com/myorg/charts", true},
		{"helm repository", "https://charts.example.com", false},
		{"oci registry", "ghcr.io/myorg/charts", false},
		{"oci registry with scheme", "oci://ghcr.io/myorg/charts", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isGitURL(tt.repoURL))
		})
	}
}

func TestIsOCIURL(t *testing.T) {
	tests := []struct {
		name     string
		repoURL  string
		expected bool
	}{
		{"registry with path", "ghcr.io/myorg/charts", true},
		{"registry only", "registry.example.com", true},
		{"oci scheme", "oci://ghcr.io/myorg/charts", true},
		{"oci scheme in capitals", "OCI://ghcr.io/myorg/charts", true},
		{"helm repository", "https://charts.example.com", false},
		{"plain http helm repository", "http://charts.example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isOCIURL(tt.repoURL))
		})
	}
}

func TestGitTagVersions(t *testing.T) {
	tests := []struct {
		name      string
		tags      []string
		chartPath string
		expected  []string
	}{
		{
			name:      "plain versions",
			tags:      []string{"1.2.3", "1.3.0"},
			chartPath: "charts/nginx",
			expected:  []string{"1.2.3", "1.3.0"},
		},
		{
			name:      "v prefix is kept for the semver parser",
			tags:      []string{"v1.2.3"},
			chartPath: "charts/nginx",
			expected:  []string{"v1.2.3"},
		},
		{
			name:      "release prefix",
			tags:      []string{"release-1.2.3"},
			chartPath: "",
			expected:  []string{"1.2.3"},
		},
		{
			name:      "chart prefix",
			tags:      []string{"chart-1.2.3"},
			chartPath: "",
			expected:  []string{"1.2.3"},
		},
		{
			name:      "tags of the asked chart only",
			tags:      []string{"nginx-1.2.3", "nginx-v1.3.0", "redis-9.9.9"},
			chartPath: "charts/nginx",
			expected:  []string{"1.2.3", "v1.3.0"},
		},
		{
			name:      "chart path with a trailing slash",
			tags:      []string{"nginx-1.2.3"},
			chartPath: "charts/nginx/",
			expected:  []string{"1.2.3"},
		},
		{
			name:      "tags that hold no version",
			tags:      []string{"latest", "main", "some-tag"},
			chartPath: "charts/nginx",
			expected:  []string{},
		},
		{
			name:      "without a chart path every version tag counts",
			tags:      []string{"1.2.3", "redis-9.9.9"},
			chartPath: "",
			expected:  []string{"1.2.3"},
		},
		{
			name:      "no tags at all",
			tags:      nil,
			chartPath: "charts/nginx",
			expected:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, gitTagVersions(tt.tags, tt.chartPath))
		})
	}
}
