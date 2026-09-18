![Claude Assisted](https://img.shields.io/badge/Made%20with-Claude-8A2BE2?logo=anthropic)
![CI](https://github.com/k-krew/argazer/actions/workflows/ci.yml/badge.svg)
[![codecov](https://codecov.io/gh/kreicer/argazer/branch/main/graph/badge.svg)](https://codecov.io/gh/kreicer/argazer)

# Argazer

**Argazer** (a wordplay on "Argo" and "gazer") is a lightweight tool that monitors your ArgoCD applications for Helm chart updates. It connects to ArgoCD via API, scans your applications across multiple repository types (Git, OCI, HTTP), and notifies you when newer versions are available.

![Argazer Demo](assets/demo.gif)

## Features

- **Single-run execution** - Runs once on launch, perfect for CI/CD or cron jobs.
- **Multiple repository types** - Native support for Traditional HTTP Helm repos, OCI Registries, and Git Repositories.
- **Interactive configuration** - Run `argazer configure` for a step-by-step setup wizard.
- **Flexible output formats** - Table (human-readable), JSON (programmatic), or Markdown (documentation).
- **Controllable verbosity** - Adjust output noise using the `--verbosity` flag (`normal`, `full`, or `off`).
- **Flexible filtering** - Filter by projects, application names, and labels.
- **Semantic version constraints** - Only notify on `patch`, `minor`, or `major` updates.
- **CI/CD quality gate** - Granular exit codes plus `--fail-on` to fail a pipeline on the updates you care about.
- **Multiple notification channels** - Telegram, Email, Slack, Microsoft Teams, or Generic Webhooks.
- **Graceful error handling & retries** - Reliable notifications with exponential backoff on network failures.

## Installation

### Homebrew (macOS / Linux)

```bash
brew tap k-krew/tap https://github.com/k-krew/homebrew-tap.git
brew trust k-krew/tap
brew install --cask argazer
```

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

### Exit Codes and CI/CD Gating

Argazer reports the outcome of a scan through its exit code:

| Code | Meaning |
|------|---------|
| `0` | Nothing to report |
| `1` | The scan could not be completed (ArgoCD unreachable, bad configuration, or an application that could not be checked) |
| `2` | Updates matching `--fail-on` were found |

By default (`--fail-on=none`) updates are only reported, which is what you want for a CronJob. Use `--fail-on` to turn a pipeline step into a quality gate; it reacts to updates of the given severity **and above**:

```bash
# Fail the pipeline on any update that requires a manifest change
./argazer --fail-on="any"

# Fail only on major updates (e.g. 1.2.3 -> 2.0.0)
./argazer --fail-on="major"

# Fail on minor and major updates, ignore patches
./argazer --fail-on="minor"
```

Severity is measured against the version an application would move to, so for a `targetRevision` range it is the newest version beyond that range. Updates ArgoCD applies on its own (a newer version inside the range) never fail the run, and neither do updates whose severity cannot be determined, e.g. for a `targetRevision` pointing at a Git branch — those only count for `--fail-on=any`.

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
fail_on: "none"
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

## Private Repositories

Argazer never needs the credentials of your Helm repositories. Versions of traditional Helm repositories are read through the ArgoCD API, so ArgoCD reaches the repository with the credentials it already stores and hands Argazer the resulting list of chart versions. Any private Helm repository already registered in ArgoCD works without extra configuration.

## Supported Repository Types

Argazer automatically detects the repository type based on the URL in ArgoCD:

1. **Git Repositories**: Detects `.git` URLs (e.g., `https://github.com/myorg/helm-charts.git`). Reads versions directly from git tags. Only public repositories are supported.
2. **OCI Registries**: Detects registries without `http://` or `https://` (e.g., `ghcr.io/myorg/charts`). Read anonymously from the registry, so the registry has to allow anonymous tag listing.
3. **Traditional Helm**: Classic HTTP-based repositories, read through the ArgoCD API. Private repositories are supported.

## ArgoCD RBAC Setup

Argazer requires minimal read-only permissions in ArgoCD. Create a dedicated user with the following RBAC policy:

```yaml
# argocd-rbac-cm ConfigMap
p, role:argazer-reader, applications, get, */*, allow
p, role:argazer-reader, applications, list, */*, allow
p, role:argazer-reader, repositories, get, *, allow
g, argazer, role:argazer-reader
```

## Troubleshooting

- **No Applications Found**: Verify your `projects`, `app_names`, and `labels` filters. Ensure the ArgoCD user has RBAC permissions to list applications.
- **Chart Not Found**: Chart versions of Helm repositories come from ArgoCD, so the repository has to be registered in ArgoCD and the Argazer user needs `repositories, get` permission on it.
- **Connection Issues**: Ensure `argocd_url` does not contain the `https://` prefix (e.g., use `argocd.example.com`). Try setting `argocd_insecure: true` if using self-signed certificates.
- **Seeing too much output?**: Use `--verbosity="off"` to hide operational logs and only display the final scan results.

## License & Contributing

Apache 2.0 license. Contributions are welcome! Please feel free to submit a Pull Request.
