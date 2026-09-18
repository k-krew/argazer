package helm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
)

// TestOCICheckerGetLatestVersion_Success tests successful retrieval from OCI registry
func TestOCICheckerGetLatestVersion_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a valid OCI tags response
		tagsJSON := `{
  "name": "myrepo/nginx",
  "tags": ["1.21.0", "1.20.0", "1.19.5", "latest", "dev"]
}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, tagsJSON)
	}))
	defer server.Close()

	logger := logrus.NewEntry(logrus.New())
	checker := NewOCIChecker(logger)

	ctx := context.Background()
	// Extract just the host from server.URL (remove http://)
	repoURL := server.URL[7:] + "/myrepo"
	version, err := checker.GetLatestVersion(ctx, repoURL, "nginx")
	if err != nil {
		t.Fatalf("GetLatestVersion failed: %v", err)
	}

	expected := "1.21.0"
	if version != expected {
		t.Errorf("Expected version %s, got %s", expected, version)
	}
}

// TestOCICheckerGetLatestVersion_Unauthorized tests handling of authentication errors
func TestOCICheckerGetLatestVersion_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"errors":[{"code":"UNAUTHORIZED"}]}`)
	}))
	defer server.Close()

	logger := logrus.NewEntry(logrus.New())
	checker := NewOCIChecker(logger)

	ctx := context.Background()
	repoURL := server.URL[7:]
	_, err := checker.GetLatestVersion(ctx, repoURL, "nginx")
	if err == nil {
		t.Fatal("Expected error for unauthorized, got nil")
	}

	if !errors.Is(err, ErrAuthenticationFailed) {
		t.Errorf("Expected ErrAuthenticationFailed, got: %v", err)
	}
}

// TestOCICheckerGetLatestVersion_NotFound tests handling of 404 errors
func TestOCICheckerGetLatestVersion_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errors":[{"code":"NAME_UNKNOWN"}]}`)
	}))
	defer server.Close()

	logger := logrus.NewEntry(logrus.New())
	checker := NewOCIChecker(logger)

	ctx := context.Background()
	repoURL := server.URL[7:]
	_, err := checker.GetLatestVersion(ctx, repoURL, "nginx")
	if err == nil {
		t.Fatal("Expected error for not found, got nil")
	}

	if !errors.Is(err, ErrChartNotFound) {
		t.Errorf("Expected ErrChartNotFound, got: %v", err)
	}
}

// TestOCICheckerGetLatestVersion_InvalidJSON tests handling of invalid JSON
func TestOCICheckerGetLatestVersion_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "this is not valid JSON")
	}))
	defer server.Close()

	logger := logrus.NewEntry(logrus.New())
	checker := NewOCIChecker(logger)

	ctx := context.Background()
	repoURL := server.URL[7:]
	_, err := checker.GetLatestVersion(ctx, repoURL, "nginx")
	if err == nil {
		t.Fatal("Expected error for invalid JSON, got nil")
	}
}

// TestOCICheckerGetLatestVersion_NoValidVersions tests handling of no valid semver tags
func TestOCICheckerGetLatestVersion_NoValidVersions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return only non-semver tags
		tagsJSON := `{
  "name": "myrepo/nginx",
  "tags": ["latest", "dev", "main", "stable"]
}`
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, tagsJSON)
	}))
	defer server.Close()

	logger := logrus.NewEntry(logrus.New())
	checker := NewOCIChecker(logger)

	ctx := context.Background()
	repoURL := server.URL[7:]
	_, err := checker.GetLatestVersion(ctx, repoURL, "nginx")
	if err == nil {
		t.Fatal("Expected error for no valid versions, got nil")
	}

	if !errors.Is(err, ErrNoValidVersions) {
		t.Errorf("Expected ErrNoValidVersions, got: %v", err)
	}
}

// TestOCICheckerGetLatestVersion_WithVPrefix tests handling of versions with v prefix
func TestOCICheckerGetLatestVersion_WithVPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tagsJSON := `{
  "name": "myrepo/app",
  "tags": ["v1.0.0", "v2.0.0", "v1.5.0", "latest"]
}`
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, tagsJSON)
	}))
	defer server.Close()

	logger := logrus.NewEntry(logrus.New())
	checker := NewOCIChecker(logger)

	ctx := context.Background()
	repoURL := server.URL[7:]
	version, err := checker.GetLatestVersion(ctx, repoURL, "app")
	if err != nil {
		t.Fatalf("GetLatestVersion failed: %v", err)
	}

	expected := "v2.0.0"
	if version != expected {
		t.Errorf("Expected version %s, got %s", expected, version)
	}
}

// TestOCICheckerGetLatestVersion_MixedValidInvalid tests filtering of mixed valid/invalid tags
func TestOCICheckerGetLatestVersion_MixedValidInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tagsJSON := `{
  "name": "myrepo/app",
  "tags": ["1.0.0", "not-a-version", "2.0.0", "latest", "3.0.0-beta", "dev"]
}`
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, tagsJSON)
	}))
	defer server.Close()

	logger := logrus.NewEntry(logrus.New())
	checker := NewOCIChecker(logger)

	ctx := context.Background()
	repoURL := server.URL[7:]
	version, err := checker.GetLatestVersion(ctx, repoURL, "app")
	if err != nil {
		t.Fatalf("GetLatestVersion failed: %v", err)
	}

	// Should return the highest valid semver (pre-release versions are valid)
	expected := "3.0.0-beta"
	if version != expected {
		t.Errorf("Expected version %s, got %s", expected, version)
	}
}

// TestParseOCIURL tests OCI URL parsing
func TestParseOCIURL(t *testing.T) {
	tests := []struct {
		input        string
		expectedReg  string
		expectedPath string
	}{
		{"ghcr.io/myorg/charts", "ghcr.io", "myorg/charts"},
		{"harbor.company.com/helm", "harbor.company.com", "helm"},
		{"registry.example.com", "registry.example.com", ""},
		{"localhost:5000/myrepo", "localhost:5000", "myrepo"},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			registry, repoPath := parseOCIURL(test.input)
			if registry != test.expectedReg {
				t.Errorf("Expected registry %s, got %s", test.expectedReg, registry)
			}
			if repoPath != test.expectedPath {
				t.Errorf("Expected path %s, got %s", test.expectedPath, repoPath)
			}
		})
	}
}
