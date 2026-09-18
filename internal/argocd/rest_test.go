package argocd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRestClient points a REST client at a test server.
func newTestRestClient(server *httptest.Server) *restClient {
	return &restClient{
		baseURL:    server.URL,
		authToken:  "a-token",
		httpClient: server.Client(),
	}
}

// TestLogin checks the login Argazer performs when it is given a username and password: the
// credentials are posted to the session endpoint and the token of the answer is what comes
// back.
func TestLogin(t *testing.T) {
	var gotMethod, gotPath, gotContentType string
	var gotBody sessionRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		_, _ = w.Write([]byte(`{"token":"session-token"}`))
	}))
	defer server.Close()

	client := &restClient{baseURL: server.URL, httpClient: server.Client()}

	token, err := client.login(context.Background(), "admin", "secret")
	require.NoError(t, err)
	assert.Equal(t, "session-token", token)

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/api/v1/session", gotPath)
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, sessionRequest{Username: "admin", Password: "secret"}, gotBody)
}

// TestLogin_Rejected checks that credentials ArgoCD turns down are reported as a failure to
// authenticate rather than as an empty token.
func TestLogin_Rejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Invalid username or password"}`))
	}))
	defer server.Close()

	client := &restClient{baseURL: server.URL, httpClient: server.Client()}

	_, err := client.login(context.Background(), "admin", "wrong")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to authenticate with ArgoCD")
	assert.Contains(t, err.Error(), "Invalid username or password")
}

// TestLogin_WithoutToken checks that an accepted login that carries no token is reported,
// instead of leaving every later call to fail unauthenticated.
func TestLogin_WithoutToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := &restClient{baseURL: server.URL, httpClient: server.Client()}

	_, err := client.login(context.Background(), "admin", "secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no token")
}

// TestGetJSON_AnonymousRequest checks that a client without a token sends no Authorization
// header at all, rather than an empty one.
func TestGetJSON_AnonymousRequest(t *testing.T) {
	var hadAuthorization bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuthorization = r.Header["Authorization"]
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := &restClient{baseURL: server.URL, httpClient: server.Client()}

	var out struct{}
	require.NoError(t, client.getJSON(context.Background(), apiEndpoint{path: "/api/v1/applications"}, &out))
	assert.False(t, hadAuthorization)
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
