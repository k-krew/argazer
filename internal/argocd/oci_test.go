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

// newTestOCITagsClient points an ociTagsClient at a test server.
func newTestOCITagsClient(server *httptest.Server, authToken string) *ociTagsClient {
	return &ociTagsClient{
		baseURL:    server.URL,
		authToken:  authToken,
		httpClient: server.Client(),
	}
}

// TestOCITagsClient_ListOCITags checks the request Argazer sends and the answer it reads
// back: the artifact is a single path segment even though it holds slashes, the project
// travels as a query parameter, and the token authenticates the call.
func TestOCITagsClient_ListOCITags(t *testing.T) {
	var gotPath, gotRawQuery, gotAuthorization string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotRawQuery = r.URL.RawQuery
		gotAuthorization = r.Header.Get("Authorization")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"branches":[],"tags":["1.20.0","1.21.0"]}`))
	}))
	defer server.Close()

	client := newTestOCITagsClient(server, "a-token")

	tags, err := client.ListOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.20.0", "1.21.0"}, tags)

	assert.Equal(t, "/api/v1/repositories/ghcr.io%2Fmyorg%2Fcharts%2Fnginx/oci-tags", gotPath)
	assert.Equal(t, "appProject=team-a", gotRawQuery)
	assert.Equal(t, "Bearer a-token", gotAuthorization)
}

// TestOCITagsClient_ArtifactWithScheme checks that an artifact named with the oci:// scheme
// reaches ArgoCD with the scheme intact, since that is the URL a repository registered that
// way is known by.
func TestOCITagsClient_ArtifactWithScheme(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"tags":["1.21.0"]}`))
	}))
	defer server.Close()

	tags, err := newTestOCITagsClient(server, "a-token").ListOCITags(context.Background(), "oci://ghcr.io/myorg/nginx", "team-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.21.0"}, tags)

	// The escaped artifact is a single path segment, and arrives as the artifact it was.
	assert.Equal(t, "/api/v1/repositories/oci://ghcr.io/myorg/nginx/oci-tags", gotPath)
}

// TestOCITagsClient_WithoutProject checks that a globally registered repository is asked for
// without a project.
func TestOCITagsClient_WithoutProject(t *testing.T) {
	var gotRawQuery string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"tags":["1.21.0"]}`))
	}))
	defer server.Close()

	tags, err := newTestOCITagsClient(server, "a-token").ListOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.21.0"}, tags)
	assert.Empty(t, gotRawQuery)
}

// TestOCITagsClient_ArtifactWithoutTags checks that an artifact ArgoCD reports no tags for
// is not mistaken for a failure.
func TestOCITagsClient_ArtifactWithoutTags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	tags, err := newTestOCITagsClient(server, "a-token").ListOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "team-a")
	require.NoError(t, err)
	assert.Empty(t, tags)
}

// TestOCITagsClient_NotFound checks that a 404 explains itself: the endpoint only exists in
// recent ArgoCD versions, which a bare "404" would leave the user guessing about.
func TestOCITagsClient_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not Found"}`))
	}))
	defer server.Close()

	_, err := newTestOCITagsClient(server, "a-token").ListOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "team-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ArgoCD 3.1")

	// The status survives the added explanation, so retrying can still tell that asking
	// again is pointless.
	var statusErr *responseStatusError
	require.True(t, errors.As(err, &statusErr))
	assert.Equal(t, http.StatusNotFound, statusErr.status)
	assert.False(t, worthRetrying(context.Background(), err))
}

// TestOCITagsClient_ServerError checks that a failing ArgoCD is reported as something worth
// retrying.
func TestOCITagsClient_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream is down"))
	}))
	defer server.Close()

	_, err := newTestOCITagsClient(server, "a-token").ListOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "team-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream is down")
	assert.True(t, worthRetrying(context.Background(), err))
}

// TestOCITagsClient_Unauthorized checks that a rejected token is not asked about again.
func TestOCITagsClient_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := newTestOCITagsClient(server, "a-token").ListOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "team-a")
	require.Error(t, err)
	assert.False(t, worthRetrying(context.Background(), err))
}

// TestOCITagsClient_UnexpectedBody checks that an answer that is not the expected JSON is
// reported instead of being read as an artifact without tags. A proxy in front of ArgoCD
// answering with a login page is the usual reason.
func TestOCITagsClient_UnexpectedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>please log in</html>"))
	}))
	defer server.Close()

	_, err := newTestOCITagsClient(server, "a-token").ListOCITags(context.Background(), "ghcr.io/myorg/charts/nginx", "team-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse")
}

// TestOCITagsClient_CancelledContext checks that a cancelled run does not wait for ArgoCD.
func TestOCITagsClient_CancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tags":["1.21.0"]}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newTestOCITagsClient(server, "a-token").ListOCITags(ctx, "ghcr.io/myorg/charts/nginx", "team-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, worthRetrying(ctx, err))
}

// TestOCITagsClient_RequestTimeoutIsRetried checks that an ArgoCD that accepts the request
// and then says nothing is not mistaken for a run that was called off: the attempt runs
// into its own deadline, and another attempt is what that deserves.
func TestOCITagsClient_RequestTimeoutIsRetried(t *testing.T) {
	answer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-answer
		_, _ = w.Write([]byte(`{"tags":["1.21.0"]}`))
	}))
	defer server.Close()
	defer close(answer)

	client := newTestOCITagsClient(server, "a-token")
	client.requestTimeout = 20 * time.Millisecond

	ctx := context.Background()

	_, err := client.ListOCITags(ctx, "ghcr.io/myorg/charts/nginx", "team-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// The run itself is still going, which is what makes the timeout worth another try.
	assert.True(t, worthRetrying(ctx, err))
}

func TestRestBaseURL(t *testing.T) {
	tests := []struct {
		name      string
		serverURL string
		expected  string
	}{
		{
			name:      "bare host gets https",
			serverURL: "argocd.example.com",
			expected:  "https://argocd.example.com",
		},
		{
			name:      "host with port gets https",
			serverURL: "argocd.example.com:8080",
			expected:  "https://argocd.example.com:8080",
		},
		{
			name:      "https is kept",
			serverURL: "https://argocd.example.com",
			expected:  "https://argocd.example.com",
		},
		{
			name:      "http is kept",
			serverURL: "http://localhost:8080",
			expected:  "http://localhost:8080",
		},
		{
			name:      "trailing slash is dropped",
			serverURL: "https://argocd.example.com/",
			expected:  "https://argocd.example.com",
		},
		{
			name:      "surrounding spaces are dropped",
			serverURL: " argocd.example.com ",
			expected:  "https://argocd.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, restBaseURL(tt.serverURL))
		})
	}
}
