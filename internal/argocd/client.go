package argocd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/argoproj/argo-cd/v2/pkg/apiclient"
	"github.com/argoproj/argo-cd/v2/pkg/apiclient/application"
	"github.com/argoproj/argo-cd/v2/pkg/apiclient/repository"
	"github.com/argoproj/argo-cd/v2/pkg/apiclient/session"
	"github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	reposerver "github.com/argoproj/argo-cd/v2/reposerver/apiclient"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// repositoryService holds the calls Argazer needs from the ArgoCD repository service:
// the charts of a Helm repository and the refs of a Git repository.
type repositoryService interface {
	GetHelmCharts(ctx context.Context, in *repository.RepoQuery, opts ...grpc.CallOption) (*reposerver.HelmChartsResponse, error)
	ListRefs(ctx context.Context, in *repository.RepoQuery, opts ...grpc.CallOption) (*reposerver.Refs, error)
}

// ociTagLister lists the tags of an OCI artifact through ArgoCD.
type ociTagLister interface {
	ListOCITags(ctx context.Context, artifact, project string) ([]string, error)
}

// Requests for a repository are retried before their error is handed to every caller
// waiting for them. Without retries a single hiccup of the repo-server skips a repository
// for the whole run, and a hiccup is likely exactly when Argazer starts its workers and
// asks for every repository at once.
const (
	repoRequestAttempts = 3
	// defaultRetryBackoff is the delay before the second attempt, doubled for each
	// further one.
	defaultRetryBackoff = 500 * time.Millisecond
)

// Client wraps ArgoCD API client
type Client struct {
	apiClient  apiclient.Client
	appClient  application.ApplicationServiceClient
	repoClient repositoryService
	ociClient  ociTagLister
	logger     *logrus.Entry

	// retryBackoff overrides defaultRetryBackoff, which is what a zero value means. It
	// exists so that tests do not have to wait for real backoffs.
	retryBackoff time.Duration

	// repoCache maps a repoCacheKey to the *repoCacheEntry holding the answer of ArgoCD,
	// and lives only for the duration of the run. Every request makes the ArgoCD
	// repo-server reach out to the repository, so hundreds of applications sharing a
	// repository must not turn into hundreds of requests.
	repoCache sync.Map
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

	// Create ArgoCD client options
	opts := apiclient.ClientOptions{
		ServerAddr: serverURL,
		PlainText:  strings.HasPrefix(serverURL, "http://"),
		Insecure:   insecure,
		GRPCWeb:    true, // Use gRPC-Web mode to avoid warnings and support HTTP proxies
	}

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

	// OCI tags are read over REST, see ociTagsClient. Every request bounds itself, so the
	// client has no timeout of its own, see ociTagsRequestTimeout.
	httpClient := &http.Client{}
	if insecure {
		// The default transport is cloned rather than replaced so that everything else it
		// does, proxies from the environment above all, keeps working.
		transport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("cannot skip TLS verification: the default HTTP transport is a %T", http.DefaultTransport)
		}

		insecureTransport := transport.Clone()
		insecureTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // asked for by --argocd-insecure
		httpClient.Transport = insecureTransport
	}

	logger.Info("Successfully created ArgoCD API client")

	return &Client{
		apiClient:  apiClient,
		appClient:  appClient,
		repoClient: repoClient,
		ociClient: &ociTagsClient{
			baseURL:    restBaseURL(serverURL),
			authToken:  authToken,
			httpClient: httpClient,
		},
		logger: logger,
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

	charts, err := cachedRepoRequest(ctx, c, helmChartsKey{repoURL: repoURL, project: project},
		func(ctx context.Context) (*reposerver.HelmChartsResponse, error) {
			return c.repoClient.GetHelmCharts(ctx, &repository.RepoQuery{
				Repo:       repoURL,
				AppProject: project,
			})
		})
	if err != nil {
		return nil, fmt.Errorf("failed to get Helm charts of repository %s: %w", repoURL, err)
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

// GetOCITags returns the tags ArgoCD reports for the OCI artifact holding a chart. The
// tags carry no chart metadata, so they are returned as they are and it is up to the
// caller to decide which of them are versions.
//
// project must be the ArgoCD project of the application that uses the chart, for the same
// reason as in GetHelmChartVersions.
func (c *Client) GetOCITags(ctx context.Context, repoURL, chartName, project string) ([]string, error) {
	artifact := ociArtifact(repoURL, chartName)

	c.logger.WithFields(logrus.Fields{
		"artifact": artifact,
		"project":  project,
	}).Debug("Fetching OCI tags from ArgoCD")

	tags, err := cachedRepoRequest(ctx, c, ociTagsKey{artifact: artifact, project: project},
		func(ctx context.Context) ([]string, error) {
			return c.ociClient.ListOCITags(ctx, artifact, project)
		})
	if err != nil {
		return nil, fmt.Errorf("failed to get the OCI tags of %s: %w", artifact, err)
	}

	c.logger.WithFields(logrus.Fields{
		"artifact":   artifact,
		"project":    project,
		"tags_count": len(tags),
	}).Debug("Received OCI tags from ArgoCD")

	// The cached tags are shared by every caller of the artifact, so callers get a copy
	// they are free to modify.
	return slices.Clone(tags), nil
}

// GetGitTags returns the tags ArgoCD reports for a Git repository. Charts kept in Git have
// no version list of their own, so their versions are whatever the tags say, and it is up
// to the caller to decide which of them are versions.
//
// project must be the ArgoCD project of the application that uses the repository, for the
// same reason as in GetHelmChartVersions.
func (c *Client) GetGitTags(ctx context.Context, repoURL, project string) ([]string, error) {
	c.logger.WithFields(logrus.Fields{
		"repo":    repoURL,
		"project": project,
	}).Debug("Fetching Git tags from ArgoCD")

	refs, err := cachedRepoRequest(ctx, c, gitRefsKey{repoURL: repoURL, project: project},
		func(ctx context.Context) (*reposerver.Refs, error) {
			return c.repoClient.ListRefs(ctx, &repository.RepoQuery{
				Repo:       repoURL,
				AppProject: project,
			})
		})
	if err != nil {
		return nil, fmt.Errorf("failed to get the refs of Git repository %s: %w", repoURL, err)
	}

	c.logger.WithFields(logrus.Fields{
		"repo":       repoURL,
		"project":    project,
		"tags_count": len(refs.GetTags()),
	}).Debug("Received Git tags from ArgoCD")

	// The cached response is shared by every caller of the repository, so callers get a
	// copy they are free to modify.
	return slices.Clone(refs.GetTags()), nil
}

// ociArtifact builds the reference of the OCI artifact holding a chart, which is what
// ArgoCD lists the tags of.
//
// The repository URL is passed on exactly as the Application spells it, because that same
// string is what ArgoCD looks its credentials up by, and it matches a registered
// repository only as a whole. Dropping the oci:// scheme, for one, would turn a repository
// registered as "oci://ghcr.io/myorg/nginx" into an unknown one, and ArgoCD would fall
// back to an anonymous request the registry rejects.
//
// A source with the scheme is an OCI source to ArgoCD: its URL already names the artifact
// and a chart name, should the Application carry one, is not part of the reference. A
// source without it is a Helm source, which names the registry path and the chart
// separately the way Helm does, so chart "nginx" of "ghcr.io/myorg/charts" lives in
// "ghcr.io/myorg/charts/nginx".
func ociArtifact(repoURL, chartName string) string {
	if strings.HasPrefix(repoURL, "oci://") {
		return strings.TrimSuffix(repoURL, "/")
	}

	artifact := strings.Trim(repoURL, "/")
	chartName = strings.Trim(chartName, "/")
	if chartName == "" {
		return artifact
	}

	return artifact + "/" + chartName
}

// Cache keys of the repository requests. Every kind of request has its own key type, which
// both keeps the entries of different requests apart within one cache and makes the type of
// a cached value follow from the type of its key.
type (
	// helmChartsKey identifies the charts of a Helm repository. The project is part of
	// every key because the same URL can resolve with different credentials, and
	// therefore to a different answer, depending on the project of the asking
	// application.
	helmChartsKey struct {
		repoURL string
		project string
	}

	// ociTagsKey identifies the tags of an OCI artifact.
	ociTagsKey struct {
		artifact string
		project  string
	}

	// gitRefsKey identifies the refs of a Git repository.
	gitRefsKey struct {
		repoURL string
		project string
	}
)

func (k helmChartsKey) String() string {
	return fmt.Sprintf("Helm charts of %s (project %q)", k.repoURL, k.project)
}

func (k ociTagsKey) String() string {
	return fmt.Sprintf("OCI tags of %s (project %q)", k.artifact, k.project)
}

func (k gitRefsKey) String() string {
	return fmt.Sprintf("Git refs of %s (project %q)", k.repoURL, k.project)
}

// repoCacheKey identifies a cached answer of ArgoCD, and names the request it belongs to
// for the logs.
type repoCacheKey interface {
	String() string
}

// cachedRepoRequest asks ArgoCD once per key and serves every later caller from the cache.
//
// Callers run in parallel worker goroutines, so a cache lookup alone is not enough: the
// first callers would all miss it at the same time and all hit ArgoCD. Callers that arrive
// while a request is in flight therefore wait for that single request instead of starting
// their own, which also means the retries of that request cover all of them.
func cachedRepoRequest[T any](ctx context.Context, c *Client, key repoCacheKey, request func(context.Context) (T, error)) (T, error) {
	entry := &repoCacheEntry[T]{ready: make(chan struct{})}
	if cached, loaded := c.repoCache.LoadOrStore(key, entry); loaded {
		c.logger.WithField("request", key.String()).Debug("Reusing the cached answer of ArgoCD")

		// Only requests of one kind use a given key type, so the entry of a key always
		// holds the type its request returns.
		waiting, ok := cached.(*repoCacheEntry[T])
		if !ok {
			var zero T
			return zero, fmt.Errorf("cached answer for %s holds %T instead of %T", key, cached, entry)
		}

		return waiting.wait(ctx)
	}

	// This goroutine stored the entry, so it owns the single request to ArgoCD.
	value, err := requestWithRetries(ctx, c, key, request)
	if err != nil {
		// A failure is not worth caching: it can be a cancelled context or a temporary
		// ArgoCD problem, and the next application must be free to retry.
		c.repoCache.Delete(key)
	}

	entry.value, entry.err = value, err
	close(entry.ready)

	return value, err
}

// requestWithRetries repeats a failed request to ArgoCD, giving it a growing pause to
// recover. Errors that a retry cannot change are reported right away.
func requestWithRetries[T any](ctx context.Context, c *Client, key repoCacheKey, request func(context.Context) (T, error)) (T, error) {
	for attempt := 1; ; attempt++ {
		value, err := request(ctx)
		if err == nil {
			return value, nil
		}

		if attempt == repoRequestAttempts || !worthRetrying(ctx, err) {
			var zero T
			return zero, err
		}

		delay := c.retryDelay(attempt)
		c.logger.WithError(err).WithFields(logrus.Fields{
			"request":  key.String(),
			"attempt":  attempt,
			"attempts": repoRequestAttempts,
			"retry_in": delay.String(),
		}).Warn("Request to ArgoCD failed, retrying")

		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			// The caller gave up, which is the outcome it should hear about rather than
			// the failure that was about to be retried.
			timer.Stop()

			var zero T
			return zero, ctx.Err()
		}
	}
}

// retryDelay is how long to wait after the given attempt, doubling with every attempt so
// that a busy repo-server gets more room each time.
func (c *Client) retryDelay(attempt int) time.Duration {
	backoff := c.retryBackoff
	if backoff <= 0 {
		backoff = defaultRetryBackoff
	}

	return backoff << (attempt - 1)
}

// worthRetrying reports whether repeating a failed request can plausibly succeed. A
// repository that does not exist, or a token that is not allowed to read it, answers the
// same way every time, while a timeout or a restarting repo-server does not.
//
// ctx is the context of the caller, and is what tells the two kinds of expired deadline
// apart. A request that ran into the deadline of its own attempt is worth repeating, while
// one that ended because the run itself is over is not, and both report the same error.
func worthRetrying(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}

	var responseErr *responseStatusError
	if errors.As(err, &responseErr) {
		return responseErr.worthRetrying()
	}

	if grpcStatus, ok := status.FromError(err); ok && slices.Contains(permanentCodes, grpcStatus.Code()) {
		return false
	}

	return true
}

// permanentCodes are the answers of ArgoCD that say the request itself is wrong, rather
// than that ArgoCD is momentarily unable to serve it.
var permanentCodes = []codes.Code{
	codes.NotFound,
	codes.PermissionDenied,
	codes.Unauthenticated,
	codes.InvalidArgument,
	codes.Unimplemented,
	codes.FailedPrecondition,
}

// repoCacheEntry holds the outcome of one request to ArgoCD. ready is closed once value
// and err are written, which is what makes them safe to read from other goroutines.
type repoCacheEntry[T any] struct {
	ready chan struct{}
	value T
	err   error
}

// wait blocks until the request owning the entry has finished, and reports the outcome it
// got. It gives up when the caller's own context is cancelled.
func (e *repoCacheEntry[T]) wait(ctx context.Context) (T, error) {
	select {
	case <-e.ready:
		return e.value, e.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
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
