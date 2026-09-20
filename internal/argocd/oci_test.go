package argocd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListOCITags checks the request Argazer sends and the answer it reads back: the
// artifact reaches ArgoCD with the oci:// scheme its endpoint needs and as a single path
// segment even though it holds slashes, the project travels as a query parameter, and the
// token authenticates the call.
func TestListOCITags(t *testing.T) {
	var gotPath, gotDecodedPath, gotRawQuery, gotAuthorization string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotDecodedPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		gotAuthorization = r.Header.Get("Authorization")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"branches":[],"tags":["1.20.0","1.21.0"]}`))
	}))
	defer server.Close()

	client := newTestRestClient(server)

	tags, err := client.listOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.20.0", "1.21.0"}, tags)

	assert.Equal(t, "/api/v1/repositories/oci:%2F%2Fghcr.io%2Fmyorg%2Fcharts%2Fnginx/oci-tags", gotPath)
	assert.Equal(t, "appProject=team-a", gotRawQuery)
	assert.Equal(t, "Bearer a-token", gotAuthorization)

	// Escaping aside, ArgoCD reads one repository URL carrying the scheme and the path.
	assert.Equal(t, "/api/v1/repositories/oci://ghcr.io/myorg/charts/nginx/oci-tags", gotDecodedPath)
}

// TestListOCITags_ArtifactWithScheme checks that an artifact named with the oci:// scheme
// reaches ArgoCD with that one scheme: the endpoint needs it, and adding another one would
// leave the artifact unknown to ArgoCD.
func TestListOCITags_ArtifactWithScheme(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"tags":["1.21.0"]}`))
	}))
	defer server.Close()

	tags, err := newTestRestClient(server).listOCITags(context.Background(), "oci://ghcr.io/myorg/nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.21.0"}, tags)

	// The escaped artifact is a single path segment, and arrives as the artifact it was.
	assert.Equal(t, "/api/v1/repositories/oci://ghcr.io/myorg/nginx/oci-tags", gotPath)
}

// TestListOCITags_WithoutProject checks that a globally registered repository is asked for
// without a project.
func TestListOCITags_WithoutProject(t *testing.T) {
	var gotRawQuery string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"tags":["1.21.0"]}`))
	}))
	defer server.Close()

	tags, err := newTestRestClient(server).listOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.21.0"}, tags)
	assert.Empty(t, gotRawQuery)
}

// TestListOCITags_ArtifactWithoutTags checks that an artifact ArgoCD reports no tags for
// is not mistaken for a failure.
func TestListOCITags_ArtifactWithoutTags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	tags, err := newTestRestClient(server).listOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "team-a")
	require.NoError(t, err)
	assert.Empty(t, tags)
}

// TestListOCITags_NotFound checks that a 404 explains itself: the endpoint only exists in
// recent ArgoCD versions, which a bare "404" would leave the user guessing about.
func TestListOCITags_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not Found"}`))
	}))
	defer server.Close()

	_, err := newTestRestClient(server).listOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "team-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ArgoCD 3.1")

	// The status survives the added explanation, so retrying can still tell that asking
	// again is pointless.
	var statusErr *responseStatusError
	require.True(t, errors.As(err, &statusErr))
	assert.Equal(t, http.StatusNotFound, statusErr.status)
	assert.False(t, worthRetrying(context.Background(), err))
}

// TestListOCITags_ServerError checks that a failing ArgoCD is reported as something worth
// retrying.
func TestListOCITags_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream is down"))
	}))
	defer server.Close()

	_, err := newTestRestClient(server).listOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "team-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream is down")
	assert.True(t, worthRetrying(context.Background(), err))
}

// TestListOCITags_Unauthorized checks that a rejected token is not asked about again.
func TestListOCITags_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := newTestRestClient(server).listOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "team-a")
	require.Error(t, err)
	assert.False(t, worthRetrying(context.Background(), err))
}

// TestListOCITags_UnexpectedBody checks that an answer that is not the expected JSON is
// reported instead of being read as an artifact without tags. A proxy in front of ArgoCD
// answering with a login page is the usual reason.
func TestListOCITags_UnexpectedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>please log in</html>"))
	}))
	defer server.Close()

	_, err := newTestRestClient(server).listOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "team-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse")
}

// TestListOCITags_CancelledContext checks that a cancelled run does not wait for ArgoCD.
func TestListOCITags_CancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tags":["1.21.0"]}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newTestRestClient(server).listOCITags(ctx, "ghcr.io/myorg/charts/nginx", "team-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, worthRetrying(ctx, err))
}

// TestListOCITags_RequestTimeoutIsRetried checks that an ArgoCD that accepts the request
// and then says nothing is not mistaken for a run that was called off: the attempt runs
// into its own deadline, and another attempt is what that deserves.
func TestListOCITags_RequestTimeoutIsRetried(t *testing.T) {
	answer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-answer
		_, _ = w.Write([]byte(`{"tags":["1.21.0"]}`))
	}))
	defer server.Close()
	defer close(answer)

	client := newTestRestClient(server)
	client.requestTimeout = 20 * time.Millisecond

	ctx := context.Background()

	_, err := client.listOCITags(ctx, "ghcr.io/myorg/charts/nginx", "team-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// The run itself is still going, which is what makes the timeout worth another try.
	assert.True(t, worthRetrying(ctx, err))
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
			name:      "the scheme is recognised whatever its case",
			repoURL:   "OCI://ghcr.io/myorg/nginx",
			chartName: "nginx",
			expected:  "OCI://ghcr.io/myorg/nginx",
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

// TestOCIRepositoryURL checks the repository URL the oci-tags endpoint is asked with: the
// scheme is what the endpoint reads the registry from, and an artifact that already names it
// must not end up with a second one.
func TestOCIRepositoryURL(t *testing.T) {
	tests := []struct {
		name     string
		artifact string
		expected string
	}{
		{
			name:     "a bare registry path gets the scheme the endpoint needs",
			artifact: "ghcr.io/myorg/charts/nginx",
			expected: "oci://ghcr.io/myorg/charts/nginx",
		},
		{
			name:     "a registry without a path gets it as well",
			artifact: "registry.example.com/nginx",
			expected: "oci://registry.example.com/nginx",
		},
		{
			name:     "an artifact naming the scheme keeps the one it has",
			artifact: "oci://ghcr.io/myorg/nginx",
			expected: "oci://ghcr.io/myorg/nginx",
		},
		{
			name:     "the scheme is recognised whatever its case",
			artifact: "OCI://ghcr.io/myorg/nginx",
			expected: "OCI://ghcr.io/myorg/nginx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ociRepositoryURL(tt.artifact))
		})
	}
}
