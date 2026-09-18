package argocd

import (
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
	// ociTagsRequestTimeout bounds one attempt at the OCI tags endpoint. Listing the tags
	// of an artifact makes ArgoCD talk to the registry, which is slower than a local
	// answer but still nothing to wait minutes for.
	//
	// It is a deadline on the attempt rather than a timeout on the HTTP client so that
	// running out of it stays distinguishable from the caller giving up: an attempt that
	// timed out is worth repeating, a cancelled run is not.
	ociTagsRequestTimeout = 30 * time.Second

	// maxOCITagsResponse bounds how much of an answer is read. A tag list is small; the
	// limit is there in case something other than ArgoCD answers, e.g. a proxy serving a
	// login page.
	maxOCITagsResponse = 4 << 20 // 4 MiB
)

// ociTagsClient lists the tags of an OCI artifact through the REST API of ArgoCD.
//
// It does not go through the ArgoCD SDK because the SDK Argazer is pinned to (2.x) has no
// call for OCI tags: GET /api/v1/repositories/{repo}/oci-tags was added in ArgoCD 3.1. The
// request is made against the same server and with the same token as the gRPC calls.
type ociTagsClient struct {
	baseURL    string
	authToken  string
	httpClient *http.Client

	// requestTimeout overrides ociTagsRequestTimeout, which is what a zero value means.
	// It exists so that tests do not have to wait for a real one.
	requestTimeout time.Duration
}

// ListOCITags returns the tags ArgoCD reports for an OCI artifact, e.g.
// "ghcr.io/myorg/charts/nginx". ArgoCD reaches the registry with the credentials it stores
// for the repository, so Argazer never needs them itself.
//
// project is the ArgoCD project of the asking application, needed for repositories whose
// credentials are scoped to a project.
func (o *ociTagsClient) ListOCITags(ctx context.Context, artifact, project string) ([]string, error) {
	// The artifact is a path parameter holding slashes of its own, so it is escaped as a
	// single segment, the way the ArgoCD UI does it.
	endpoint := fmt.Sprintf("%s/api/v1/repositories/%s/oci-tags", o.baseURL, url.PathEscape(artifact))
	if project != "" {
		endpoint += "?appProject=" + url.QueryEscape(project)
	}

	// The whole attempt, including reading the answer, is bounded here rather than by the
	// HTTP client, see ociTagsRequestTimeout.
	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build the OCI tags request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if o.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+o.authToken)
	}

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach the ArgoCD API: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOCITagsResponse))
	if err != nil {
		return nil, fmt.Errorf("failed to read the answer of the ArgoCD API: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		statusErr := &responseStatusError{status: resp.StatusCode, body: strings.TrimSpace(string(body))}
		if resp.StatusCode == http.StatusNotFound {
			// The endpoint itself may be the thing that is missing, which is easy to run
			// into and hard to guess from a bare 404.
			return nil, fmt.Errorf("%w; OCI tags require ArgoCD 3.1 or newer and the repository to be registered in ArgoCD", statusErr)
		}

		return nil, statusErr
	}

	// Only the tags are of interest: ArgoCD answers OCI tags with the same message it uses
	// for Git refs, whose branches stay empty here.
	var refs struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &refs); err != nil {
		return nil, fmt.Errorf("failed to parse the OCI tags ArgoCD answered with: %w", err)
	}

	return refs.Tags, nil
}

// timeout is how long a single attempt may take.
func (o *ociTagsClient) timeout() time.Duration {
	if o.requestTimeout > 0 {
		return o.requestTimeout
	}

	return ociTagsRequestTimeout
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
