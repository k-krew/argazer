![Claude Assisted](https://img.shields.io/badge/Made%20with-Claude-8A2BE2?logo=anthropic)
![CI](https://github.com/kreicer/argazer/actions/workflows/ci.yml/badge.svg)
[![codecov](https://codecov.io/gh/kreicer/argazer/branch/main/graph/badge.svg)](https://codecov.io/gh/kreicer/argazer)

# Argazer

**Argazer** (a wordplay on "Argo" and "gazer") is a lightweight tool that monitors your ArgoCD applications for Helm chart updates. It connects to ArgoCD via API, scans your applications, and notifies you when newer versions are available.

## Example

```bash
$ argazer

================================================================================
ARGAZER SCAN RESULTS
================================================================================

Total applications checked: 15

Up to date: 11
Updates available: 4
Skipped: 0

--------------------------------------------------------------------------------
APPLICATIONS WITH UPDATES AVAILABLE:
--------------------------------------------------------------------------------

Application: cert-manager
  Project: internal
  Chart: cert-manager
  Current Version: 1.18.5
  Latest Version: v1.20.0
  Repository: https://charts.jetstack.io
...
```

## Features

- **Single-run execution** - Runs once on launch, perfect for CI/CD or cron jobs
- **Multiple output formats** - Table (human-readable), JSON (programmatic), or Markdown (documentation)
- **Flexible logging** - JSON (production) or text (development) log formats
- **Interactive configuration** - `argazer configure` command with step-by-step wizard
- **Git Repository Support** - Monitor Helm charts stored in Git repositories (GitHub, GitLab, Bitbucket, etc.)
- **OCI Registry Support** - Works with OCI-based Helm repositories (Harbor, GHCR, ACR, etc.)
- **Traditional Helm Repos** - Supports classic HTTP-based Helm chart repositories
- **Flexible filtering** - Filter by projects, application names, and labels
- **Multiple notification channels** - Telegram, Email, Slack, Microsoft Teams, Generic Webhooks, or console-only output
- **Secure ArgoCD connection** - Username/password authentication with optional TLS verification
- **Environment variable support** - All settings configurable via AG_* environment variables
- **Graceful error handling** - Clear error messages for unsupported scenarios
- **Multi-source support** - Handles ArgoCD applications with multiple Helm sources

## Installation

### From Source

```bash
git clone git@github.com:kreicer/argazer.git
cd argazer
go build -o argazer .
```

### Using Docker

Multi-architecture images available for **AMD64** and **ARM64** (Apple Silicon, Raspberry Pi, AWS Graviton, etc.):

```bash
# Pull the latest image (automatically selects the right architecture)
docker pull ghcr.io/kreicer/argazer:latest

# Or specific version
docker pull ghcr.io/kreicer/argazer:v1.0.4

# Or build locally
docker build -t argazer:latest .
```

## Configuration

Argazer can be configured via:
1. Configuration file (config.yaml)
2. Command-line flags
3. Environment variables (prefixed with `AG_`)

### Configuration File

Create a `config.yaml` file:

```yaml
# ArgoCD Connection
argocd_url: "argocd.example.com"  # Just hostname, no https:// prefix
argocd_username: "admin"
argocd_password: "your-password"
argocd_insecure: false  # Set to true to skip TLS verification

# Search Scope
projects:
  - "*"  # All projects, or specify: ["project1", "project2"]
app_names:
  - "*"  # All apps, or specify: ["app1", "app2"]
labels:  # Optional: filter by labels
  type: "operator"
  environment: "production"

# Notification Channel ("telegram", "email", "slack", "teams", "webhook", or empty for console-only)
notification_channel: "telegram"

# Telegram Settings
telegram_webhook: "https://api.telegram.org/botTOKEN/sendMessage"
telegram_chat_id: "123456789"

# Email Settings
email_smtp_host: "smtp.gmail.com"
email_smtp_port: 587
email_smtp_username: "your-email@gmail.com"
email_smtp_password: "your-app-password"
email_from: "argazer@example.com"
email_to:
  - "devops@example.com"
email_use_tls: true

# Slack Settings
slack_webhook: "https://hooks.slack.com/services/YOUR/WEBHOOK/URL"

# Microsoft Teams Settings
teams_webhook: "https://outlook.office.com/webhook/YOUR/WEBHOOK/URL"

# Generic Webhook Settings
webhook_url: "https://your-webhook-endpoint.example.com/notify"

# General
verbose: false
source_name: "chart-repo"  # For multi-source apps, specify which source to check
concurrency: 10  # Number of concurrent workers (default: 10)

# Version Constraint Strategy
# Controls which version updates to check for:
# - "major": Check all versions (default)
# - "minor": Only same major version
# - "patch": Only same major.minor
version_constraint: "major"

# Output Format
# Controls how results are displayed:
# - "table": Human-readable formatted text (default)
# - "json": JSON structured output for automation
# - "markdown": Markdown formatted output for docs
output_format: "table"

# Log Format
# Controls the format of application logs (not scan results):
# - "json": Structured JSON logs for production (default)
# - "text": Human-readable text logs for development
log_format: "json"
```

### Environment Variables

All configuration options can be set via environment variables with the `AG_` prefix:

```bash
# ArgoCD Connection
export AG_ARGOCD_URL="argocd.example.com"
export AG_ARGOCD_USERNAME="admin"
export AG_ARGOCD_PASSWORD="your-password"
export AG_ARGOCD_INSECURE="false"

# Search Scope
export AG_PROJECTS="project1,project2"  # or "*" for all
export AG_APP_NAMES="app1,app2"         # or "*" for all
export AG_LABELS="type=operator,environment=production"  # Format: key1=value1,key2=value2

# Notification
export AG_NOTIFICATION_CHANNEL="telegram"  # "telegram", "email", "slack", "teams", "webhook", or empty

# Telegram
export AG_TELEGRAM_WEBHOOK="https://api.telegram.org/botTOKEN/sendMessage"
export AG_TELEGRAM_CHAT_ID="123456789"

# Email
export AG_EMAIL_SMTP_HOST="smtp.gmail.com"
export AG_EMAIL_SMTP_PORT="587"
export AG_EMAIL_SMTP_USERNAME="your-email@gmail.com"
export AG_EMAIL_SMTP_PASSWORD="your-app-password"
export AG_EMAIL_FROM="argazer@example.com"
export AG_EMAIL_TO="devops@example.com,team@example.com"
export AG_EMAIL_USE_TLS="true"

# Slack
export AG_SLACK_WEBHOOK="https://hooks.slack.com/services/YOUR/WEBHOOK/URL"

# Microsoft Teams
export AG_TEAMS_WEBHOOK="https://outlook.office.com/webhook/YOUR/WEBHOOK/URL"

# Generic Webhook
export AG_WEBHOOK_URL="https://your-webhook-endpoint.example.com/notify"

# General
export AG_VERBOSE="false"
export AG_SOURCE_NAME="chart-repo"
export AG_CONCURRENCY="10"  # Number of concurrent workers

# Version Constraint
export AG_VERSION_CONSTRAINT="major"  # "major", "minor", or "patch"

# Output Format
export AG_OUTPUT_FORMAT="table"  # "table", "json", or "markdown"

# Log Format
export AG_LOG_FORMAT="json"  # "json" or "text"
```

## ArgoCD RBAC Setup

Argazer requires minimal read-only permissions in ArgoCD. Create a dedicated user with the following RBAC policy:

```yaml
# argocd-cm ConfigMap - Create the user
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
  accounts.argazer: apiKey, login
```

```yaml
# argocd-rbac-cm ConfigMap - Set permissions
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-rbac-cm
  namespace: argocd
data:
  policy.csv: |
    # Allow listing and reading applications
    p, role:argazer-reader, applications, get, */*, allow
    p, role:argazer-reader, applications, list, */*, allow
    g, argazer, role:argazer-reader
```

Set the user password:

```bash
argocd account update-password --account argazer --new-password <secure-password>
```

For project-specific access, replace `*/*` with `<project-name>/*` in the RBAC policy.

## Usage

### Quick Start with Interactive Configuration

The easiest way to get started is with the interactive configuration wizard:

```bash
./argazer configure
```

This will guide you through:
1. ArgoCD connection setup
2. Application filtering
3. Version constraint preferences
4. Notification channel setup (with test!)
5. Saving configuration to config.yaml

### Basic Usage

```bash
# Run with config file
./argazer --config config.yaml

# Run with environment variables
AG_ARGOCD_URL="argocd.example.com" \
AG_ARGOCD_USERNAME="admin" \
AG_ARGOCD_PASSWORD="password" \
./argazer

# Run with flags
./argazer \
  --argocd-url="argocd.example.com" \
  --argocd-username="admin" \
  --argocd-password="password" \
  --projects="production" \
  --notification-channel="telegram"
```

### Filtering Examples

```bash
# Check all applications in all projects
./argazer --projects="*" --app-names="*"

# Check specific projects
./argazer --projects="production,staging"

# Check specific applications
./argazer --app-names="frontend,backend"

# Filter by labels (using environment variable)
AG_LABELS="type=operator,environment=production" ./argazer

# Combine filters
./argazer --projects="production" --app-names="frontend,backend"

# Using config file with label filters (see config.yaml example)
./argazer --config config.yaml
```

### Notification Examples

```bash
# Console output only (no notifications)
./argazer --notification-channel=""

# Send Telegram notifications
./argazer --notification-channel="telegram"

# Send Email notifications
./argazer --notification-channel="email"

# Send Slack notifications
./argazer --notification-channel="slack"

# Send Microsoft Teams notifications
./argazer --notification-channel="teams"

# Send generic webhook notifications
./argazer --notification-channel="webhook"
```

### Output Format Examples

Control how results are displayed - choose the format that best fits your use case:

```bash
# Table format (default) - human-readable formatted text
./argazer --output-format="table"
./argazer -o table

# JSON format - structured output for automation/CI/CD
./argazer --output-format="json"
./argazer -o json | jq '.summary'

# Markdown format - formatted output for documentation/reports
./argazer --output-format="markdown" > report.md
./argazer -o markdown

# Using environment variable
AG_OUTPUT_FORMAT="json" ./argazer

# In config file
# output_format: "markdown"
```

**Format Details:**

- **`table`** (default): Human-readable formatted text with sections and borders
  - Best for: Console viewing, manual monitoring
  - Example: Formatted sections with headers, borders, and indented details

- **`json`**: Structured JSON with summary and categorized results
  - Best for: CI/CD pipelines, automation, programmatic parsing
  - Example: `{"summary": {"total": 7, "updates_available": 2}, ...}`
  - Pipe to `jq` for filtering: `./argazer -o json | jq '.updates_available'`

- **`markdown`**: Clean markdown with tables and headers
  - Best for: Documentation, reports, GitHub/GitLab issues
  - Example: Markdown headers, tables, and formatted sections
  - Save to file: `./argazer -o markdown > weekly-report.md`

### Version Constraint Examples

Control which version updates to check for based on semantic versioning:

```bash
# Check all versions (default) - any major, minor, or patch updates
./argazer --version-constraint="major"

# Only check within same major version - minor and patch updates only
# Example: Current 1.2.3 → will find 1.3.0, 1.2.5, but skip 2.0.0
./argazer --version-constraint="minor"

# Only check within same major.minor - patch updates only
# Example: Current 1.2.3 → will find 1.2.5, but skip 1.3.0 and 2.0.0
./argazer --version-constraint="patch"

# Using environment variable
AG_VERSION_CONSTRAINT="minor" ./argazer

# In config file
# version_constraint: "patch"
```

**How it works:**

- **`major` (default)**: Check all available versions
  - Current: `1.2.3` → Finds: `2.0.0`, `1.3.0`, `1.2.5`
  - Reports latest: `2.0.0`

- **`minor`**: Only check versions with same major number
  - Current: `1.2.3` → Finds: `1.3.0`, `1.2.5` (skips `2.0.0`)
  - Reports latest: `1.3.0`
  - Note: Shows `2.0.0` available outside constraint

- **`patch`**: Only check versions with same major.minor
  - Current: `1.2.3` → Finds: `1.2.5` (skips `1.3.0`, `2.0.0`)
  - Reports latest: `1.2.5`
  - Note: Shows `2.0.0` available outside constraint

**Use cases:**

- **Production stability**: Use `minor` or `patch` to avoid breaking changes
- **Security patches**: Use `patch` to only get bug fixes
- **Stay current**: Use `major` (default) to see all updates

### Cron Job Example

Add to your crontab to run every hour:

```cron
0 * * * * /path/to/argazer --config /path/to/config.yaml
```

### Docker Usage

```bash
# Using config file (mount to /app/config.yaml)
docker run --rm \
  -v $(pwd)/config.yaml:/app/config.yaml:ro \
  ghcr.io/kreicer/argazer:latest

# Using environment variables only
docker run --rm \
  -e AG_ARGOCD_URL="argocd.example.com" \
  -e AG_ARGOCD_USERNAME="admin" \
  -e AG_ARGOCD_PASSWORD="password" \
  -e AG_NOTIFICATION_CHANNEL="telegram" \
  -e AG_TELEGRAM_WEBHOOK="https://api.telegram.org/bot.../sendMessage" \
  -e AG_TELEGRAM_CHAT_ID="123456789" \
  ghcr.io/kreicer/argazer:latest

# Override default config path
docker run --rm \
  -v $(pwd)/custom-config.yaml:/config/argazer.yaml:ro \
  ghcr.io/kreicer/argazer:latest --config /config/argazer.yaml

# With verbose logging and text logs for debugging
docker run --rm \
  -v $(pwd)/config.yaml:/app/config.yaml:ro \
  ghcr.io/kreicer/argazer:latest --verbose --log-format=text

# Run with specific architecture (if needed)
docker run --rm --platform linux/arm64 \
  -v $(pwd)/config.yaml:/app/config.yaml:ro \
  ghcr.io/kreicer/argazer:latest

# Using docker-compose (see example below)
docker-compose up
```

## Supported Repository Types

Argazer automatically detects and supports three types of Helm chart repositories:

### Git Repositories 🆕
Git-based repositories containing Helm charts (detects GitHub, GitLab, Bitbucket, Gitea):
```yaml
repoURL: "https://github.com/myorg/helm-charts.git"
chart: "charts/myapp"  # Path to chart within repo

repoURL: "https://gitlab.com/myorg/charts.git"
chart: "frontend"  # Chart directory name
```

**Version Detection:**
- Reads versions from Git tags (e.g., `v1.2.3`, `chart-v1.0.0`)
- Supports semver tags with common prefixes
- For monorepos: Use chart-specific tags like `myapp-v1.2.3`

**Authentication:**
- HTTPS with username/password
- Configure via `repository_auth` or environment variables

### Traditional Helm Repositories
Classic HTTP-based Helm chart repositories with `index.yaml`:
```yaml
repoURL: "https://charts.bitnami.com/bitnami"
chart: "postgresql"
```

### OCI Registries (Docker Registry V2 API)
OCI-based registries identified by the absence of `http://` or `https://` prefix:
```yaml
repoURL: "ghcr.io/myorg/charts"  # GitHub Container Registry
chart: "my-application"

repoURL: "harbor.company.com/helm" # Harbor
chart: "backend"

repoURL: "myregistry.azurecr.io" # Azure Container Registry
chart: "frontend"
```

## Authentication for Private Repositories

> **⚠️ SECURITY WARNING**  
> **Do NOT store credentials in plain text config files!**  
> Config files may be accidentally committed to version control or shared insecurely.  
> **ALWAYS use environment variables for credentials in production.**

Argazer supports authentication for both traditional Helm repositories and OCI registries using config file or environment variables:

### Option 1: Environment Variables (Recommended)

**Use environment variables for all credentials in production:**

```bash
# ArgoCD credentials
export AG_ARGOCD_URL="argocd.example.com"
export AG_ARGOCD_USERNAME="admin"
export AG_ARGOCD_PASSWORD="${ARGOCD_PASSWORD}"  # From secrets manager

# Registry credentials
export AG_AUTH_URL_1="harbor.company.com"
export AG_AUTH_USER_1="${HARBOR_USER}"
export AG_AUTH_PASS_1="${HARBOR_PASSWORD}"
```

### Option 2: Config File (Local Development Only)

**Only for local development. DO NOT commit credentials to git!**

Add to your `config.yaml` (make sure it's in `.gitignore`):

```yaml
repository_auth:
  - url: "harbor.company.com"
    username: "myuser"
    password: "mypassword"
  
  - url: "ghcr.io"
    username: "github-user"
    password: "ghp_token"
```

**Note:** Environment variables take precedence over config file credentials.

### Environment Variables Format

```bash
# Format: AG_AUTH_URL_<id>, AG_AUTH_USER_<id>, AG_AUTH_PASS_<id>
# The <id> can be any alphanumeric identifier (numbers or descriptive names)

# Example 1: Using numbers
export AG_AUTH_URL_1="registry.example.com"
export AG_AUTH_USER_1="myuser"
export AG_AUTH_PASS_1="mypassword"

# Example 2: Using descriptive names
export AG_AUTH_URL_HARBOR="harbor.company.com"
export AG_AUTH_USER_HARBOR="myuser"
export AG_AUTH_PASS_HARBOR="mypassword"

# Example 3: GitHub Container Registry
export AG_AUTH_URL_GHCR="ghcr.io"
export AG_AUTH_USER_GHCR="github-user"
export AG_AUTH_PASS_GHCR="ghp_token"

# Example 4: Traditional Helm repo
export AG_AUTH_URL_CHARTS="charts.private.com"
export AG_AUTH_USER_CHARTS="helmuser"
export AG_AUTH_PASS_CHARTS="helmpass"

# Run argazer
./argazer
```

### Multiple Registries

You can authenticate to multiple registries at once:

```bash
export AG_AUTH_URL_1="harbor.company.com"
export AG_AUTH_USER_1="user1"
export AG_AUTH_PASS_1="pass1"

export AG_AUTH_URL_2="ghcr.io"
export AG_AUTH_USER_2="user2"
export AG_AUTH_PASS_2="pass2"

export AG_AUTH_URL_3="charts.private.com"
export AG_AUTH_USER_3="user3"
export AG_AUTH_PASS_3="pass3"

./argazer
```

### CI/CD Example (Secure)

Always use your CI/CD platform's secrets management:

```yaml
# GitHub Actions
- name: Run Argazer
  env:
    # ArgoCD credentials from secrets
    AG_ARGOCD_URL: argocd.example.com
    AG_ARGOCD_USERNAME: ${{ secrets.ARGOCD_USER }}
    AG_ARGOCD_PASSWORD: ${{ secrets.ARGOCD_PASS }}
    
    # Registry credentials from secrets
    AG_AUTH_URL_1: registry.example.com
    AG_AUTH_USER_1: ${{ secrets.REGISTRY_USER }}
    AG_AUTH_PASS_1: ${{ secrets.REGISTRY_PASS }}
  run: ./argazer
```

**Other CI Platforms:**
- **GitLab CI**: Use `$CI_JOB_TOKEN` or Variables
- **Jenkins**: Use Credentials Plugin
- **CircleCI**: Use Contexts/Environment Variables
- **Azure DevOps**: Use Variable Groups

#### Docker Compose Example

Create a `docker-compose.yml` file:

```yaml
version: '3.8'

services:
  argazer:
    image: ghcr.io/kreicer/argazer:latest
    # Or build locally:
    # build: .
    environment:
      - AG_ARGOCD_URL=argocd.example.com
      - AG_ARGOCD_USERNAME=admin
      - AG_ARGOCD_PASSWORD=${ARGOCD_PASSWORD}  # Use .env file
      - AG_PROJECTS=production,staging
      - AG_LABELS=type=operator,environment=production  # Optional: filter by labels
      - AG_NOTIFICATION_CHANNEL=telegram
      - AG_TELEGRAM_WEBHOOK=${TELEGRAM_WEBHOOK}
      - AG_TELEGRAM_CHAT_ID=${TELEGRAM_CHAT_ID}
      - AG_OUTPUT_FORMAT=json  # table, json, or markdown
      - AG_LOG_FORMAT=json  # json or text
    # Or mount config file instead
    # volumes:
    #   - ./config.yaml:/app/config.yaml:ro
    restart: "no"  # Run once and exit
```

Then run:

```bash
# Create .env file with sensitive data
cat > .env << EOF
ARGOCD_PASSWORD=your-password
TELEGRAM_WEBHOOK=https://api.telegram.org/bot.../sendMessage
TELEGRAM_CHAT_ID=123456789
EOF

# Run
docker-compose up
```

## Output Examples

### Table Format (Default)

Human-readable formatted text with sections and borders:

```
================================================================================
ARGAZER SCAN RESULTS
================================================================================

Total applications checked: 25

Up to date: 18
Updates available: 5
Skipped: 2

--------------------------------------------------------------------------------
APPLICATIONS WITH UPDATES AVAILABLE:
--------------------------------------------------------------------------------

Application: frontend
  Project: production
  Chart: nginx
  Current Version: 1.20.0
  Latest Version: 1.21.0
  Version Constraint: minor
  Repository: https://charts.bitnami.com/bitnami

Application: backend
  Project: production
  Chart: postgresql
  Current Version: 11.9.13
  Latest Version: 11.10.0
  Repository: https://charts.bitnami.com/bitnami

Application: api
  Project: staging
  Chart: fastapi
  Current Version: 0.95.0
  Latest Version: 0.95.2
  Version Constraint: patch
  Repository: oci://ghcr.io/myorg/charts

--------------------------------------------------------------------------------
UP TO DATE (with updates outside constraint):
--------------------------------------------------------------------------------

Application: monitoring
  Project: platform
  Chart: grafana
  Current Version: 6.50.0
  Status: Up to date within 'minor' constraint
  Latest (minor): 6.50.0
  Note: Version 7.0.0 available outside constraint
  Repository: https://grafana.github.io/helm-charts

Application: logging
  Project: platform
  Chart: loki
  Current Version: 5.8.0
  Status: Up to date within 'patch' constraint
  Latest (patch): 5.8.0
  Note: Version 5.9.2 available outside constraint
  Repository: https://grafana.github.io/helm-charts

--------------------------------------------------------------------------------
APPLICATIONS SKIPPED (Unable to check):
--------------------------------------------------------------------------------

Application: internal-app
  Project: platform
  Chart: custom-chart
  Repository: cr.example.com/helm
  Reason: failed to fetch chart versions: 404 Not Found

Application: legacy-service
  Project: legacy
  Chart: old-app
  Repository: https://charts.deprecated.io
  Reason: no valid semantic versions found in repository

================================================================================
```

### JSON Format

Structured output perfect for automation and CI/CD pipelines:

```json
{
  "summary": {
    "total": 25,
    "up_to_date": 18,
    "updates_available": 5,
    "skipped": 2
  },
  "updates_available": [
    {
      "app_name": "frontend",
      "project": "production",
      "chart_name": "nginx",
      "current_version": "1.20.0",
      "latest_version": "1.21.0",
      "repo_url": "https://charts.bitnami.com/bitnami",
      "has_update": true,
      "constraint_applied": "minor",
      "has_update_outside_constraint": false
    },
    {
      "app_name": "api",
      "project": "staging",
      "chart_name": "fastapi",
      "current_version": "0.95.0",
      "latest_version": "0.95.2",
      "repo_url": "oci://ghcr.io/myorg/charts",
      "has_update": true,
      "constraint_applied": "patch",
      "has_update_outside_constraint": true,
      "latest_version_all": "0.96.0"
    }
  ],
  "up_to_date_with_constraint": [
    {
      "app_name": "monitoring",
      "project": "platform",
      "chart_name": "grafana",
      "current_version": "6.50.0",
      "latest_version": "6.50.0",
      "repo_url": "https://grafana.github.io/helm-charts",
      "has_update": false,
      "constraint_applied": "minor",
      "has_update_outside_constraint": true,
      "latest_version_all": "7.0.0"
    }
  ],
  "up_to_date": [
    {
      "app_name": "database",
      "project": "production",
      "chart_name": "postgresql",
      "current_version": "12.0.0",
      "latest_version": "12.0.0",
      "repo_url": "https://charts.bitnami.com/bitnami",
      "has_update": false,
      "constraint_applied": "major",
      "has_update_outside_constraint": false
    }
  ],
  "errors": [
    {
      "app_name": "internal-app",
      "project": "platform",
      "chart_name": "custom-chart",
      "repo_url": "cr.example.com/helm",
      "error": "failed to fetch chart versions: 404 Not Found"
    }
  ]
}
```

Use with `jq` for filtering and analysis:
```bash
# Get only apps with updates
./argazer -o json | jq '.updates_available[]'

# Count updates
./argazer -o json | jq '.summary.updates_available'

# Get app names with updates
./argazer -o json | jq -r '.updates_available[].app_name'

# Find apps with updates outside their constraint
./argazer -o json | jq '.updates_available[] | select(.has_update_outside_constraint == true)'

# Get apps using patch constraint
./argazer -o json | jq '.updates_available[] | select(.constraint_applied == "patch")'

# Check if any OCI registry apps have updates
./argazer -o json | jq '.updates_available[] | select(.repo_url | startswith("oci://"))'
```

### Markdown Format

Clean markdown output ideal for reports and documentation:

```markdown
# Argazer Scan Results

## Summary

- **Total applications checked:** 25
- **Up to date:** 18
- **Updates available:** 5
- **Skipped:** 2

## Applications with Updates Available

### frontend

| Field | Value |
|-------|-------|
| **Project** | production |
| **Chart** | nginx |
| **Current Version** | 1.20.0 |
| **Latest Version** | 1.21.0 |
| **Version Constraint** | minor |
| **Repository** | https://charts.bitnami.com/bitnami |

### api

| Field | Value |
|-------|-------|
| **Project** | staging |
| **Chart** | fastapi |
| **Current Version** | 0.95.0 |
| **Latest Version** | 0.95.2 |
| **Version Constraint** | patch |
| **Note** | Version 0.96.0 available outside constraint |
| **Repository** | oci://ghcr.io/myorg/charts |

## Up to Date (with updates outside constraint)

### monitoring

| Field | Value |
|-------|-------|
| **Project** | platform |
| **Chart** | grafana |
| **Current Version** | 6.50.0 |
| **Status** | Up to date within 'minor' constraint |
| **Latest (minor)** | 6.50.0 |
| **Note** | Version 7.0.0 available outside constraint |
| **Repository** | https://grafana.github.io/helm-charts |

## Applications Skipped

### internal-app

| Field | Value |
|-------|-------|
| **Project** | platform |
| **Chart** | custom-chart |
| **Repository** | cr.example.com/helm |
| **Reason** | failed to fetch chart versions: 404 Not Found |
```

Save to file for reports:
```bash
./argazer -o markdown > weekly-helm-updates.md
```

### JSON Logs

All operational logs (separate from output) are in structured JSON format:

```json
{"level":"info","msg":"Starting Argazer","argocd_url":"argo.example.com","projects":["production"],"app_names":["*"],"labels":{"type":"operator"},"time":"2025-10-15T12:00:00+00:00"}
{"level":"info","msg":"Creating ArgoCD API client","server":"argo.example.com","username":"admin","time":"2025-10-15T12:00:01+00:00"}
{"level":"info","msg":"Found applications","count":25,"time":"2025-10-15T12:00:02+00:00"}
{"level":"info","msg":"Processing application","app_name":"frontend","project":"production","time":"2025-10-15T12:00:02+00:00"}
{"level":"info","msg":"Found Helm-based application","app_name":"frontend","chart_name":"nginx","chart_version":"1.20.0","time":"2025-10-15T12:00:02+00:00"}
{"level":"warning","msg":"Update available!","app_name":"frontend","current_version":"1.20.0","latest_version":"1.21.0","time":"2025-10-15T12:00:03+00:00"}
{"level":"info","msg":"Argazer completed","total_checked":25,"time":"2025-10-15T12:00:15+00:00"}
```

## Notification Setup

### Telegram

**Setting up Telegram notifications:**

1. Create a new bot via [@BotFather](https://t.me/botfather)
2. Get your bot token (looks like `123456789:ABCdefGHIjklMNOpqrsTUVwxyz`)
3. Get your chat ID by messaging your bot and visiting: `https://api.telegram.org/bot<YOUR_BOT_TOKEN>/getUpdates`
4. Configure Argazer:
   ```bash
   export AG_NOTIFICATION_CHANNEL="telegram"
   export AG_TELEGRAM_WEBHOOK="https://api.telegram.org/bot<YOUR_BOT_TOKEN>/sendMessage"
   export AG_TELEGRAM_CHAT_ID="<YOUR_CHAT_ID>"
   ```

### Email

**Setting up Email notifications:**

For Gmail, you need to use an [App Password](https://support.google.com/accounts/answer/185833):

```bash
export AG_NOTIFICATION_CHANNEL="email"
export AG_EMAIL_SMTP_HOST="smtp.gmail.com"
export AG_EMAIL_SMTP_PORT="587"
export AG_EMAIL_SMTP_USERNAME="your-email@gmail.com"
export AG_EMAIL_SMTP_PASSWORD="your-app-password"
export AG_EMAIL_FROM="argazer@example.com"
export AG_EMAIL_TO="devops@example.com,team@example.com"
export AG_EMAIL_USE_TLS="true"
```

For other email providers, adjust the SMTP settings accordingly.

### Slack

**Setting up Slack notifications:**

1. Create a Slack app in your workspace
2. Enable Incoming Webhooks
3. Create a webhook URL for a channel
4. Configure Argazer:
   ```bash
   export AG_NOTIFICATION_CHANNEL="slack"
   export AG_SLACK_WEBHOOK="https://hooks.slack.com/services/YOUR/WEBHOOK/URL"
   ```

[Create a Slack App and Webhook](https://api.slack.com/messaging/webhooks)

### Microsoft Teams

**Setting up Microsoft Teams notifications:**

1. Open your Teams channel
2. Click the three dots (...) next to the channel name
3. Select "Connectors" → "Incoming Webhook"
4. Configure the webhook and copy the URL
5. Configure Argazer:
   ```bash
   export AG_NOTIFICATION_CHANNEL="teams"
   export AG_TEAMS_WEBHOOK="https://outlook.office.com/webhook/YOUR/WEBHOOK/URL"
   ```

[Learn more about Teams webhooks](https://learn.microsoft.com/en-us/microsoftteams/platform/webhooks-and-connectors/how-to/add-incoming-webhook)

### Generic Webhook

**Setting up generic webhook notifications:**

Argazer sends a POST request with JSON payload:
```json
{
  "subject": "Argazer Notification: 2 Helm Chart Update(s) Available",
  "message": "app1 (production)\n  Chart: nginx\n  Version: 1.0.0 -> 1.1.0\n..."
}
```

Configure your webhook endpoint:
```bash
export AG_NOTIFICATION_CHANNEL="webhook"
export AG_WEBHOOK_URL="https://your-webhook-endpoint.example.com/notify"
```

The webhook must accept POST requests and return a 2xx status code.

## Notification Formats

### Telegram

Argazer sends compact plain text messages to Telegram:

```
Argazer Notification: 2 Helm Chart Update(s) Available

frontend (production)
  Chart: nginx
  Version: 1.20.0 -> 1.21.0
  Repo: https://charts.bitnami.com/bitnami

backend (production)
  Chart: postgresql
  Version: 11.9.13 -> 11.10.0
  Repo: https://charts.bitnami.com/bitnami
```

For large numbers of updates, messages are automatically split to stay within Telegram's 4096 character limit:

```
Argazer Notification [1/3]: 27 Update(s)

app1 (production)
  Chart: redis
  Version: 7.0.0 -> 7.2.0
  Repo: https://charts.bitnami.com/bitnami

app2 (staging)
  Chart: postgresql
  Version: 15.0.0 -> 15.3.0
  Repo: https://charts.bitnami.com/bitnami

...
```

### Email

Plain text email with clean, compact formatting:

```
Subject: Argazer Notification: 2 Helm Chart Update(s) Available

frontend (production)
  Chart: nginx
  Version: 1.20.0 -> 1.21.0
  Repo: https://charts.bitnami.com/bitnami

backend (production)
  Chart: postgresql
  Version: 11.9.13 -> 11.10.0
  Repo: https://charts.bitnami.com/bitnami
```

### Slack

Slack messages with markdown formatting for the subject:

```
*Argazer Notification: 2 Helm Chart Update(s) Available*

frontend (production)
  Chart: nginx
  Version: 1.20.0 -> 1.21.0
  Repo: https://charts.bitnami.com/bitnami

backend (production)
  Chart: postgresql
  Version: 11.9.13 -> 11.10.0
  Repo: https://charts.bitnami.com/bitnami
```

### Microsoft Teams

Teams MessageCard format with structured layout:

**Title:** Argazer Notification: 2 Helm Chart Update(s) Available  
**Theme:** Blue card (#0078D7)

```
frontend (production)
  Chart: nginx
  Version: 1.20.0 -> 1.21.0
  Repo: https://charts.bitnami.com/bitnami

backend (production)
  Chart: postgresql
  Version: 11.9.13 -> 11.10.0
  Repo: https://charts.bitnami.com/bitnami
```

### Generic Webhook

JSON payload with separate subject and message fields:

```json
{
  "subject": "Argazer Notification: 2 Helm Chart Update(s) Available",
  "message": "frontend (production)\n  Chart: nginx\n  Version: 1.20.0 -> 1.21.0\n..."
}
```

## Development

### Prerequisites

- Go 1.21 or higher
- Access to an ArgoCD instance
- golangci-lint (for development)

### Building

```bash
# Build the binary
make build

# Or using go directly
go build -o argazer .
```

### Development Workflow

```bash
# Install git hooks (runs linter and tests before push)
make install-hooks

# Run tests
make test

# Run linter
make lint

# Clean build artifacts
make clean
```

The pre-push hook will automatically run linter and tests before each push. To bypass (not recommended):
```bash
git push --no-verify
```


## CI/CD Integration

### GitHub Actions

To run Argazer on a schedule (e.g., check for updates every 6 hours):

```yaml
name: Check Helm Updates
on:
  schedule:
    - cron: '0 */6 * * *'  # Every 6 hours
  workflow_dispatch:  # Allow manual trigger

jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - name: Run Argazer
        uses: docker://ghcr.io/<owner>/<repo>:<version>
        env:
          AG_ARGOCD_URL: ${{ secrets.ARGOCD_URL }}
          AG_ARGOCD_USERNAME: ${{ secrets.ARGOCD_USERNAME }}
          AG_ARGOCD_PASSWORD: ${{ secrets.ARGOCD_PASSWORD }}
          AG_NOTIFICATION_CHANNEL: telegram
          AG_TELEGRAM_WEBHOOK: ${{ secrets.TELEGRAM_WEBHOOK }}
          AG_TELEGRAM_CHAT_ID: ${{ secrets.TELEGRAM_CHAT_ID }}
          AG_PROJECTS: production
          AG_LABELS: type=operator
```

### GitLab CI

```yaml
argazer-check:
  stage: check
  image: ghcr.io/kreicer/argazer:latest
  script:
    - argazer
  variables:
    AG_ARGOCD_URL: ${ARGOCD_URL}
    AG_ARGOCD_USERNAME: ${ARGOCD_USERNAME}
    AG_ARGOCD_PASSWORD: ${ARGOCD_PASSWORD}
    AG_NOTIFICATION_CHANNEL: telegram
    AG_TELEGRAM_WEBHOOK: ${TELEGRAM_WEBHOOK}
    AG_TELEGRAM_CHAT_ID: ${TELEGRAM_CHAT_ID}
  only:
    - schedules
```

## Troubleshooting

### Connection Issues

If you're having trouble connecting to ArgoCD:

1. Verify the URL format is correct (use `argocd.example.com`, not `https://argocd.example.com`)
2. Check username/password credentials
3. Try with `--argocd-insecure=true` for self-signed certificates
4. Use `--verbose` flag for detailed JSON logging

### No Applications Found

If Argazer reports 0 applications:

1. Check your project and app name filters
2. Verify label selectors if using them (format: `AG_LABELS=key1=value1,key2=value2`)
3. Ensure your user has permission to list applications
4. Try with `--projects="*" --app-names="*"` and without label filters to check all apps
5. Verify labels exist on your ArgoCD applications (check in ArgoCD UI)

### Email Not Sending

For email notification issues:

1. Verify SMTP settings are correct
2. Check if you need an "app password" for Gmail
3. Ensure firewall allows SMTP traffic
4. Try with `email_use_tls: false` if needed

### Applications Skipped

If applications are being skipped with "OCI/container registry" messages:

1. This is expected behavior - OCI repositories don't support traditional Helm index.yaml files
2. Argazer can only check traditional Helm repositories (HTTP/HTTPS with index.yaml)
3. For OCI repositories, version checking must be done differently (not currently supported)
4. These skipped applications won't affect the update notifications for other apps

### Multi-Source Applications

For applications with multiple Helm sources:

1. Set `source_name` in config to match the specific source name you want to check
2. Default is `"chart-repo"` - change if your sources use different names
3. If no matching source name is found, Argazer will use the first Helm source it finds
4. Use `--verbose` to see which sources are being checked

## License

GPL-3.0 license

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.
