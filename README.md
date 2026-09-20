![CI](https://github.com/k-krew/argazer/actions/workflows/ci.yml/badge.svg)
[![codecov](https://codecov.io/gh/kreicer/argazer/branch/main/graph/badge.svg)](https://codecov.io/gh/kreicer/argazer)

# Argazer

Argazer is a stateless, read-only Helm chart auditor for ArgoCD. One run lists Applications through the ArgoCD API, resolves the available chart versions through the same API, and reports the applications whose `targetRevision` has to be changed by hand. The result of a run is a report and an exit code — nothing is written to the cluster and no state is kept between runs.

![Argazer Demo](assets/demo.gif)

## Argazer vs Renovate

Renovate and Dependabot read files in Git and open pull requests. Argazer reads live Application specs through the ArgoCD API and reports — it needs no write access, no chart repository credentials and no state, and it knows that a `targetRevision` range is resolved by ArgoCD itself, so it stays quiet about `in-range` updates. Deciding how to bump a chart is left to you. The two are complementary: Renovate proposes changes in the repositories it is pointed at, Argazer audits what the fleet is actually running.

## Installation

```bash
# Homebrew (macOS / Linux)
brew tap k-krew/tap https://github.com/k-krew/homebrew-tap.git
brew trust k-krew/tap
brew install --cask argazer

# Docker (AMD64 and ARM64)
docker pull ghcr.io/kreicer/argazer:latest

# From source, Go 1.25+
git clone git@github.com:kreicer/argazer.git && cd argazer && go build -o argazer .
```

## Quick Start

An ArgoCD URL and a read-only token are the only required inputs:

```bash
export ARGOCD_AUTH_TOKEN="eyJhbGciOi..."
argazer --argocd-url="argocd.example.com"
```

This prints a table and exits `0`. Add `--fail-on` to make the run a pipeline gate:

```bash
argazer --argocd-url="argocd.example.com" --fail-on="minor"
```

Argazer uses exit codes (`0`, `1`, `2`) to fail your CI/CD pipeline when updates are found. See [docs/contract.md](docs/contract.md#exit-codes) for details.

## GitHub Actions

```yaml
- name: Audit Helm charts
  env:
    ARGOCD_AUTH_TOKEN: ${{ secrets.ARGOCD_AUTH_TOKEN }}
  run: |
    argazer --argocd-url="argocd.example.com" \
            --projects="production" \
            --fail-on="major" \
            --output-format="markdown" >> "$GITHUB_STEP_SUMMARY"
```

The report lands in the job summary; the step fails only on a major update or an incomplete scan.

## Documentation

- [docs/contract.md](docs/contract.md) — semver logic (pinned vs range), `--notify-on`, `--fail-on`, exit codes and the full JSON contract.
- [docs/configuration.md](docs/configuration.md) — flags, environment variables and config file, authentication, ArgoCD RBAC, filtering, output formats and notifications.
- [docs/operations.md](docs/operations.md) — repository types (Helm HTTP, Git, OCI), multi-source applications, concurrency and caching, Kubernetes CronJob and troubleshooting.

## License & Contributing

Apache 2.0. Pull requests are welcome.
