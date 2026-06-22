![Claude Assisted](https://img.shields.io/badge/Made%20with-Claude-8A2BE2?logo=anthropic)
![CI](https://github.com/k-krew/argazer/actions/workflows/ci.yml/badge.svg)
[![codecov](https://codecov.io/gh/kreicer/argazer/branch/main/graph/badge.svg)](https://codecov.io/gh/kreicer/argazer)

# Argazer

**Argazer** (a wordplay on "Argo" and "gazer") is a lightweight tool that monitors your ArgoCD applications for Helm chart updates. It connects to ArgoCD via API, scans your applications across multiple repository types (Git, OCI, HTTP), and notifies you when newer versions are available.

## Features

- **Single-run execution** - Runs once on launch, perfect for CI/CD or cron jobs.
- **Multiple repository types** - Native support for Traditional HTTP Helm repos, OCI Registries, and Git Repositories.
- **Interactive configuration** - Run `argazer configure` for a step-by-step setup wizard.
- **Flexible output formats** - Table (human-readable), JSON (programmatic), or Markdown (documentation).
- **Controllable verbosity** - Adjust output noise using the `--verbosity` flag (`normal`, `full`, or `off`).
- **Flexible filtering** - Filter by projects, application names, and labels.
- **Semantic version constraints** - Only notify on `patch`, `minor`, or `major` updates.
- **Multiple notification channels** - Telegram, Email, Slack, Microsoft Teams, or Generic Webhooks.
- **Graceful error handling & retries** - Reliable notifications with exponential backoff on network failures.

## Installation

### Using Docker

Multi-architecture images are available for **AMD64** and **ARM64**:

```bash
# Pull the latest image
docker pull ghcr.io/kreicer/argazer:latest

# Or a specific version
docker pull ghcr.io/kreicer/argazer:v1.1.0
```

### From Source

```bash
git clone git@github.com:kreicer/argazer.git
cd argazer
go build -o argazer .
```

*(Note: Homebrew support is coming soon!)*

## Quick Start

The easiest way to get started is with the interactive configuration wizard:

```bash
./argazer configure
```

This will guide you through connecting to ArgoCD, selecting filters, configuring notifications, and saving everything to `config.yaml`.

## Usage

### Basic Execution

```bash
# Run with config file
./argazer --config config.yaml

# Run with environment variables
AG_ARGOCD_URL="argocd.example.com" AG_ARGOCD_USERNAME="admin" AG_ARGOCD_PASSWORD="password" ./argazer

# Run with flags
./argazer --argocd-url="argocd.example.com" --argocd-username="admin" --argocd-password="password"
```

### Output and Verbosity Control

Argazer allows you to control both the format of the results and the verbosity of its operational logs.

```bash
# Change output format (table, json, markdown)
./argazer --output-format="json"

# Control operational log verbosity (normal, full, off)
./argazer --verbosity="off"  # Mutes logs, prints only the final scan results
./argazer --verbosity="full" # Enables debug logging

# Change log format for integration (json, text)
./argazer --log-format="text"
```

### Version Constraints

Control which updates trigger notifications based on semantic versioning:

```bash
# Check all versions (default)
./argazer --version-constraint="major"

# Only check for minor or patch updates (e.g. 1.2.3 -> 1.3.0)
./argazer --version-constraint="minor"

# Only check for patch updates (e.g. 1.2.3 -> 1.2.5)
./argazer --version-constraint="patch"
```

## Configuration

Argazer can be configured via a `config.yaml` file, CLI flags, or environment variables (`AG_` prefix).

> **Note:** Full configuration examples can be found in the `examples/` directory:
> - [`examples/config.yaml`](examples/config.yaml)
> - [`examples/.env.example`](examples/.env.example)

<details>
<summary><b>Click to expand full configuration examples</b></summary>

### `config.yaml` Example
```yaml
argocd_url: "argocd.example.com"
argocd_username: "admin"
argocd_password: "your-password"
argocd_insecure: false

projects: ["*"]
app_names: ["*"]
labels:
  type: "operator"

notification_channel: "telegram"
telegram_webhook: "https://api.telegram.org/botTOKEN/sendMessage"
telegram_chat_id: "123456789"

verbosity: "normal"
version_constraint: "major"
output_format: "table"
```

### Environment Variables
```bash
export AG_ARGOCD_URL="argocd.example.com"
export AG_ARGOCD_USERNAME="admin"
export AG_ARGOCD_PASSWORD="your-password"
export AG_PROJECTS="production,staging"
export AG_LABELS="type=operator,environment=production"
export AG_OUTPUT_FORMAT="table"
export AG_VERBOSITY="normal"
```
</details>

## Authentication for Private Repositories

> **⚠️ SECURITY WARNING**  
> **Do NOT store credentials in plain text config files! ALWAYS use environment variables for credentials in production.**

Argazer supports authentication for Git, OCI, and HTTP Helm repositories. Provide credentials using environment variables:

```bash
# Example: GitHub Container Registry (OCI)
export AG_AUTH_URL_GHCR="ghcr.io"
export AG_AUTH_USER_GHCR="github-user"
export AG_AUTH_PASS_GHCR="ghp_token"

# Example: Private Git Repository
export AG_AUTH_URL_GIT="github.com"
export AG_AUTH_USER_GIT="git-user"
export AG_AUTH_PASS_GIT="git-token"
```

## Supported Repository Types

Argazer automatically detects the repository type based on the URL in ArgoCD:

1. **Git Repositories**: Detects `.git` URLs (e.g., `https://github.com/myorg/helm-charts.git`). Reads versions directly from git tags.
2. **OCI Registries**: Detects registries without `http://` or `https://` (e.g., `ghcr.io/myorg/charts`).
3. **Traditional Helm**: Classic HTTP-based repositories with an `index.yaml`.

## ArgoCD RBAC Setup

Argazer requires minimal read-only permissions in ArgoCD. Create a dedicated user with the following RBAC policy:

```yaml
# argocd-rbac-cm ConfigMap
p, role:argazer-reader, applications, get, */*, allow
p, role:argazer-reader, applications, list, */*, allow
g, argazer, role:argazer-reader
```

## Troubleshooting

- **No Applications Found**: Verify your `projects`, `app_names`, and `labels` filters. Ensure the ArgoCD user has RBAC permissions to list applications.
- **Connection Issues**: Ensure `argocd_url` does not contain the `https://` prefix (e.g., use `argocd.example.com`). Try setting `argocd_insecure: true` if using self-signed certificates.
- **Seeing too much output?**: Use `--verbosity="off"` to hide operational logs and only display the final scan results.

## License & Contributing

Apache 2.0 license. Contributions are welcome! Please feel free to submit a Pull Request.
