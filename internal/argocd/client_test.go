package argocd

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestNewClient_InvalidURL(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	// Test with invalid/unreachable ArgoCD server
	_, err := NewClient("http://invalid-argocd-server-that-does-not-exist.example.com", "admin", "password", "", false, logger)
	// Should fail because the server doesn't exist
	assert.Error(t, err)
}

func TestNewClient_EmptyCredentials(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	// Test with empty credentials
	_, err := NewClient("http://localhost:8080", "", "", "", false, logger)
	// Should fail during authentication
	assert.Error(t, err)
}

// TestNewClient_AuthToken checks that a token short-circuits the login round-trip:
// the client is built without ever reaching the (non-existent) server.
func TestNewClient_AuthToken(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())

	client, err := NewClient("http://invalid-argocd-server-that-does-not-exist.example.com", "", "", "some-token", false, logger)
	require.NoError(t, err)
	assert.NotNil(t, client)
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

// Note: Full integration tests for ListApplications would require a running ArgoCD instance
// or extensive mocking of the ArgoCD API client, which is complex due to the interface structure.
// The contains() function and basic client creation are tested above.
// For production, consider using integration tests with a real or containerized ArgoCD instance.
