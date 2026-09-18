package argocd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// repoRequestTimeout bounds one attempt at a repository endpoint. Listing the charts,
	// refs or tags of a repository makes ArgoCD talk to that repository, which is slower
	// than an answer it has at hand but still nothing to wait minutes for.
	//
	// It is a deadline on the attempt rather than a timeout on the HTTP client so that
	// running out of it stays distinguishable from the caller giving up: an attempt that
	// timed out is worth repeating, a cancelled run is not.
	repoRequestTimeout = 30 * time.Second

	// listApplicationsTimeout bounds the request for the applications. ArgoCD answers it
	// from its own cluster cache, but the answer carries every Application in full, so a
	// large installation needs more room than a single repository does.
	listApplicationsTimeout = 60 * time.Second

	// loginTimeout bounds the request exchanging username and password for a token.
	loginTimeout = 30 * time.Second

	// maxRepositoryResponse bounds how much of a repository answer is read. A chart or tag
	// list is small; the limit is there in case something other than ArgoCD answers, e.g.
	// a proxy serving a login page.
	maxRepositoryResponse = 4 << 20 // 4 MiB

	// maxApplicationsResponse bounds how much of the applications answer is read. Every
	// Application comes with its whole status, so thousands of them add up to far more
	// than a repository answer ever does.
	maxApplicationsResponse = 512 << 20 // 512 MiB

	// maxErrorResponse bounds how much of a failed answer is read for its message.
	maxErrorResponse = 8 << 10 // 8 KiB
)

// restClient talks to the REST API of ArgoCD over plain HTTP.
//
// Everything Argazer asks of ArgoCD is available over REST, which is why the ArgoCD SDK is
// not used: the SDK speaks gRPC and brings gRPC, client-go and Helm along, and those are
// what used to make up the bulk of the binary and most of the CVEs reported against it.
type restClient struct {
	// baseURL is the address of ArgoCD including its scheme, without a trailing slash.
	baseURL string

	// authToken is sent as a bearer token and is what every call authenticates with.
	authToken string

	httpClient *http.Client

	// requestTimeout overrides the deadline an endpoint asks for. It exists so that tests
	// do not have to wait for a real one.
	requestTimeout time.Duration
}

// apiEndpoint is one GET endpoint of the ArgoCD API together with how its answer is to be
// treated.
type apiEndpoint struct {
	// path is the path of the endpoint, with its path parameters already escaped.
	path  string
	query url.Values

	// timeout bounds one attempt, maxResponse how much of the answer is read.
	timeout     time.Duration
	maxResponse int64
}

// getJSON asks an endpoint of the ArgoCD API for a resource and parses the answer into out.
func (r *restClient) getJSON(ctx context.Context, endpoint apiEndpoint, out any) error {
	address := r.baseURL + endpoint.path
	if len(endpoint.query) > 0 {
		address += "?" + endpoint.query.Encode()
	}

	// The whole attempt, including reading the answer, is bounded here rather than by the
	// HTTP client, see repoRequestTimeout.
	ctx, cancel := context.WithTimeout(ctx, r.timeout(endpoint.timeout))
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return fmt.Errorf("failed to build the request for %s: %w", endpoint.path, err)
	}

	return r.send(req, endpoint.maxResponse, out)
}

// login exchanges a username and password for a session token, the way the ArgoCD CLI
// does. Every later call authenticates with that token.
func (r *restClient) login(ctx context.Context, username, password string) (string, error) {
	body, err := json.Marshal(sessionRequest{Username: username, Password: password})
	if err != nil {
		return "", fmt.Errorf("failed to build the login request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout(loginTimeout))
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/api/v1/session", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to build the login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	var session sessionResponse
	if err := r.send(req, maxRepositoryResponse, &session); err != nil {
		return "", fmt.Errorf("failed to authenticate with ArgoCD: %w", err)
	}

	if session.Token == "" {
		return "", fmt.Errorf("failed to authenticate with ArgoCD: the answer carries no token")
	}

	return session.Token, nil
}

// listApplications returns the Applications ArgoCD reports for a query. The query is what
// keeps the filtering on the ArgoCD side, so that a run watching one project does not read
// every Application of the installation.
func (r *restClient) listApplications(ctx context.Context, query url.Values) ([]Application, error) {
	var list applicationList
	err := r.getJSON(ctx, apiEndpoint{
		path:        "/api/v1/applications",
		query:       query,
		timeout:     listApplicationsTimeout,
		maxResponse: maxApplicationsResponse,
	}, &list)
	if err != nil {
		return nil, err
	}

	return list.Items, nil
}

// listHelmCharts returns the charts ArgoCD finds in a Helm repository, with their versions.
func (r *restClient) listHelmCharts(ctx context.Context, repoURL, project string) ([]helmChart, error) {
	var charts helmChartsResponse
	if err := r.getJSON(ctx, r.repositoryEndpoint(repoURL, "helmcharts", project), &charts); err != nil {
		return nil, err
	}

	return charts.Items, nil
}

// listRefs returns the branches and tags ArgoCD finds in a Git repository.
func (r *restClient) listRefs(ctx context.Context, repoURL, project string) (*refs, error) {
	var found refs
	if err := r.getJSON(ctx, r.repositoryEndpoint(repoURL, "refs", project), &found); err != nil {
		return nil, err
	}

	return &found, nil
}

// repositoryEndpoint builds one of the repository endpoints for a repository.
//
// The repository is a path parameter holding slashes of its own, so it is escaped as a
// single segment, the way the ArgoCD UI does it. project is the ArgoCD project of the
// asking application, and is left out for a repository registered globally.
func (r *restClient) repositoryEndpoint(repoURL, action, project string) apiEndpoint {
	endpoint := apiEndpoint{
		path:        "/api/v1/repositories/" + url.PathEscape(repoURL) + "/" + action,
		timeout:     repoRequestTimeout,
		maxResponse: maxRepositoryResponse,
	}

	if project != "" {
		endpoint.query = url.Values{"appProject": []string{project}}
	}

	return endpoint
}

// send executes a prepared request, authenticating it on the way, and parses a successful
// answer into out.
func (r *restClient) send(req *http.Request, maxResponse int64, out any) error {
	req.Header.Set("Accept", "application/json")
	if r.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+r.authToken)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to reach the ArgoCD API: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}

	if maxResponse <= 0 {
		maxResponse = maxRepositoryResponse
	}

	// The answer is parsed as it arrives rather than read into memory first, since the
	// applications of a large installation make for a lot of JSON.
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(out); err != nil {
		return fmt.Errorf("failed to parse the answer of the ArgoCD API: %w", err)
	}

	return nil
}

// timeout is how long a single attempt may take: the deadline the endpoint asks for, unless
// the client overrides it.
func (r *restClient) timeout(endpointTimeout time.Duration) time.Duration {
	switch {
	case r.requestTimeout > 0:
		return r.requestTimeout
	case endpointTimeout > 0:
		return endpointTimeout
	default:
		return repoRequestTimeout
	}
}

// statusError turns an answer that is not a success into an error carrying its status and
// as much of its body as is worth quoting.
func statusError(resp *http.Response) error {
	// A body that cannot be read costs nothing here: the status alone already says that
	// the request failed.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorResponse))

	return &responseStatusError{status: resp.StatusCode, body: strings.TrimSpace(string(body))}
}

// responseStatusError is an answer of the ArgoCD REST API that is not a success.
type responseStatusError struct {
	status int
	body   string
}

func (e *responseStatusError) Error() string {
	return fmt.Sprintf("ArgoCD answered with status %d: %s", e.status, e.body)
}

// worthRetrying reports whether the same request can plausibly get a different answer.
// ArgoCD rejecting the request stays a rejection, while an overloaded or restarting server
// does not.
func (e *responseStatusError) worthRetrying() bool {
	switch e.status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	default:
		return e.status >= http.StatusInternalServerError
	}
}

// restBaseURL turns the configured ArgoCD address into the base URL of its REST API. The
// address is usually a bare host, the way the ArgoCD CLI takes it, and HTTPS is what
// ArgoCD serves unless the address says otherwise.
func restBaseURL(serverURL string) string {
	address := strings.TrimSuffix(strings.TrimSpace(serverURL), "/")
	if strings.HasPrefix(address, "http://") || strings.HasPrefix(address, "https://") {
		return address
	}

	return "https://" + address
}
