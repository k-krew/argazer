package argocd

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/argoproj/argo-cd/v2/pkg/apiclient"
	"github.com/argoproj/argo-cd/v2/pkg/apiclient/application"
	"github.com/argoproj/argo-cd/v2/pkg/apiclient/repository"
	"github.com/argoproj/argo-cd/v2/pkg/apiclient/session"
	"github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	reposerver "github.com/argoproj/argo-cd/v2/reposerver/apiclient"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

// helmChartLister is the single call Argazer needs from the ArgoCD repository service.
type helmChartLister interface {
	GetHelmCharts(ctx context.Context, in *repository.RepoQuery, opts ...grpc.CallOption) (*reposerver.HelmChartsResponse, error)
}

// helmChartsCacheKey identifies a cached ArgoCD helmcharts response. The project is part
// of the key because the same URL can resolve with different credentials, and therefore
// to a different set of charts, depending on the project of the asking application.
type helmChartsCacheKey struct {
	repoURL string
	project string
}

// Client wraps ArgoCD API client
type Client struct {
	apiClient  apiclient.Client
	appClient  application.ApplicationServiceClient
	repoClient helmChartLister
	logger     *logrus.Entry

	// helmChartsCache maps helmChartsCacheKey to *helmChartsCacheEntry and lives only
	// for the duration of the run. Every GetHelmCharts call makes the ArgoCD
	// repo-server download and parse the whole index.yaml of a repository, so hundreds
	// of applications sharing a repository must not turn into hundreds of requests.
	helmChartsCache sync.Map
}

// NewClient creates a new ArgoCD API client.
// When authToken is not empty it is used as is, otherwise a session is created from username/password.
func NewClient(serverURL, username, password, authToken string, insecure bool, logger *logrus.Entry) (*Client, error) {
	authMethod := "password"
	if authToken != "" {
		authMethod = "token"
	}

	logger.WithFields(logrus.Fields{
		"server":      serverURL,
		"username":    username,
		"insecure":    insecure,
		"auth_method": authMethod,
	}).Info("Creating ArgoCD API client")

	// Create HTTP client with optional TLS skip verification
	var httpClient *http.Client
	if insecure {
		httpClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	}

	// Create ArgoCD client options
	opts := apiclient.ClientOptions{
		ServerAddr: serverURL,
		PlainText:  strings.HasPrefix(serverURL, "http://"),
		Insecure:   insecure,
		GRPCWeb:    true, // Use gRPC-Web mode to avoid warnings and support HTTP proxies
	}

	_ = httpClient // Will be used for direct HTTP calls if needed

	// Without a token, exchange username/password for a session token
	if authToken == "" {
		sessionToken, err := createSessionToken(&opts, username, password, logger)
		if err != nil {
			return nil, err
		}
		authToken = sessionToken
	}

	opts.AuthToken = authToken

	// Create authenticated client
	apiClient, err := apiclient.NewClient(&opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create authenticated client: %w", err)
	}

	// Create application service client
	_, appClient, err := apiClient.NewApplicationClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create application client: %w", err)
	}

	// Create repository service client, used to read chart versions through ArgoCD
	_, repoClient, err := apiClient.NewRepoClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create repository client: %w", err)
	}

	logger.Info("Successfully created ArgoCD API client")

	return &Client{
		apiClient:  apiClient,
		appClient:  appClient,
		repoClient: repoClient,
		logger:     logger,
	}, nil
}

// createSessionToken logs in with username/password and returns a session token
func createSessionToken(opts *apiclient.ClientOptions, username, password string, logger *logrus.Entry) (string, error) {
	apiClient, err := apiclient.NewClient(opts)
	if err != nil {
		return "", fmt.Errorf("failed to create ArgoCD API client: %w", err)
	}

	closer, sessionClient, err := apiClient.NewSessionClient()
	if err != nil {
		return "", fmt.Errorf("failed to create session client: %w", err)
	}
	defer func() {
		if err := closer.Close(); err != nil {
			logger.WithError(err).Warn("Failed to close session client")
		}
	}()

	sessionResp, err := sessionClient.Create(context.Background(), &session.SessionCreateRequest{
		Username: username,
		Password: password,
	})
	if err != nil {
		return "", fmt.Errorf("failed to authenticate with ArgoCD: %w", err)
	}

	return sessionResp.Token, nil
}

// FilterOptions defines filtering criteria for applications
type FilterOptions struct {
	Projects []string          // Projects to filter by, ["*"] for all
	AppNames []string          // App names to filter by, ["*"] for all
	Labels   map[string]string // Label selectors
}

// ListApplications lists ArgoCD applications with optional filtering
func (c *Client) ListApplications(ctx context.Context, filter FilterOptions) ([]*v1alpha1.Application, error) {
	c.logger.WithFields(logrus.Fields{
		"projects":  filter.Projects,
		"app_names": filter.AppNames,
		"labels":    filter.Labels,
	}).Debug("Listing ArgoCD applications")

	// Build query - use Projects field directly instead of selector
	query := &application.ApplicationQuery{}

	// Add project filter using the Projects field
	if len(filter.Projects) > 0 && !contains(filter.Projects, "*") {
		query.Projects = filter.Projects
		c.logger.WithField("projects", filter.Projects).Debug("Filtering by projects")
	}

	// Add app name filter using the AppNamePattern field for server-side filtering
	if len(filter.AppNames) > 0 && !contains(filter.AppNames, "*") {
		// If single app name, use AppNamePattern
		if len(filter.AppNames) == 1 {
			query.Name = &filter.AppNames[0]
			c.logger.WithField("app_name", filter.AppNames[0]).Debug("Filtering by app name")
		}
		// For multiple app names, we'll still need to filter client-side
		// as ArgoCD API doesn't support multiple app names in one query
	}

	// Build label selector if needed
	if len(filter.Labels) > 0 {
		var labelSelectors []string
		for key, value := range filter.Labels {
			labelSelectors = append(labelSelectors, fmt.Sprintf("%s=%s", key, value))
		}
		selectorStr := strings.Join(labelSelectors, ",")
		query.Selector = &selectorStr
		c.logger.WithField("label_selector", selectorStr).Debug("Filtering by labels")
	}

	// List applications
	appList, err := c.appClient.List(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list applications: %w", err)
	}

	var filtered []*v1alpha1.Application

	// Filter by app names if we have multiple (client-side filter)
	for _, app := range appList.Items {
		// Check app name filter (only needed if multiple app names specified)
		if len(filter.AppNames) > 1 && !contains(filter.AppNames, "*") {
			if !contains(filter.AppNames, app.Name) {
				continue
			}
		}

		filtered = append(filtered, &app)
	}

	c.logger.WithField("count", len(filtered)).Info("Found applications")

	return filtered, nil
}

// GetHelmChartVersions returns the versions ArgoCD knows about for a chart in a Helm
// repository. ArgoCD reaches the repository with the credentials it already stores, so
// Argazer never needs the repository password itself.
// An empty result means the repository is reachable but holds no such chart.
//
// project must be the ArgoCD project of the application that uses the chart. Credentials
// of a project-scoped repository are only resolved when the project is part of the query,
// otherwise ArgoCD falls back to an anonymous request and the repository rejects it.
func (c *Client) GetHelmChartVersions(ctx context.Context, repoURL, chartName, project string) ([]string, error) {
	c.logger.WithFields(logrus.Fields{
		"repo":    repoURL,
		"chart":   chartName,
		"project": project,
	}).Debug("Fetching Helm chart versions from ArgoCD")

	charts, err := c.helmChartsOfRepository(ctx, repoURL, project)
	if err != nil {
		return nil, err
	}

	// The response is shared by every caller of the repository, so callers get a copy
	// they are free to modify.
	versions := slices.Clone(findChartVersions(charts, chartName))

	c.logger.WithFields(logrus.Fields{
		"repo":           repoURL,
		"chart":          chartName,
		"project":        project,
		"versions_count": len(versions),
	}).Debug("Received Helm chart versions from ArgoCD")

	return versions, nil
}

// helmChartsOfRepository returns every chart ArgoCD reports for a repository, asking
// ArgoCD once per repository and project and serving later callers from the cache.
//
// Callers run in parallel worker goroutines, so a cache lookup alone is not enough: the
// first callers would all miss it at the same time and all hit ArgoCD. Callers that
// arrive while a repository is being fetched therefore wait for that single in-flight
// request instead of starting their own.
func (c *Client) helmChartsOfRepository(ctx context.Context, repoURL, project string) (*reposerver.HelmChartsResponse, error) {
	key := helmChartsCacheKey{repoURL: repoURL, project: project}

	entry := &helmChartsCacheEntry{ready: make(chan struct{})}
	if cached, loaded := c.helmChartsCache.LoadOrStore(key, entry); loaded {
		c.logger.WithFields(logrus.Fields{
			"repo":    repoURL,
			"project": project,
		}).Debug("Reusing the cached Helm charts of the repository")

		return cached.(*helmChartsCacheEntry).wait(ctx)
	}

	// This goroutine stored the entry, so it owns the single request to ArgoCD.
	charts, err := c.repoClient.GetHelmCharts(ctx, &repository.RepoQuery{
		Repo:       repoURL,
		AppProject: project,
	})
	if err != nil {
		// A failure is not worth caching: it can be a cancelled context or a
		// temporary ArgoCD problem, and the next application must be free to retry.
		c.helmChartsCache.Delete(key)
		err = fmt.Errorf("failed to get Helm charts of repository %s: %w", repoURL, err)
	}

	entry.charts, entry.err = charts, err
	close(entry.ready)

	return charts, err
}

// helmChartsCacheEntry holds the outcome of one GetHelmCharts call. ready is closed once
// charts and err are written, which is what makes them safe to read from other
// goroutines.
type helmChartsCacheEntry struct {
	ready  chan struct{}
	charts *reposerver.HelmChartsResponse
	err    error
}

// wait blocks until the request owning the entry has finished, and reports the outcome it
// got. It gives up when the caller's own context is cancelled.
func (e *helmChartsCacheEntry) wait(ctx context.Context) (*reposerver.HelmChartsResponse, error) {
	select {
	case <-e.ready:
		return e.charts, e.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// findChartVersions picks the versions of a single chart out of an ArgoCD helmcharts
// response, and returns nil when the response does not mention the chart.
func findChartVersions(charts *reposerver.HelmChartsResponse, chartName string) []string {
	if charts == nil {
		return nil
	}

	for _, chart := range charts.Items {
		if chart != nil && chart.Name == chartName {
			return chart.Versions
		}
	}

	return nil
}

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
