package argocd

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"

	"github.com/argoproj/argo-cd/v2/pkg/apiclient"
	"github.com/argoproj/argo-cd/v2/pkg/apiclient/application"
	"github.com/argoproj/argo-cd/v2/pkg/apiclient/session"
	"github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	"github.com/sirupsen/logrus"
)

// Client wraps ArgoCD API client
type Client struct {
	apiClient apiclient.Client
	appClient application.ApplicationServiceClient
	logger    *logrus.Entry
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

	logger.Info("Successfully created ArgoCD API client")

	return &Client{
		apiClient: apiClient,
		appClient: appClient,
		logger:    logger,
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

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
