package argocd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

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

// Client reads applications and repository versions from the ArgoCD REST API.
type Client struct {
	rest   *restClient
	logger *logrus.Entry

	// retryBackoff overrides defaultRetryBackoff, which is what a zero value means. It
	// exists so that tests do not have to wait for real backoffs.
	retryBackoff time.Duration

	// repoCache maps a repoCacheKey to the *repoCacheEntry holding the answer of ArgoCD,
	// and lives only for the duration of the run. Every request makes the ArgoCD
	// repo-server reach out to the repository, so hundreds of applications sharing a
	// repository must not turn into hundreds of requests.
	repoCache sync.Map
}

// NewClient creates a client for the ArgoCD API.
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

	if authToken == "" && (username == "" || password == "") {
		return nil, fmt.Errorf("an auth token, or a username and password, are required to authenticate with ArgoCD")
	}

	// Every request bounds itself, so the client has no timeout of its own, see
	// repoRequestTimeout.
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

	rest := &restClient{
		baseURL:    restBaseURL(serverURL),
		httpClient: httpClient,
	}

	// Without a token, exchange username/password for a session token.
	if authToken == "" {
		sessionToken, err := rest.login(context.Background(), username, password)
		if err != nil {
			return nil, err
		}
		authToken = sessionToken
	}
	rest.authToken = authToken

	logger.Info("Successfully created ArgoCD API client")

	return &Client{
		rest:   rest,
		logger: logger,
	}, nil
}

// FilterOptions defines filtering criteria for applications
type FilterOptions struct {
	Projects []string          // Projects to filter by, ["*"] for all
	AppNames []string          // App names to filter by, ["*"] for all
	Labels   map[string]string // Label selectors
}

// ListApplications lists ArgoCD applications with optional filtering
func (c *Client) ListApplications(ctx context.Context, filter FilterOptions) ([]*Application, error) {
	c.logger.WithFields(logrus.Fields{
		"projects":  filter.Projects,
		"app_names": filter.AppNames,
		"labels":    filter.Labels,
	}).Debug("Listing ArgoCD applications")

	apps, err := c.rest.listApplications(ctx, c.applicationQuery(filter))
	if err != nil {
		return nil, fmt.Errorf("failed to list applications: %w", err)
	}

	// Several app names cannot be asked for in one query, so they are filtered here.
	filterNames := len(filter.AppNames) > 1 && !contains(filter.AppNames, "*")

	filtered := make([]*Application, 0, len(apps))
	for i := range apps {
		app := &apps[i]
		if filterNames && !contains(filter.AppNames, app.Metadata.Name) {
			continue
		}

		filtered = append(filtered, app)
	}

	c.logger.WithField("count", len(filtered)).Info("Found applications")

	return filtered, nil
}

// applicationQuery turns the filters into the query parameters of the applications
// endpoint, so that ArgoCD leaves out what the run is not interested in.
func (c *Client) applicationQuery(filter FilterOptions) url.Values {
	query := url.Values{}

	if len(filter.Projects) > 0 && !contains(filter.Projects, "*") {
		query["projects"] = filter.Projects
		c.logger.WithField("projects", filter.Projects).Debug("Filtering by projects")
	}

	// ArgoCD matches a single name, while several of them are left to ListApplications.
	if len(filter.AppNames) == 1 && !contains(filter.AppNames, "*") {
		query.Set("name", filter.AppNames[0])
		c.logger.WithField("app_name", filter.AppNames[0]).Debug("Filtering by app name")
	}

	if len(filter.Labels) > 0 {
		// The labels are sorted so that the same filter always makes the same request.
		labelSelectors := make([]string, 0, len(filter.Labels))
		for _, key := range slices.Sorted(maps.Keys(filter.Labels)) {
			labelSelectors = append(labelSelectors, fmt.Sprintf("%s=%s", key, filter.Labels[key]))
		}

		selector := strings.Join(labelSelectors, ",")
		query.Set("selector", selector)
		c.logger.WithField("label_selector", selector).Debug("Filtering by labels")
	}

	return query
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
		func(ctx context.Context) ([]helmChart, error) {
			return c.rest.listHelmCharts(ctx, repoURL, project)
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
			return c.rest.listOCITags(ctx, artifact, project)
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

	found, err := cachedRepoRequest(ctx, c, gitRefsKey{repoURL: repoURL, project: project},
		func(ctx context.Context) (*refs, error) {
			return c.rest.listRefs(ctx, repoURL, project)
		})
	if err != nil {
		return nil, fmt.Errorf("failed to get the refs of Git repository %s: %w", repoURL, err)
	}

	c.logger.WithFields(logrus.Fields{
		"repo":       repoURL,
		"project":    project,
		"tags_count": len(found.Tags),
	}).Debug("Received Git tags from ArgoCD")

	// The cached response is shared by every caller of the repository, so callers get a
	// copy they are free to modify.
	return slices.Clone(found.Tags), nil
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

	// Anything else is a connection that did not come about, which the next attempt may
	// well have better luck with.
	return true
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

// findChartVersions picks the versions of a single chart out of the charts of a repository,
// and returns nil when none of them is the chart in question.
func findChartVersions(charts []helmChart, chartName string) []string {
	for _, chart := range charts {
		if chart.Name == chartName {
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
