package argocd

import (
	"context"
	"errors"
	"testing"

	"github.com/argoproj/argo-cd/v2/pkg/apiclient/repository"
	reposerver "github.com/argoproj/argo-cd/v2/reposerver/apiclient"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// stubHelmChartLister records the query ArgoCD receives and answers with a canned response.
type stubHelmChartLister struct {
	response *reposerver.HelmChartsResponse
	err      error

	lastQuery *repository.RepoQuery
}

func (s *stubHelmChartLister) GetHelmCharts(_ context.Context, in *repository.RepoQuery, _ ...grpc.CallOption) (*reposerver.HelmChartsResponse, error) {
	s.lastQuery = in

	if s.err != nil {
		return nil, s.err
	}

	return s.response, nil
}

func newTestClient(lister helmChartLister) *Client {
	return &Client{
		repoClient: lister,
		logger:     logrus.NewEntry(logrus.New()),
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		item     string
		expected bool
	}{
		{
			name:     "item exists",
			slice:    []string{"app1", "app2", "app3"},
			item:     "app2",
			expected: true,
		},
		{
			name:     "item does not exist",
			slice:    []string{"app1", "app2", "app3"},
			item:     "app4",
			expected: false,
		},
		{
			name:     "empty slice",
			slice:    []string{},
			item:     "app1",
			expected: false,
		},
		{
			name:     "wildcard exists",
			slice:    []string{"*"},
			item:     "*",
			expected: true,
		},
		{
			name:     "single item match",
			slice:    []string{"app1"},
			item:     "app1",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := contains(tt.slice, tt.item)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNewClient_InvalidURL(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	// Test with invalid/unreachable ArgoCD server
	_, err := NewClient("http://invalid-argocd-server-that-does-not-exist.example.com", "admin", "password", "", false, logger)
	// Should fail because the server doesn't exist
	assert.Error(t, err)
}

func TestNewClient_EmptyCredentials(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	// Test with empty credentials
	_, err := NewClient("http://localhost:8080", "", "", "", false, logger)
	// Should fail during authentication
	assert.Error(t, err)
}

// TestNewClient_AuthToken checks that a token short-circuits the login round-trip:
// the client is built without ever reaching the (non-existent) server.
func TestNewClient_AuthToken(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	client, err := NewClient("http://invalid-argocd-server-that-does-not-exist.example.com", "", "", "some-token", false, logger)
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestFindChartVersions(t *testing.T) {
	charts := &reposerver.HelmChartsResponse{
		Items: []*reposerver.HelmChart{
			{Name: "redis", Versions: []string{"7.0.0", "7.1.0"}},
			nil,
			{Name: "nginx", Versions: []string{"1.20.0", "1.21.0"}},
		},
	}

	tests := []struct {
		name      string
		charts    *reposerver.HelmChartsResponse
		chartName string
		expected  []string
	}{
		{
			name:      "chart is in the repository",
			charts:    charts,
			chartName: "nginx",
			expected:  []string{"1.20.0", "1.21.0"},
		},
		{
			name:      "chart is not in the repository",
			charts:    charts,
			chartName: "postgresql",
			expected:  nil,
		},
		{
			name:      "empty repository",
			charts:    &reposerver.HelmChartsResponse{},
			chartName: "nginx",
			expected:  nil,
		},
		{
			name:      "no response",
			charts:    nil,
			chartName: "nginx",
			expected:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, findChartVersions(tt.charts, tt.chartName))
		})
	}
}

// TestGetHelmChartVersions_PassesProject checks that the application project reaches
// ArgoCD. Without it ArgoCD cannot resolve the credentials of a project-scoped repository
// and falls back to an anonymous request.
func TestGetHelmChartVersions_PassesProject(t *testing.T) {
	lister := &stubHelmChartLister{
		response: &reposerver.HelmChartsResponse{
			Items: []*reposerver.HelmChart{
				{Name: "nginx", Versions: []string{"1.20.0", "1.21.0"}},
			},
		},
	}
	client := newTestClient(lister)

	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.20.0", "1.21.0"}, versions)

	require.NotNil(t, lister.lastQuery)
	assert.Equal(t, "https://charts.example.com", lister.lastQuery.Repo)
	assert.Equal(t, "team-a", lister.lastQuery.AppProject)
}

// TestGetHelmChartVersions_EmptyProject checks that a globally registered repository can
// still be queried without a project.
func TestGetHelmChartVersions_EmptyProject(t *testing.T) {
	lister := &stubHelmChartLister{response: &reposerver.HelmChartsResponse{}}
	client := newTestClient(lister)

	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "")
	require.NoError(t, err)
	assert.Nil(t, versions)

	require.NotNil(t, lister.lastQuery)
	assert.Empty(t, lister.lastQuery.AppProject)
}

// TestGetHelmChartVersions_Error checks that an ArgoCD failure is wrapped instead of being
// reported as an empty repository.
func TestGetHelmChartVersions_Error(t *testing.T) {
	listerErr := errors.New("permission denied")
	client := newTestClient(&stubHelmChartLister{err: listerErr})

	_, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, listerErr)
	assert.Contains(t, err.Error(), "https://charts.example.com")
}

func TestFilterOptions(t *testing.T) {
	// Test FilterOptions struct creation
	filter := FilterOptions{
		Projects: []string{"project1", "project2"},
		AppNames: []string{"app1", "app2"},
		Labels:   map[string]string{"env": "prod", "team": "platform"},
	}

	assert.Equal(t, 2, len(filter.Projects))
	assert.Equal(t, 2, len(filter.AppNames))
	assert.Equal(t, 2, len(filter.Labels))
	assert.Equal(t, "project1", filter.Projects[0])
	assert.Equal(t, "app1", filter.AppNames[0])
	assert.Equal(t, "prod", filter.Labels["env"])
}

// Note: Full integration tests for ListApplications would require a running ArgoCD instance
// or extensive mocking of the ArgoCD API client, which is complex due to the interface structure.
// The contains() function and basic client creation are tested above.
// For production, consider using integration tests with a real or containerized ArgoCD instance.
