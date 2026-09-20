# Configuration

Every input of a run: where the settings come from, how Argazer authenticates against ArgoCD, which applications it looks at, and where the report goes.

- [Sources of configuration](#sources-of-configuration)
- [Flags, environment variables and config keys](#flags-environment-variables-and-config-keys)
- [Authentication](#authentication)
- [ArgoCD RBAC](#argocd-rbac)
- [Filtering](#filtering)
- [Output formats](#output-formats)
- [Notifications](#notifications)

## Sources of configuration

Sources, in order of precedence: CLI flags → environment variables (`AG_` prefix) → config file → defaults. A config file is read from `--config`, or, if that flag is absent, from the first match among `./config.yaml`, `/etc/argazer/config.yaml` and `$HOME/.argazer/config.yaml`.

Invalid enum values and missing required fields fail at startup with exit code `1`. Full examples: [`examples/config.yaml`](../examples/config.yaml), [`examples/.env.example`](../examples/.env.example).

`argazer version` prints the version and build commit.

## Flags, environment variables and config keys

| Flag | Config key / env | Default | Meaning |
|------|------------------|---------|---------|
| `--argocd-url` | `argocd_url` / `AG_ARGOCD_URL` | — | ArgoCD address; `https://` is assumed when no scheme is given. Required |
| `--argocd-auth-token` | `argocd_auth_token` / `ARGOCD_AUTH_TOKEN`, `AG_ARGOCD_AUTH_TOKEN` | — | API token; takes precedence over username and password |
| `--argocd-username` | `argocd_username` / `AG_ARGOCD_USERNAME` | — | Username; required when no token is set |
| `--argocd-password` | `argocd_password` / `AG_ARGOCD_PASSWORD` | — | Password; required when no token is set |
| `--argocd-insecure` | `argocd_insecure` / `AG_ARGOCD_INSECURE` | `false` | Skip TLS verification |
| `--projects` | `projects` / `AG_PROJECTS` | `*` | Comma-separated list of projects to check |
| `--app-names` | `app_names` / `AG_APP_NAMES` | `*` | Comma-separated list of applications to check |
| — | `labels` / `AG_LABELS` | — | Label filter as `key=value` pairs, comma-separated |
| — | `source_name` / `AG_SOURCE_NAME` | — | Optional filter for multi-source applications: when a Helm source carries this name, only that source is checked; otherwise all Helm sources are |
| `--notify-on` | `notify_on` / `AG_NOTIFY_ON` | `major` | Widest in-policy bump for a pinned revision: `major`, `minor`, `patch` |
| `--fail-on` | `fail_on` / `AG_FAIL_ON` | `none` | Severity that makes the run exit `2`: `none`, `any`, `patch`, `minor`, `major` |
| `--output-format`, `-o` | `output_format` / `AG_OUTPUT_FORMAT` | `table` | `table`, `json`, `markdown` |
| `--verbosity`, `-v` | `verbosity` / `AG_VERBOSITY` | `off` | `normal` (info), `full` (debug), `off` (results only) |
| `--log-format`, `-l` | `log_format` / `AG_LOG_FORMAT` | `json` | `json`, `text` |
| `--concurrency` | `concurrency` / `AG_CONCURRENCY` | `10` | Applications checked in parallel |
| `--notification-channel` | `notification_channel` / `AG_NOTIFICATION_CHANNEL` | — | `slack`, `webhook`, or empty for console only |
| — | `slack_webhook` / `AG_SLACK_WEBHOOK` | — | Slack webhook URL; required for `--notification-channel=slack` |
| — | `webhook_url` / `AG_WEBHOOK_URL` | — | Target URL; required for `--notification-channel=webhook` |
| `--config`, `-c` | — | — | Path to a config file |

## Authentication

Argazer authenticates with an ArgoCD API token or with a username and password. A token takes precedence when both are set. A whitespace-only token counts as unset.

```bash
# Token as a flag
argazer --argocd-url="argocd.example.com" --argocd-auth-token="eyJhbGciOi..."

# Token from the environment. ARGOCD_AUTH_TOKEN (the variable the ArgoCD CLI uses)
# and AG_ARGOCD_AUTH_TOKEN are both accepted.
export ARGOCD_AUTH_TOKEN="eyJhbGciOi..."
argazer --argocd-url="argocd.example.com"
```

Generate a token for a local ArgoCD account:

```bash
argocd account generate-token --account argazer
```

## ArgoCD RBAC

Read-only permissions are sufficient:

```yaml
# argocd-rbac-cm ConfigMap
p, role:argazer-reader, applications, get, */*, allow
p, role:argazer-reader, applications, list, */*, allow
p, role:argazer-reader, repositories, get, *, allow
g, argazer, role:argazer-reader
```

Chart repository credentials are never needed: version lists are fetched through the ArgoCD API, so ArgoCD uses the credentials it already stores. Project-scoped repositories work as well — the `AppProject` of the application is passed with every request.

## Filtering

```bash
# By project
argazer --projects="production,staging"

# By application name
argazer --app-names="nginx,redis"

# By label — config file or environment only, no flag
AG_LABELS="type=operator,environment=production" argazer
```

## Output formats

Results go to stdout in the selected format; operational logs are separate and structured, and `--verbosity=off` silences them.

| Format | Use |
|--------|-----|
| `table` (default) | terminal |
| `markdown` | pipeline summaries, e.g. `$GITHUB_STEP_SUMMARY` |
| `json` | machine-readable contract for other tools; see [contract.md](contract.md#json-contract) |

## Notifications

Without `--notification-channel` the report only goes to stdout. A channel is used only when there is something to report, and a failed notification does not change the exit code.

```bash
# Slack
AG_SLACK_WEBHOOK="https://hooks.slack.com/services/..." argazer --notification-channel="slack"

# Generic webhook: JSON payload with "subject" and "message"
AG_WEBHOOK_URL="https://example.com/notify" argazer --notification-channel="webhook"
```
