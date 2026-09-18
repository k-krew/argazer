package argocd

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/argoproj/argo-cd/v2/pkg/apiclient/repository"
	reposerver "github.com/argoproj/argo-cd/v2/reposerver/apiclient"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// stubHelmChartLister records the queries ArgoCD receives and answers with a canned
// response.
type stubHelmChartLister struct {
	response *reposerver.HelmChartsResponse
	err      error

	// failOnlyFirstCall limits err to the very first call, so a test can check that a
	// failure does not end up in the cache.
	failOnlyFirstCall bool

	// entered is signalled on entry of every call, and block holds a call until the
	// test closes it. Together they let a test keep callers in flight.
	entered chan struct{}
	block   chan struct{}

	mu        sync.Mutex
	calls     int
	lastQuery *repository.RepoQuery
	queries   []*repository.RepoQuery
}

func (s *stubHelmChartLister) GetHelmCharts(_ context.Context, in *repository.RepoQuery, _ ...grpc.CallOption) (*reposerver.HelmChartsResponse, error) {
	s.mu.Lock()
	s.calls++
	firstCall := s.calls == 1
	s.lastQuery = in
	s.queries = append(s.queries, in)
	s.mu.Unlock()

	// A call never waits for the test to notice it, so that an unexpected extra call
	// shows up as a failed assertion instead of a hanging test.
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	if s.block != nil {
		<-s.block
	}

	if s.err != nil && (!s.failOnlyFirstCall || firstCall) {
		return nil, s.err
	}

	return s.response, nil
}

func (s *stubHelmChartLister) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.calls
}

func (s *stubHelmChartLister) projectsAsked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	projects := make([]string, 0, len(s.queries))
	for _, query := range s.queries {
		projects = append(projects, query.AppProject)
	}

	return projects
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

// TestGetHelmChartVersions_AsksArgoCDOncePerRepository is the reason the cache exists:
// every GetHelmCharts call makes the ArgoCD repo-server parse the whole index.yaml of the
// repository, so applications sharing a repository must not each trigger a request.
func TestGetHelmChartVersions_AsksArgoCDOncePerRepository(t *testing.T) {
	lister := &stubHelmChartLister{
		response: &reposerver.HelmChartsResponse{
			Items: []*reposerver.HelmChart{
				{Name: "nginx", Versions: []string{"1.20.0", "1.21.0"}},
				{Name: "redis", Versions: []string{"7.0.0"}},
			},
		},
	}
	client := newTestClient(lister)

	for i := 0; i < 10; i++ {
		versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
		require.NoError(t, err)
		assert.Equal(t, []string{"1.20.0", "1.21.0"}, versions)
	}

	assert.Equal(t, 1, lister.callCount())

	// A second chart of the same repository is already in the cached response.
	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "redis", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"7.0.0"}, versions)
	assert.Equal(t, 1, lister.callCount())

	// So is the answer for a chart the repository does not hold.
	versions, err = client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "postgresql", "team-a")
	require.NoError(t, err)
	assert.Empty(t, versions)
	assert.Equal(t, 1, lister.callCount())
}

// TestGetHelmChartVersions_CachesPerRepositoryAndProject checks that the cache key is
// specific enough: the same URL resolves with the credentials of the asking project, and
// a different repository is a different repository.
func TestGetHelmChartVersions_CachesPerRepositoryAndProject(t *testing.T) {
	lister := &stubHelmChartLister{
		response: &reposerver.HelmChartsResponse{
			Items: []*reposerver.HelmChart{
				{Name: "nginx", Versions: []string{"1.21.0"}},
			},
		},
	}
	client := newTestClient(lister)

	for _, query := range []struct {
		repoURL string
		project string
	}{
		{repoURL: "https://charts.example.com", project: "team-a"},
		{repoURL: "https://charts.example.com", project: "team-b"},
		{repoURL: "https://charts.example.com", project: ""},
		{repoURL: "https://other.example.com", project: "team-a"},
	} {
		// Twice each, as only the first one may reach ArgoCD.
		for i := 0; i < 2; i++ {
			_, err := client.GetHelmChartVersions(context.Background(), query.repoURL, "nginx", query.project)
			require.NoError(t, err)
		}
	}

	assert.Equal(t, 4, lister.callCount())
	assert.Equal(t, []string{"team-a", "team-b", "", "team-a"}, lister.projectsAsked())
}

// TestGetHelmChartVersions_DoesNotCacheFailures checks that a failed request leaves no
// trace in the cache: it can be a temporary ArgoCD problem, and the next application has
// to be free to retry.
func TestGetHelmChartVersions_DoesNotCacheFailures(t *testing.T) {
	lister := &stubHelmChartLister{
		err:               errors.New("repo-server unavailable"),
		failOnlyFirstCall: true,
		response: &reposerver.HelmChartsResponse{
			Items: []*reposerver.HelmChart{
				{Name: "nginx", Versions: []string{"1.21.0"}},
			},
		},
	}
	client := newTestClient(lister)

	_, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.Error(t, err)

	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.21.0"}, versions)
	assert.Equal(t, 2, lister.callCount())
}

// TestGetHelmChartVersions_ConcurrentCallersShareOneRequest covers what the worker pool
// actually does: callers asking for the same repository at the same time would all miss
// an empty cache, so they have to wait for the one request already in flight.
func TestGetHelmChartVersions_ConcurrentCallersShareOneRequest(t *testing.T) {
	lister := &stubHelmChartLister{
		response: &reposerver.HelmChartsResponse{
			Items: []*reposerver.HelmChart{
				{Name: "nginx", Versions: []string{"1.20.0", "1.21.0"}},
			},
		},
		entered: make(chan struct{}, 10),
		block:   make(chan struct{}),
	}
	client := newTestClient(lister)

	const callers = 10
	var started, done sync.WaitGroup
	started.Add(callers)
	done.Add(callers)

	for i := 0; i < callers; i++ {
		go func() {
			defer done.Done()
			started.Done()

			versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
			assert.NoError(t, err)
			assert.Equal(t, []string{"1.20.0", "1.21.0"}, versions)
		}()
	}

	// Hold the first request inside ArgoCD until every caller is on its way, so that
	// any caller starting a request of its own has the time to be counted.
	started.Wait()
	<-lister.entered
	time.Sleep(50 * time.Millisecond)
	close(lister.block)

	done.Wait()
	assert.Equal(t, 1, lister.callCount())
}

// TestGetHelmChartVersions_WaitingCallerRespectsContext checks that a caller waiting for
// the request of another caller still gives up when its own context is cancelled.
func TestGetHelmChartVersions_WaitingCallerRespectsContext(t *testing.T) {
	lister := &stubHelmChartLister{
		response: &reposerver.HelmChartsResponse{},
		entered:  make(chan struct{}, 1),
		block:    make(chan struct{}),
	}
	defer close(lister.block)
	client := newTestClient(lister)

	go func() {
		_, _ = client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	}()
	<-lister.entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetHelmChartVersions(ctx, "https://charts.example.com", "nginx", "team-a")
	assert.ErrorIs(t, err, context.Canceled)
}

// TestGetHelmChartVersions_CallersCannotCorruptTheCache checks that callers get their own
// copy of the versions: the cached response is shared by all of them.
func TestGetHelmChartVersions_CallersCannotCorruptTheCache(t *testing.T) {
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
	versions[0] = "corrupted"

	versions, err = client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.20.0", "1.21.0"}, versions)
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
