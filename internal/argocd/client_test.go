package argocd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeArgoCD stands in for the REST API of ArgoCD. It records every request it receives and
// answers with what a test set it up with, failures included, which is what makes the cache
// and the retries observable.
type fakeArgoCD struct {
	// body is the JSON answering a call that is not failed. The same body serves every
	// endpoint, since a test only ever asks one of them.
	body string

	// failStatus is answered instead of body for the first failCalls calls. A failStatus
	// without failCalls fails every call.
	failStatus int
	failCalls  int

	// entered is signalled on entry of every call, and block holds a call until the test
	// closes it. Together they let a test keep callers in flight.
	entered chan struct{}
	block   chan struct{}

	mu      sync.Mutex
	calls   int
	paths   []string
	queries []url.Values
}

// client starts the fake ArgoCD and builds a client talking to it. Retries happen without a
// real backoff, so that tests do not pay for them.
func (f *fakeArgoCD) client(t *testing.T) *Client {
	t.Helper()

	server := httptest.NewServer(f)
	t.Cleanup(server.Close)

	return &Client{
		rest: &restClient{
			baseURL:    server.URL,
			authToken:  "a-token",
			httpClient: server.Client(),
		},
		retryBackoff: time.Millisecond,
		logger:       logrus.NewEntry(logrus.New()),
	}
}

func (f *fakeArgoCD) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.paths = append(f.paths, r.URL.EscapedPath())
	f.queries = append(f.queries, r.URL.Query())
	f.mu.Unlock()

	// A call never waits for the test to notice it, so that an unexpected extra call
	// shows up as a failed assertion instead of a hanging test.
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	if f.block != nil {
		<-f.block
	}

	if f.failStatus != 0 && (f.failCalls == 0 || call <= f.failCalls) {
		http.Error(w, "ArgoCD says no", f.failStatus)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(f.body))
}

func (f *fakeArgoCD) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

// lastPath is the escaped path of the last request, which is where the repository of a
// repository request travels.
func (f *fakeArgoCD) lastPath(t *testing.T) string {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotEmpty(t, f.paths, "ArgoCD was not asked anything")

	return f.paths[len(f.paths)-1]
}

func (f *fakeArgoCD) lastQuery(t *testing.T) url.Values {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotEmpty(t, f.queries, "ArgoCD was not asked anything")

	return f.queries[len(f.queries)-1]
}

// projectsAsked is the project of every request, in order.
func (f *fakeArgoCD) projectsAsked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	projects := make([]string, 0, len(f.queries))
	for _, query := range f.queries {
		projects = append(projects, query.Get("appProject"))
	}

	return projects
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

// TestNewClient_UnreachableServer checks that a server that cannot be logged in to is
// reported when the client is built, rather than when the first application is checked.
func TestNewClient_UnreachableServer(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	// A server that is started and stopped right away leaves an address nothing listens
	// on, which is a refused connection instead of a name to resolve.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()

	_, err := NewClient(address, "admin", "password", "", false, logger)
	assert.Error(t, err)
}

// TestNewClient_MissingCredentials checks that a run without any way to authenticate is
// turned down before a request is made.
func TestNewClient_MissingCredentials(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	for _, credentials := range []struct {
		name     string
		username string
		password string
	}{
		{name: "nothing at all"},
		{name: "username without password", username: "admin"},
		{name: "password without username", password: "secret"},
	} {
		t.Run(credentials.name, func(t *testing.T) {
			_, err := NewClient("http://localhost:8080", credentials.username, credentials.password, "", false, logger)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "auth token")
		})
	}
}

// TestNewClient_AuthToken checks that a token short-circuits the login round-trip:
// the client is built without ever reaching the (non-existent) server.
func TestNewClient_AuthToken(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	client, err := NewClient("http://invalid-argocd-server-that-does-not-exist.example.com", "", "", "some-token", false, logger)
	require.NoError(t, err)
	assert.NotNil(t, client)
}

// TestNewClient_LogsInWithPassword checks the password path end to end: a session token is
// asked for once, and it is what the calls that follow authenticate with.
func TestNewClient_LogsInWithPassword(t *testing.T) {
	var gotAuthorization string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/session" {
			_, _ = w.Write([]byte(`{"token":"session-token"}`))
			return
		}

		gotAuthorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"items":[{"name":"nginx","versions":["1.21.0"]}]}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "admin", "password", "", false, logrus.NewEntry(logrus.New()))
	require.NoError(t, err)

	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.21.0"}, versions)
	assert.Equal(t, "Bearer session-token", gotAuthorization)
}

func TestFindChartVersions(t *testing.T) {
	charts := []helmChart{
		{Name: "redis", Versions: []string{"7.0.0", "7.1.0"}},
		{Name: "nginx", Versions: []string{"1.20.0", "1.21.0"}},
	}

	tests := []struct {
		name      string
		charts    []helmChart
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
			charts:    []helmChart{},
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

// applicationsAnswer is what ArgoCD answers the applications endpoint with: a single-source
// and a multi-source application, both carrying more than Argazer reads.
const applicationsAnswer = `{
  "metadata": {"resourceVersion": "1234"},
  "items": [
    {
      "metadata": {"name": "nginx-app", "namespace": "argocd", "labels": {"env": "prod"}},
      "spec": {
        "project": "team-a",
        "destination": {"server": "https://kubernetes.default.svc", "namespace": "web"},
        "source": {
          "repoURL": "https://charts.example.com",
          "chart": "nginx",
          "targetRevision": "1.21.0"
        }
      },
      "status": {"sync": {"status": "Synced"}, "health": {"status": "Healthy"}}
    },
    {
      "metadata": {"name": "redis-app", "namespace": "argocd"},
      "spec": {
        "project": "team-b",
        "sources": [
          {
            "name": "values",
            "repoURL": "https://github.com/myorg/values.git",
            "targetRevision": "main",
            "path": "values"
          },
          {
            "name": "chart",
            "repoURL": "https://charts.example.com",
            "chart": "redis",
            "targetRevision": "7.0.0",
            "helm": {"valueFiles": ["$values/redis.yaml"]}
          }
        ]
      }
    }
  ]
}`

// TestListApplications_ReadsTheFieldsArgazerNeeds checks that the answer of ArgoCD is
// parsed into the DTOs: single-source and multi-source applications alike, and that the
// fields Argazer has no use for are ignored instead of getting in the way.
func TestListApplications_ReadsTheFieldsArgazerNeeds(t *testing.T) {
	argo := &fakeArgoCD{body: applicationsAnswer}
	client := argo.client(t)

	apps, err := client.ListApplications(context.Background(), FilterOptions{Projects: []string{"*"}, AppNames: []string{"*"}})
	require.NoError(t, err)
	require.Len(t, apps, 2)

	assert.Equal(t, "/api/v1/applications", argo.lastPath(t))

	nginx := apps[0]
	assert.Equal(t, "nginx-app", nginx.Metadata.Name)
	assert.Equal(t, "team-a", nginx.Spec.Project)
	assert.Empty(t, nginx.Spec.Sources)
	require.NotNil(t, nginx.Spec.Source)
	assert.Equal(t, "https://charts.example.com", nginx.Spec.Source.RepoURL)
	assert.Equal(t, "nginx", nginx.Spec.Source.Chart)
	assert.Equal(t, "1.21.0", nginx.Spec.Source.TargetRevision)

	redis := apps[1]
	assert.Equal(t, "redis-app", redis.Metadata.Name)
	assert.Equal(t, "team-b", redis.Spec.Project)
	assert.Nil(t, redis.Spec.Source)
	require.Len(t, redis.Spec.Sources, 2)

	values := redis.Spec.Sources[0]
	assert.Equal(t, "values", values.Name)
	assert.Equal(t, "values", values.Path)
	assert.Empty(t, values.Chart)
	// Only a source with Helm options of its own is a Helm source, which is what tells a
	// chart in a Git repository from plain manifests.
	assert.Nil(t, values.Helm)

	chart := redis.Spec.Sources[1]
	assert.Equal(t, "chart", chart.Name)
	assert.Equal(t, "redis", chart.Chart)
	assert.Equal(t, "7.0.0", chart.TargetRevision)
	assert.NotNil(t, chart.Helm)
}

// TestListApplications_AsksArgoCDToFilter checks that the filters travel to ArgoCD instead
// of being applied to a full list of every application of the installation.
func TestListApplications_AsksArgoCDToFilter(t *testing.T) {
	tests := []struct {
		name     string
		filter   FilterOptions
		expected url.Values
	}{
		{
			name:     "everything",
			filter:   FilterOptions{Projects: []string{"*"}, AppNames: []string{"*"}},
			expected: url.Values{},
		},
		{
			name:     "no filters at all",
			filter:   FilterOptions{},
			expected: url.Values{},
		},
		{
			name:     "projects",
			filter:   FilterOptions{Projects: []string{"team-a", "team-b"}, AppNames: []string{"*"}},
			expected: url.Values{"projects": []string{"team-a", "team-b"}},
		},
		{
			name:     "a single application name",
			filter:   FilterOptions{Projects: []string{"*"}, AppNames: []string{"nginx-app"}},
			expected: url.Values{"name": []string{"nginx-app"}},
		},
		{
			// ArgoCD matches one name, so several of them are filtered by Argazer itself.
			name:     "several application names",
			filter:   FilterOptions{Projects: []string{"*"}, AppNames: []string{"nginx-app", "redis-app"}},
			expected: url.Values{},
		},
		{
			// The labels are sorted, so that the same filter always makes the same request.
			name:     "labels",
			filter:   FilterOptions{Labels: map[string]string{"team": "platform", "env": "prod"}},
			expected: url.Values{"selector": []string{"env=prod,team=platform"}},
		},
		{
			name: "everything at once",
			filter: FilterOptions{
				Projects: []string{"team-a"},
				AppNames: []string{"nginx-app"},
				Labels:   map[string]string{"env": "prod"},
			},
			expected: url.Values{
				"projects": []string{"team-a"},
				"name":     []string{"nginx-app"},
				"selector": []string{"env=prod"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argo := &fakeArgoCD{body: `{"items":[]}`}
			client := argo.client(t)

			_, err := client.ListApplications(context.Background(), tt.filter)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, argo.lastQuery(t))
		})
	}
}

// TestListApplications_FiltersSeveralNamesItself checks the filter ArgoCD cannot do: the
// applications it answers with are narrowed down to the names that were asked for.
func TestListApplications_FiltersSeveralNamesItself(t *testing.T) {
	argo := &fakeArgoCD{body: applicationsAnswer}
	client := argo.client(t)

	apps, err := client.ListApplications(context.Background(), FilterOptions{
		AppNames: []string{"redis-app", "an-app-that-is-not-there"},
	})
	require.NoError(t, err)
	require.Len(t, apps, 1)
	assert.Equal(t, "redis-app", apps[0].Metadata.Name)
}

// TestListApplications_Error checks that a failing ArgoCD ends the run instead of being
// read as an installation without applications.
func TestListApplications_Error(t *testing.T) {
	argo := &fakeArgoCD{failStatus: http.StatusInternalServerError}
	client := argo.client(t)

	_, err := client.ListApplications(context.Background(), FilterOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list applications")
}

// TestGetHelmChartVersions_PassesProject checks that the repository reaches ArgoCD as a
// single path segment and the application project as a query parameter. Without the project
// ArgoCD cannot resolve the credentials of a project-scoped repository and falls back to an
// anonymous request.
func TestGetHelmChartVersions_PassesProject(t *testing.T) {
	argo := &fakeArgoCD{body: `{"items":[{"name":"nginx","versions":["1.20.0","1.21.0"]}]}`}
	client := argo.client(t)

	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.20.0", "1.21.0"}, versions)

	assert.Equal(t, "/api/v1/repositories/https:%2F%2Fcharts.example.com/helmcharts", argo.lastPath(t))
	assert.Equal(t, "team-a", argo.lastQuery(t).Get("appProject"))
}

// TestGetHelmChartVersions_EmptyProject checks that a globally registered repository can
// still be queried without a project.
func TestGetHelmChartVersions_EmptyProject(t *testing.T) {
	argo := &fakeArgoCD{body: `{"items":[]}`}
	client := argo.client(t)

	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "")
	require.NoError(t, err)
	assert.Nil(t, versions)

	assert.Empty(t, argo.lastQuery(t))
}

// TestGetHelmChartVersions_Error checks that an ArgoCD failure is wrapped instead of being
// reported as an empty repository.
func TestGetHelmChartVersions_Error(t *testing.T) {
	argo := &fakeArgoCD{failStatus: http.StatusServiceUnavailable}
	client := argo.client(t)

	_, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "https://charts.example.com")

	var statusErr *responseStatusError
	require.True(t, errors.As(err, &statusErr))
	assert.Equal(t, http.StatusServiceUnavailable, statusErr.status)
}

// TestGetHelmChartVersions_UnexpectedBody checks that an answer that is not the expected
// JSON is reported instead of being read as an empty repository. A proxy in front of ArgoCD
// answering with a login page is the usual reason.
func TestGetHelmChartVersions_UnexpectedBody(t *testing.T) {
	argo := &fakeArgoCD{body: "<html>please log in</html>"}
	client := argo.client(t)

	_, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse")
}

// TestGetHelmChartVersions_AsksArgoCDOncePerRepository is the reason the cache exists:
// every request makes the ArgoCD repo-server parse the whole index.yaml of the repository,
// so applications sharing a repository must not each trigger one.
func TestGetHelmChartVersions_AsksArgoCDOncePerRepository(t *testing.T) {
	argo := &fakeArgoCD{body: `{"items":[
		{"name":"nginx","versions":["1.20.0","1.21.0"]},
		{"name":"redis","versions":["7.0.0"]}
	]}`}
	client := argo.client(t)

	for i := 0; i < 10; i++ {
		versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
		require.NoError(t, err)
		assert.Equal(t, []string{"1.20.0", "1.21.0"}, versions)
	}

	assert.Equal(t, 1, argo.callCount())

	// A second chart of the same repository is already in the cached response.
	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "redis", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"7.0.0"}, versions)
	assert.Equal(t, 1, argo.callCount())

	// So is the answer for a chart the repository does not hold.
	versions, err = client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "postgresql", "team-a")
	require.NoError(t, err)
	assert.Empty(t, versions)
	assert.Equal(t, 1, argo.callCount())
}

// TestGetHelmChartVersions_CachesPerRepositoryAndProject checks that the cache key is
// specific enough: the same URL resolves with the credentials of the asking project, and
// a different repository is a different repository.
func TestGetHelmChartVersions_CachesPerRepositoryAndProject(t *testing.T) {
	argo := &fakeArgoCD{body: `{"items":[{"name":"nginx","versions":["1.21.0"]}]}`}
	client := argo.client(t)

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

	assert.Equal(t, 4, argo.callCount())
	assert.Equal(t, []string{"team-a", "team-b", "", "team-a"}, argo.projectsAsked())
}

// TestGetHelmChartVersions_RetriesFailedRequests checks that a request surviving on the
// second attempt is not reported as a failure: the repo-server can be busy or restarting
// exactly when the workers start.
func TestGetHelmChartVersions_RetriesFailedRequests(t *testing.T) {
	argo := &fakeArgoCD{
		failStatus: http.StatusBadGateway,
		failCalls:  repoRequestAttempts - 1,
		body:       `{"items":[{"name":"nginx","versions":["1.21.0"]}]}`,
	}
	client := argo.client(t)

	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.21.0"}, versions)
	assert.Equal(t, repoRequestAttempts, argo.callCount())
}

// TestGetHelmChartVersions_GivesUpAfterAllAttempts checks that retrying is bounded: a
// repository that stays unreachable must not hold up the run for long.
func TestGetHelmChartVersions_GivesUpAfterAllAttempts(t *testing.T) {
	argo := &fakeArgoCD{failStatus: http.StatusBadGateway}
	client := argo.client(t)

	_, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.Error(t, err)
	assert.Equal(t, repoRequestAttempts, argo.callCount())
}

// TestGetHelmChartVersions_DoesNotRetryRejections checks that answers a retry cannot change
// are reported right away, instead of being asked for again a few times.
func TestGetHelmChartVersions_DoesNotRetryRejections(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusUnauthorized, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			argo := &fakeArgoCD{failStatus: status}
			client := argo.client(t)

			_, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
			require.Error(t, err)
			assert.Equal(t, 1, argo.callCount())
		})
	}
}

// TestGetHelmChartVersions_DoesNotCacheFailures checks that a failed request leaves no
// trace in the cache: it can be a temporary ArgoCD problem, and the next application has
// to be free to retry.
func TestGetHelmChartVersions_DoesNotCacheFailures(t *testing.T) {
	argo := &fakeArgoCD{
		failStatus: http.StatusBadGateway,
		failCalls:  repoRequestAttempts,
		body:       `{"items":[{"name":"nginx","versions":["1.21.0"]}]}`,
	}
	client := argo.client(t)

	// The first caller uses up every attempt and fails.
	_, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.Error(t, err)
	assert.Equal(t, repoRequestAttempts, argo.callCount())

	versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.21.0"}, versions)
	assert.Equal(t, repoRequestAttempts+1, argo.callCount())
}

// TestGetHelmChartVersions_ConcurrentCallersShareOneRequest covers what the worker pool
// actually does: callers asking for the same repository at the same time would all miss
// an empty cache, so they have to wait for the one request already in flight.
func TestGetHelmChartVersions_ConcurrentCallersShareOneRequest(t *testing.T) {
	argo := &fakeArgoCD{
		body:    `{"items":[{"name":"nginx","versions":["1.20.0","1.21.0"]}]}`,
		entered: make(chan struct{}, 10),
		block:   make(chan struct{}),
	}
	client := argo.client(t)

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
	<-argo.entered
	time.Sleep(50 * time.Millisecond)
	close(argo.block)

	done.Wait()
	assert.Equal(t, 1, argo.callCount())
}

// TestGetHelmChartVersions_WaitingCallersGetTheRetriedAnswer is why retrying happens inside
// the cache: waiting callers get the outcome of the one shared request, so a request that
// is not retried would fail every application of the repository at once.
func TestGetHelmChartVersions_WaitingCallersGetTheRetriedAnswer(t *testing.T) {
	argo := &fakeArgoCD{
		failStatus: http.StatusBadGateway,
		failCalls:  1,
		body:       `{"items":[{"name":"nginx","versions":["1.21.0"]}]}`,
		entered:    make(chan struct{}, 1),
	}
	client := argo.client(t)

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
	<-argo.entered

	for i := 1; i < callers; i++ {
		go func() {
			defer done.Done()

			versions, err := client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
			assert.NoError(t, err)
			assert.Equal(t, []string{"1.21.0"}, versions)
		}()
	}

	done.Wait()
	assert.Equal(t, 2, argo.callCount())
}

// TestGetHelmChartVersions_WaitingCallerRespectsContext checks that a caller waiting for
// the request of another caller still gives up when its own context is cancelled.
func TestGetHelmChartVersions_WaitingCallerRespectsContext(t *testing.T) {
	argo := &fakeArgoCD{
		body:    `{"items":[]}`,
		entered: make(chan struct{}, 1),
		block:   make(chan struct{}),
	}
	defer close(argo.block)
	client := argo.client(t)

	go func() {
		_, _ = client.GetHelmChartVersions(context.Background(), "https://charts.example.com", "nginx", "team-a")
	}()
	<-argo.entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetHelmChartVersions(ctx, "https://charts.example.com", "nginx", "team-a")
	assert.ErrorIs(t, err, context.Canceled)
}

// TestGetHelmChartVersions_CallersCannotCorruptTheCache checks that callers get their own
// copy of the versions: the cached response is shared by all of them.
func TestGetHelmChartVersions_CallersCannotCorruptTheCache(t *testing.T) {
	argo := &fakeArgoCD{body: `{"items":[{"name":"nginx","versions":["1.20.0","1.21.0"]}]}`}
	client := argo.client(t)

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
	argo := &fakeArgoCD{body: `{"branches":["main"],"tags":["v1.2.3","v1.3.0"]}`}
	client := argo.client(t)

	tags, err := client.GetGitTags(context.Background(), "https://github.com/myorg/charts.git", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"v1.2.3", "v1.3.0"}, tags)

	assert.Equal(t, "/api/v1/repositories/https:%2F%2Fgithub.com%2Fmyorg%2Fcharts.git/refs", argo.lastPath(t))
	assert.Equal(t, "team-a", argo.lastQuery(t).Get("appProject"))
}

// TestGetGitTags_EmptyRepository checks that a repository without tags is reported as such
// instead of failing.
func TestGetGitTags_EmptyRepository(t *testing.T) {
	client := (&fakeArgoCD{body: `{}`}).client(t)

	tags, err := client.GetGitTags(context.Background(), "https://github.com/myorg/charts.git", "team-a")
	require.NoError(t, err)
	assert.Empty(t, tags)
}

// TestGetGitTags_AsksArgoCDOncePerRepository checks that the tags of a repository are
// cached like the charts of a Helm repository: cloning a repository is the most expensive
// thing ArgoCD does on behalf of Argazer.
func TestGetGitTags_AsksArgoCDOncePerRepository(t *testing.T) {
	argo := &fakeArgoCD{body: `{"tags":["v1.2.3"]}`}
	client := argo.client(t)

	for i := 0; i < 5; i++ {
		tags, err := client.GetGitTags(context.Background(), "https://github.com/myorg/charts.git", "team-a")
		require.NoError(t, err)
		assert.Equal(t, []string{"v1.2.3"}, tags)
	}

	assert.Equal(t, 1, argo.callCount())

	// Another project may resolve to other credentials, so it is asked for separately.
	_, err := client.GetGitTags(context.Background(), "https://github.com/myorg/charts.git", "team-b")
	require.NoError(t, err)
	assert.Equal(t, 2, argo.callCount())
}

// TestGetGitTags_RetriesFailedRequests checks that the tags of a Git repository are retried
// as well.
func TestGetGitTags_RetriesFailedRequests(t *testing.T) {
	argo := &fakeArgoCD{
		failStatus: http.StatusBadGateway,
		failCalls:  1,
		body:       `{"tags":["v1.2.3"]}`,
	}
	client := argo.client(t)

	tags, err := client.GetGitTags(context.Background(), "https://github.com/myorg/charts.git", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"v1.2.3"}, tags)
	assert.Equal(t, 2, argo.callCount())
}

// TestGetGitTags_Error checks that an ArgoCD failure names the repository it happened for.
func TestGetGitTags_Error(t *testing.T) {
	client := (&fakeArgoCD{failStatus: http.StatusNotFound}).client(t)

	_, err := client.GetGitTags(context.Background(), "https://github.com/myorg/charts.git", "team-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "https://github.com/myorg/charts.git")
}

// TestGetOCITags_AsksForTheArtifactOfTheChart checks that ArgoCD is asked for the artifact
// the chart lives in, within the project of the asking application.
func TestGetOCITags_AsksForTheArtifactOfTheChart(t *testing.T) {
	argo := &fakeArgoCD{body: `{"tags":["1.20.0","1.21.0","latest"]}`}
	client := argo.client(t)

	tags, err := client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
	require.NoError(t, err)

	// The tags carry no chart metadata, so they are passed on as they are.
	assert.Equal(t, []string{"1.20.0", "1.21.0", "latest"}, tags)
	assert.Equal(t, "/api/v1/repositories/oci:%2F%2Fghcr.io%2Fmyorg%2Fcharts%2Fnginx/oci-tags", argo.lastPath(t))
	assert.Equal(t, "team-a", argo.lastQuery(t).Get("appProject"))
}

// TestGetOCITags_AsksForAChartNamedByItsURL checks the other way an Application may name an
// OCI chart: a repository URL that is the artifact already, without a chart of its own.
func TestGetOCITags_AsksForAChartNamedByItsURL(t *testing.T) {
	argo := &fakeArgoCD{body: `{"tags":["1.21.0"]}`}
	client := argo.client(t)

	tags, err := client.GetOCITags(context.Background(), "oci://ghcr.io/myorg/charts/nginx", "", "team-a")
	require.NoError(t, err)

	assert.Equal(t, []string{"1.21.0"}, tags)
	assert.Equal(t, "/api/v1/repositories/oci:%2F%2Fghcr.io%2Fmyorg%2Fcharts%2Fnginx/oci-tags", argo.lastPath(t))
}

// TestGetOCITags_AsksArgoCDOncePerArtifact checks that the tags of an artifact are cached,
// while the charts of one registry stay separate requests: ArgoCD lists the tags of a
// single artifact, not of a whole registry.
func TestGetOCITags_AsksArgoCDOncePerArtifact(t *testing.T) {
	argo := &fakeArgoCD{body: `{"tags":["1.21.0"]}`}
	client := argo.client(t)

	for i := 0; i < 5; i++ {
		_, err := client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
		require.NoError(t, err)
	}
	assert.Equal(t, 1, argo.callCount())

	_, err := client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "redis", "team-a")
	require.NoError(t, err)
	assert.Equal(t, 2, argo.callCount())

	// The same artifact within another project may resolve to other credentials.
	_, err = client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-b")
	require.NoError(t, err)
	assert.Equal(t, 3, argo.callCount())
}

// TestGetOCITags_RetriesFailedRequests checks that OCI tags are retried as well.
func TestGetOCITags_RetriesFailedRequests(t *testing.T) {
	argo := &fakeArgoCD{
		failStatus: http.StatusServiceUnavailable,
		failCalls:  repoRequestAttempts - 1,
		body:       `{"tags":["1.21.0"]}`,
	}
	client := argo.client(t)

	tags, err := client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.21.0"}, tags)
	assert.Equal(t, repoRequestAttempts, argo.callCount())
}

// TestGetOCITags_Error checks that a failure names the artifact it happened for.
func TestGetOCITags_Error(t *testing.T) {
	client := (&fakeArgoCD{failStatus: http.StatusServiceUnavailable}).client(t)

	_, err := client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghcr.io/myorg/charts/nginx")
}

// TestGetOCITags_CallersCannotCorruptTheCache checks that callers get their own copy of the
// cached tags.
func TestGetOCITags_CallersCannotCorruptTheCache(t *testing.T) {
	client := (&fakeArgoCD{body: `{"tags":["1.20.0","1.21.0"]}`}).client(t)

	tags, err := client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
	require.NoError(t, err)
	tags[0] = "corrupted"

	tags, err = client.GetOCITags(context.Background(), "ghcr.io/myorg/charts", "nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.20.0", "1.21.0"}, tags)
}

func TestWorthRetrying(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "a connection that did not come about can come about",
			err:      errors.New("connection reset by peer"),
			expected: true,
		},
		{
			// The HTTP client reports the deadline of an attempt the same way a cancelled
			// run is reported, so the error alone must not decide.
			name:     "an attempt that ran out of time gets another one",
			err:      context.DeadlineExceeded,
			expected: true,
		},
		{
			name:     "an unavailable repo-server can come back",
			err:      &responseStatusError{status: http.StatusServiceUnavailable},
			expected: true,
		},
		{
			name:     "a server error is worth another attempt",
			err:      &responseStatusError{status: http.StatusBadGateway},
			expected: true,
		},
		{
			name:     "too many requests is worth waiting for",
			err:      &responseStatusError{status: http.StatusTooManyRequests},
			expected: true,
		},
		{
			name:     "a deadline within ArgoCD can be met next time",
			err:      &responseStatusError{status: http.StatusRequestTimeout},
			expected: true,
		},
		{
			name:     "a missing repository stays missing",
			err:      &responseStatusError{status: http.StatusNotFound},
			expected: false,
		},
		{
			name:     "a rejected token stays rejected",
			err:      &responseStatusError{status: http.StatusUnauthorized},
			expected: false,
		},
		{
			name:     "a request that is not allowed stays disallowed",
			err:      &responseStatusError{status: http.StatusForbidden},
			expected: false,
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
	argo := &fakeArgoCD{failStatus: http.StatusBadGateway}
	client := argo.client(t)
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
