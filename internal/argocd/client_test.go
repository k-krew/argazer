package argocd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/argoproj/argo-cd/v2/pkg/apiclient/repository"
	reposerver "github.com/argoproj/argo-cd/v2/reposerver/apiclient"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// stubRepositoryService records the queries ArgoCD receives and answers with canned
// responses.
type stubRepositoryService struct {
	charts *reposerver.HelmChartsResponse
	refs   *reposerver.Refs
	err    error

	// failCalls limits err to the given number of first calls, so a test can check that a
	// failing request is retried. Zero means every call fails.
	failCalls int

	// entered is signalled on entry of every call, and block holds a call until the
	// test closes it. Together they let a test keep callers in flight.
	entered chan struct{}
	block   chan struct{}

	mu        sync.Mutex
	calls     int
	lastQuery *repository.RepoQuery
	queries   []*repository.RepoQuery
}

func (s *stubRepositoryService) GetHelmCharts(_ context.Context, in *repository.RepoQuery, _ ...grpc.CallOption) (*reposerver.HelmChartsResponse, error) {
	if err := s.record(in); err != nil {
		return nil, err
	}

	return s.charts, nil
}

func (s *stubRepositoryService) ListRefs(_ context.Context, in *repository.RepoQuery, _ ...grpc.CallOption) (*reposerver.Refs, error) {
	if err := s.record(in); err != nil {
		return nil, err
	}

	return s.refs, nil
}

// record notes one call and reports the error it is supposed to answer with.
func (s *stubRepositoryService) record(in *repository.RepoQuery) error {
	s.mu.Lock()
	s.calls++
	call := s.calls
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

	if s.err != nil && (s.failCalls == 0 || call <= s.failCalls) {
		return s.err
	}

	return nil
}

func (s *stubRepositoryService) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.calls
}

func (s *stubRepositoryService) projectsAsked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	projects := make([]string, 0, len(s.queries))
	for _, query := range s.queries {
		projects = append(projects, query.AppProject)
	}

	return projects
}

// stubOCITagLister answers with canned OCI tags and records what was asked of ArgoCD.
type stubOCITagLister struct {
	tags []string
	err  error

	// failCalls limits err to the given number of first calls. Zero means every call fails.
	failCalls int

	mu        sync.Mutex
	calls     int
	artifacts []string
	projects  []string
}

func (s *stubOCITagLister) ListOCITags(_ context.Context, artifact, project string) ([]string, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.artifacts = append(s.artifacts, artifact)
	s.projects = append(s.projects, project)
	s.mu.Unlock()

	if s.err != nil && (s.failCalls == 0 || call <= s.failCalls) {
		return nil, s.err
	}

	return s.tags, nil
}

func (s *stubOCITagLister) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.calls
}

// newTestClient builds a client talking to a stubbed repository service. Retries happen
// without a real backoff, so that tests do not pay for them.
func newTestClient(repoClient repositoryService) *Client {
	return &Client{
		repoClient:   repoClient,
		retryBackoff: time.Millisecond,
		logger:       logrus.NewEntry(logrus.New()),
	}
}

// newTestOCIClient builds a client talking to a stubbed OCI tags endpoint.
func newTestOCIClient(ociClient ociTagLister) *Client {
	return &Client{
		ociClient:    ociClient,
		retryBackoff: time.Millisecond,
		logger:       logrus.NewEntry(logrus.New()),
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
	repoService := &stubRepositoryService{
		charts: &reposerver.HelmChartsResponse{
			Items: []*reposerver.HelmChart{
				{Name: "nginx", Versions: []string{"1.20.0", "1.21.0"}},
			},
		},
	}
	client := newTestClient(repoService)

	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.20.0", "1.21.0"}, versions)

	require.NotNil(t, repoService.lastQuery)
	assert.Equal(t, "https://charts.example.com", repoService.lastQuery.Repo)
	assert.Equal(t, "team-a", repoService.lastQuery.AppProject)
}

// TestGetHelmChartVersions_EmptyProject checks that a globally registered repository can
// still be queried without a project.
func TestGetHelmChartVersions_EmptyProject(t *testing.T) {
	repoService := &stubRepositoryService{charts: &reposerver.HelmChartsResponse{}}
	client := newTestClient(repoService)

	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "")
	require.NoError(t, err)
	assert.Nil(t, versions)

	require.NotNil(t, repoService.lastQuery)
	assert.Empty(t, repoService.lastQuery.AppProject)
}

// TestGetHelmChartVersions_Error checks that an ArgoCD failure is wrapped instead of being
// reported as an empty repository.
func TestGetHelmChartVersions_Error(t *testing.T) {
	serviceErr := errors.New("repo-server unavailable")
	client := newTestClient(&stubRepositoryService{err: serviceErr})

	_, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, serviceErr)
	assert.Contains(t, err.Error(), "https://charts.example.com")
}

// TestGetHelmChartVersions_AsksArgoCDOncePerRepository is the reason the cache exists:
// every GetHelmCharts call makes the ArgoCD repo-server parse the whole index.yaml of the
// repository, so applications sharing a repository must not each trigger a request.
func TestGetHelmChartVersions_AsksArgoCDOncePerRepository(t *testing.T) {
	repoService := &stubRepositoryService{
		charts: &reposerver.HelmChartsResponse{
			Items: []*reposerver.HelmChart{
				{Name: "nginx", Versions: []string{"1.20.0", "1.21.0"}},
				{Name: "redis", Versions: []string{"7.0.0"}},
			},
		},
	}
	client := newTestClient(repoService)

	for i := 0; i < 10; i++ {
		versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
		require.NoError(t, err)
		assert.Equal(t, []string{"1.20.0", "1.21.0"}, versions)
	}

	assert.Equal(t, 1, repoService.callCount())

	// A second chart of the same repository is already in the cached response.
	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "redis", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"7.0.0"}, versions)
	assert.Equal(t, 1, repoService.callCount())

	// So is the answer for a chart the repository does not hold.
	versions, err = client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "postgresql", "team-a")
	require.NoError(t, err)
	assert.Empty(t, versions)
	assert.Equal(t, 1, repoService.callCount())
}

// TestGetHelmChartVersions_CachesPerRepositoryAndProject checks that the cache key is
// specific enough: the same URL resolves with the credentials of the asking project, and
// a different repository is a different repository.
func TestGetHelmChartVersions_CachesPerRepositoryAndProject(t *testing.T) {
	repoService := &stubRepositoryService{
		charts: &reposerver.HelmChartsResponse{
			Items: []*reposerver.HelmChart{
				{Name: "nginx", Versions: []string{"1.21.0"}},
			},
		},
	}
	client := newTestClient(repoService)

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

	assert.Equal(t, 4, repoService.callCount())
	assert.Equal(t, []string{"team-a", "team-b", "", "team-a"}, repoService.projectsAsked())
}

// TestGetHelmChartVersions_RetriesFailedRequests checks that a request surviving on the
// second attempt is not reported as a failure: the repo-server can be busy or restarting
// exactly when the workers start.
func TestGetHelmChartVersions_RetriesFailedRequests(t *testing.T) {
	repoService := &stubRepositoryService{
		err:       errors.New("repo-server unavailable"),
		failCalls: repoRequestAttempts - 1,
		charts: &reposerver.HelmChartsResponse{
			Items: []*reposerver.HelmChart{
				{Name: "nginx", Versions: []string{"1.21.0"}},
			},
		},
	}
	client := newTestClient(repoService)

	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.21.0"}, versions)
	assert.Equal(t, repoRequestAttempts, repoService.callCount())
}

// TestGetHelmChartVersions_GivesUpAfterAllAttempts checks that retrying is bounded: a
// repository that stays unreachable must not hold up the run for long.
func TestGetHelmChartVersions_GivesUpAfterAllAttempts(t *testing.T) {
	repoService := &stubRepositoryService{err: errors.New("repo-server unavailable")}
	client := newTestClient(repoService)

	_, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.Error(t, err)
	assert.Equal(t, repoRequestAttempts, repoService.callCount())
}

// TestGetHelmChartVersions_DoesNotRetryRejections checks that answers a retry cannot change
// are reported right away, instead of being asked for again a few times.
func TestGetHelmChartVersions_DoesNotRetryRejections(t *testing.T) {
	for _, code := range []codes.Code{codes.NotFound, codes.PermissionDenied, codes.Unauthenticated} {
		t.Run(code.String(), func(t *testing.T) {
			repoService := &stubRepositoryService{err: status.Error(code, "no")}
			client := newTestClient(repoService)

			_, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
			require.Error(t, err)
			assert.Equal(t, 1, repoService.callCount())
		})
	}
}

// TestGetHelmChartVersions_DoesNotCacheFailures checks that a failed request leaves no
// trace in the cache: it can be a temporary ArgoCD problem, and the next application has
// to be free to retry.
func TestGetHelmChartVersions_DoesNotCacheFailures(t *testing.T) {
	repoService := &stubRepositoryService{
		err:       errors.New("repo-server unavailable"),
		failCalls: repoRequestAttempts,
		charts: &reposerver.HelmChartsResponse{
			Items: []*reposerver.HelmChart{
				{Name: "nginx", Versions: []string{"1.21.0"}},
			},
		},
	}
	client := newTestClient(repoService)

	// The first caller uses up every attempt and fails.
	_, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.Error(t, err)
	assert.Equal(t, repoRequestAttempts, repoService.callCount())

	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.21.0"}, versions)
	assert.Equal(t, repoRequestAttempts+1, repoService.callCount())
}

// TestGetHelmChartVersions_ConcurrentCallersShareOneRequest covers what the worker pool
// actually does: callers asking for the same repository at the same time would all miss
// an empty cache, so they have to wait for the one request already in flight.
func TestGetHelmChartVersions_ConcurrentCallersShareOneRequest(t *testing.T) {
	repoService := &stubRepositoryService{
		charts: &reposerver.HelmChartsResponse{
			Items: []*reposerver.HelmChart{
				{Name: "nginx", Versions: []string{"1.20.0", "1.21.0"}},
			},
		},
		entered: make(chan struct{}, 10),
		block:   make(chan struct{}),
	}
	client := newTestClient(repoService)

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
	<-repoService.entered
	time.Sleep(50 * time.Millisecond)
	close(repoService.block)

	done.Wait()
	assert.Equal(t, 1, repoService.callCount())
}

// TestGetHelmChartVersions_WaitingCallersGetTheRetriedAnswer is why retrying happens inside
// the cache: waiting callers get the outcome of the one shared request, so a request that
// is not retried would fail every application of the repository at once.
func TestGetHelmChartVersions_WaitingCallersGetTheRetriedAnswer(t *testing.T) {
	repoService := &stubRepositoryService{
		err:       errors.New("repo-server unavailable"),
		failCalls: 1,
		charts: &reposerver.HelmChartsResponse{
			Items: []*reposerver.HelmChart{
				{Name: "nginx", Versions: []string{"1.21.0"}},
			},
		},
		entered: make(chan struct{}, 1),
	}
	client := newTestClient(repoService)

	const callers = 5
	var done sync.WaitGroup
	done.Add(callers)

	// The first caller owns the request and is inside its failing first attempt while the
	// others queue up behind it.
	go func() {
		defer done.Done()

		versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
		assert.NoError(t, err)
		assert.Equal(t, []string{"1.21.0"}, versions)
	}()
	<-repoService.entered

	for i := 1; i < callers; i++ {
		go func() {
			defer done.Done()

			versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
			assert.NoError(t, err)
			assert.Equal(t, []string{"1.21.0"}, versions)
		}()
	}

	done.Wait()
	assert.Equal(t, 2, repoService.callCount())
}

// TestGetHelmChartVersions_WaitingCallerRespectsContext checks that a caller waiting for
// the request of another caller still gives up when its own context is cancelled.
func TestGetHelmChartVersions_WaitingCallerRespectsContext(t *testing.T) {
	repoService := &stubRepositoryService{
		charts:  &reposerver.HelmChartsResponse{},
		entered: make(chan struct{}, 1),
		block:   make(chan struct{}),
	}
	defer close(repoService.block)
	client := newTestClient(repoService)

	go func() {
		_, _ = client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	}()
	<-repoService.entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetHelmChartVersions(ctx, "https://charts.example.com", "nginx", "team-a")
	assert.ErrorIs(t, err, context.Canceled)
}

// TestGetHelmChartVersions_CallersCannotCorruptTheCache checks that callers get their own
// copy of the versions: the cached response is shared by all of them.
func TestGetHelmChartVersions_CallersCannotCorruptTheCache(t *testing.T) {
	repoService := &stubRepositoryService{
		charts: &reposerver.HelmChartsResponse{
			Items: []*reposerver.HelmChart{
				{Name: "nginx", Versions: []string{"1.20.0", "1.21.0"}},
			},
		},
	}
	client := newTestClient(repoService)

	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.NoError(t, err)
	versions[0] = "corrupted"

	versions, err = client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.20.0", "1.21.0"}, versions)
}

// TestGetGitTags_PassesRepositoryAndProject checks that the tags of a Git repository are
// read through ArgoCD, which is what makes private repositories work without Argazer
// holding any credentials.
func TestGetGitTags_PassesRepositoryAndProject(t *testing.T) {
	repoService := &stubRepositoryService{
		refs: &reposerver.Refs{
			Branches: []string{"main"},
			Tags:     []string{"v1.2.3", "v1.3.0"},
		},
	}
	client := newTestClient(repoService)

	tags, err := client.GetGitTags(context.Background(), "https://github.com/myorg/charts.git", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"v1.2.3", "v1.3.0"}, tags)

	require.NotNil(t, repoService.lastQuery)
	assert.Equal(t, "https://github.com/myorg/charts.git", repoService.lastQuery.Repo)
	assert.Equal(t, "team-a", repoService.lastQuery.AppProject)
}

// TestGetGitTags_EmptyRepository checks that a repository without tags is reported as such
// instead of failing.
func TestGetGitTags_EmptyRepository(t *testing.T) {
	client := newTestClient(&stubRepositoryService{refs: &reposerver.Refs{}})

	tags, err := client.GetGitTags(context.Background(), "https://github.com/myorg/charts.git", "team-a")
	require.NoError(t, err)
	assert.Empty(t, tags)
}

// TestGetGitTags_AsksArgoCDOncePerRepository checks that the tags of a repository are
// cached like the charts of a Helm repository: cloning a repository is the most expensive
// thing ArgoCD does on behalf of Argazer.
func TestGetGitTags_AsksArgoCDOncePerRepository(t *testing.T) {
	repoService := &stubRepositoryService{refs: &reposerver.Refs{Tags: []string{"v1.2.3"}}}
	client := newTestClient(repoService)

	for i := 0; i < 5; i++ {
		tags, err := client.GetGitTags(context.Background(), "https://github.com/myorg/charts.git", "team-a")
		require.NoError(t, err)
		assert.Equal(t, []string{"v1.2.3"}, tags)
	}

	assert.Equal(t, 1, repoService.callCount())

	// Another project may resolve to other credentials, so it is asked for separately.
	_, err := client.GetGitTags(context.Background(), "https://github.com/myorg/charts.git", "team-b")
	require.NoError(t, err)
	assert.Equal(t, 2, repoService.callCount())
}

// TestGetGitTags_RetriesFailedRequests checks that the tags of a Git repository are retried
// as well.
func TestGetGitTags_RetriesFailedRequests(t *testing.T) {
	repoService := &stubRepositoryService{
		err:       errors.New("repo-server unavailable"),
		failCalls: 1,
		refs:      &reposerver.Refs{Tags: []string{"v1.2.3"}},
	}
	client := newTestClient(repoService)

	tags, err := client.GetGitTags(context.Background(), "https://github.com/myorg/charts.git", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"v1.2.3"}, tags)
	assert.Equal(t, 2, repoService.callCount())
}

// TestGetGitTags_Error checks that an ArgoCD failure names the repository it happened for.
func TestGetGitTags_Error(t *testing.T) {
	serviceErr := errors.New("repository not accessible")
	client := newTestClient(&stubRepositoryService{err: serviceErr})

	_, err := client.GetGitTags(context.Background(), "https://github.com/myorg/charts.git", "team-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, serviceErr)
	assert.Contains(t, err.Error(), "https://github.com/myorg/charts.git")
}

// TestGetOCITags_AsksForTheArtifactOfTheChart checks that ArgoCD is asked for the artifact
// the chart lives in, within the project of the asking application.
func TestGetOCITags_AsksForTheArtifactOfTheChart(t *testing.T) {
	lister := &stubOCITagLister{tags: []string{"1.20.0", "1.21.0", "latest"}}
	client := newTestOCIClient(lister)

	tags, err := client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
	require.NoError(t, err)

	// The tags carry no chart metadata, so they are passed on as they are.
	assert.Equal(t, []string{"1.20.0", "1.21.0", "latest"}, tags)
	assert.Equal(t, []string{"ghcr.io/myorg/charts/nginx"}, lister.artifacts)
	assert.Equal(t, []string{"team-a"}, lister.projects)
}

// TestGetOCITags_AsksArgoCDOncePerArtifact checks that the tags of an artifact are cached,
// while the charts of one registry stay separate requests: ArgoCD lists the tags of a
// single artifact, not of a whole registry.
func TestGetOCITags_AsksArgoCDOncePerArtifact(t *testing.T) {
	lister := &stubOCITagLister{tags: []string{"1.21.0"}}
	client := newTestOCIClient(lister)

	for i := 0; i < 5; i++ {
		_, err := client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
		require.NoError(t, err)
	}
	assert.Equal(t, 1, lister.callCount())

	_, err := client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "redis", "team-a")
	require.NoError(t, err)
	assert.Equal(t, 2, lister.callCount())

	// The same artifact within another project may resolve to other credentials.
	_, err = client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-b")
	require.NoError(t, err)
	assert.Equal(t, 3, lister.callCount())
}

// TestGetOCITags_RetriesFailedRequests checks that OCI tags are retried as well.
func TestGetOCITags_RetriesFailedRequests(t *testing.T) {
	lister := &stubOCITagLister{
		err:       errors.New("registry unavailable"),
		failCalls: repoRequestAttempts - 1,
		tags:      []string{"1.21.0"},
	}
	client := newTestOCIClient(lister)

	tags, err := client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.21.0"}, tags)
	assert.Equal(t, repoRequestAttempts, lister.callCount())
}

// TestGetOCITags_Error checks that a failure names the artifact it happened for.
func TestGetOCITags_Error(t *testing.T) {
	listerErr := errors.New("registry unavailable")
	client := newTestOCIClient(&stubOCITagLister{err: listerErr})

	_, err := client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, listerErr)
	assert.Contains(t, err.Error(), "ghcr.io/myorg/charts/nginx")
}

// TestGetOCITags_CallersCannotCorruptTheCache checks that callers get their own copy of the
// cached tags.
func TestGetOCITags_CallersCannotCorruptTheCache(t *testing.T) {
	client := newTestOCIClient(&stubOCITagLister{tags: []string{"1.20.0", "1.21.0"}})

	tags, err := client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
	require.NoError(t, err)
	tags[0] = "corrupted"

	tags, err = client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.20.0", "1.21.0"}, tags)
}

func TestOCIArtifact(t *testing.T) {
	tests := []struct {
		name      string
		repoURL   string
		chartName string
		expected  string
	}{
		{
			name:      "registry path and chart",
			repoURL:   "ghcr.io/myorg/charts",
			chartName: "nginx",
			expected:  "ghcr.io/myorg/charts/nginx",
		},
		{
			// A repository is registered in ArgoCD with the scheme its Applications use,
			// and that whole string is what its credentials are found by.
			name:     "an oci url is the artifact and keeps its scheme",
			repoURL:  "oci://ghcr.io/myorg/nginx",
			expected: "oci://ghcr.io/myorg/nginx",
		},
		{
			name:      "a chart name is no part of an oci url",
			repoURL:   "oci://ghcr.io/myorg/nginx",
			chartName: "nginx",
			expected:  "oci://ghcr.io/myorg/nginx",
		},
		{
			name:      "surrounding slashes are dropped",
			repoURL:   "ghcr.io/myorg/charts/",
			chartName: "/nginx",
			expected:  "ghcr.io/myorg/charts/nginx",
		},
		{
			name:     "url is already the whole artifact",
			repoURL:  "ghcr.io/myorg/charts/nginx",
			expected: "ghcr.io/myorg/charts/nginx",
		},
		{
			name:      "registry without a path",
			repoURL:   "registry.example.com",
			chartName: "nginx",
			expected:  "registry.example.com/nginx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ociArtifact(tt.repoURL, tt.chartName))
		})
	}
}

func TestWorthRetrying(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "a plain failure can be temporary",
			err:      errors.New("connection reset by peer"),
			expected: true,
		},
		{
			name:     "an unavailable repo-server can come back",
			err:      status.Error(codes.Unavailable, "no healthy upstream"),
			expected: true,
		},
		{
			name:     "a deadline within ArgoCD can be met next time",
			err:      status.Error(codes.DeadlineExceeded, "context deadline exceeded"),
			expected: true,
		},
		{
			name:     "a missing repository stays missing",
			err:      status.Error(codes.NotFound, "repo not found"),
			expected: false,
		},
		{
			name:     "a rejected token stays rejected",
			err:      status.Error(codes.PermissionDenied, "no access"),
			expected: false,
		},
		{
			name:     "an endpoint ArgoCD does not have will not appear",
			err:      status.Error(codes.Unimplemented, "unknown method"),
			expected: false,
		},
		{
			// The HTTP client reports the deadline of an attempt the same way a cancelled
			// run is reported, so the error alone must not decide.
			name:     "an attempt that ran out of time gets another one",
			err:      context.DeadlineExceeded,
			expected: true,
		},
		{
			name:     "a server error is worth another attempt",
			err:      &responseStatusError{status: http.StatusBadGateway},
			expected: true,
		},
		{
			name:     "a rejected request is not",
			err:      &responseStatusError{status: http.StatusForbidden},
			expected: false,
		},
		{
			name:     "too many requests is worth waiting for",
			err:      &responseStatusError{status: http.StatusTooManyRequests},
			expected: true,
		},
		{
			name:     "a wrapped status keeps its meaning",
			err:      fmt.Errorf("listing tags: %w", &responseStatusError{status: http.StatusNotFound}),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, worthRetrying(context.Background(), tt.err))
		})
	}
}

// TestWorthRetrying_CallerGaveUp checks that nothing is retried once the run itself is
// over, whatever the request failed with.
func TestWorthRetrying_CallerGaveUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.False(t, worthRetrying(ctx, context.Canceled))
	assert.False(t, worthRetrying(ctx, errors.New("connection reset by peer")))
	assert.False(t, worthRetrying(ctx, &responseStatusError{status: http.StatusBadGateway}))
}

// TestRequestWithRetries_GivesUpWithTheReasonOfTheCaller checks that a run that ends while
// a retry is being waited for is reported as the run ending, not as the failure that was
// about to be retried, and that the wait itself is cut short.
func TestRequestWithRetries_GivesUpWithTheReasonOfTheCaller(t *testing.T) {
	client := newTestOCIClient(&stubOCITagLister{err: errors.New("registry unavailable")})
	client.retryBackoff = time.Minute

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := client.GetOCITags(ctx, "ghcr.io/myorg/charts", "nginx", "team-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestRetryDelay checks that the pause between attempts grows, and that a client built
// without an explicit backoff still has one.
func TestRetryDelay(t *testing.T) {
	client := &Client{retryBackoff: 10 * time.Millisecond}
	assert.Equal(t, 10*time.Millisecond, client.retryDelay(1))
	assert.Equal(t, 20*time.Millisecond, client.retryDelay(2))

	assert.Equal(t, defaultRetryBackoff, (&Client{}).retryDelay(1))
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
