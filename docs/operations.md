# Operations

What Argazer can read, how it behaves during a run, how to schedule it in a cluster, and what to check when a run does not do what you expect.

- [Repository types](#repository-types)
- [Multi-source applications](#multi-source-applications)
- [Concurrency and caching](#concurrency-and-caching)
- [GitHub Actions](#github-actions)
- [Kubernetes CronJob](#kubernetes-cronjob)
- [Troubleshooting](#troubleshooting)

## Repository types

The repository type is derived from the `repoURL` in the Application, in the order below. Versions are always fetched through the ArgoCD API.

| Type | Detected by | Version source |
|------|-------------|----------------|
| Git | A `.git` suffix, a `git@` prefix, or an HTTP(S) URL on `github.com`, `gitlab.com`, `bitbucket.org` or a `gitea` host | Repository tags: `nginx-1.2.3` counts as a version of chart `nginx`; a single-chart repository can tag plainly (`v1.2.3`, `release-1.2.3`) |
| OCI | An `oci://` prefix, or any URL without an HTTP(S) scheme (`ghcr.io/myorg/charts`) | Tags of the artifact holding the chart. **Requires ArgoCD 3.1+** |
| Helm HTTP | Everything else, i.e. a plain HTTP(S) URL (`https://charts.example.com`) | Chart list of the repository |

Tags and chart versions that are not valid semver are ignored. A repository that is not registered in ArgoCD, or that the Argazer account cannot read, cannot be checked and lands in `errors`.

## Multi-source applications

A multi-source application is reported one result per chart it holds: every Helm source is checked, in the order the sources are listed. `source_name` is an optional filter on top of that — it is unset by default, and when it is given and a Helm source carries that name, only that source is checked; when no source matches, the application is checked as if the setting had not been given.

A source that has a `ref` and no chart of its own (the `ref: values` pattern) holds the values files the other sources point at. ArgoCD renders nothing from it and it has no chart version to look up, so it is always ignored. A source counts as a chart of its own when it names a `chart`, and also when its `repoURL` starts with `oci://`, since ArgoCD lets an OCI chart be spelled either as a registry path plus a `chart` or as a single `repoURL` naming the chart already.

A chart kept in a Git repository is named by `path` instead of `chart`, which is exactly how a Kustomize or plain-manifest source looks, so the path alone settles nothing. Argazer goes by `status.sourceType` (`status.sourceTypes` for a multi-source application), the tool ArgoCD says it renders each source with: only a source recorded as `Helm` is checked for chart versions, and its versions come from the tags of the repository. An application ArgoCD has not processed yet carries no status, in which case only `helm` options on the source mark it as a chart.

## Concurrency and caching

`--concurrency` (default `10`) sets how many applications are checked in parallel. Version lists are cached in memory per repository and project for the duration of a run, so applications sharing a chart repository result in a single API request; concurrent workers asking for the same repository wait for that request instead of duplicating it. ArgoCD calls are retried with backoff.

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

## Kubernetes CronJob

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: argazer
  namespace: argocd
spec:
  schedule: "0 7 * * 1" # Every Monday morning
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: Never
          containers:
            - name: argazer
              image: ghcr.io/kreicer/argazer:latest
              args:
                - --argocd-url=argocd-server.argocd.svc
                - --argocd-insecure # In-cluster ArgoCD usually serves its own certificate
                - --notification-channel=slack
              env:
                - name: ARGOCD_AUTH_TOKEN
                  valueFrom:
                    secretKeyRef:
                      name: argazer
                      key: argocd-auth-token
                - name: AG_SLACK_WEBHOOK
                  valueFrom:
                    secretKeyRef:
                      name: argazer
                      key: slack-webhook
```

With the default `--fail-on=none` the Job fails only when the scan itself could not be completed.

## Troubleshooting

- **No applications found** — check the `projects`, `app_names` and `labels` filters and that the account may list applications.
- **Chart not found** — the repository has to be registered in ArgoCD and the account needs `repositories, get` on it.
- **OCI tags require ArgoCD 3.1** — listing OCI tags uses an API endpoint added in ArgoCD 3.1. Older servers answer `404`, the affected applications are counted as skipped and the run exits `1`.
- **A private OCI registry answers 401** — ArgoCD matches repository credentials by full URL. A repository registered as `oci://ghcr.io/myorg/nginx` covers applications whose `repoURL` is spelled the same way, while `repoURL: ghcr.io/myorg/charts` with `chart: nginx` is resolved as `ghcr.io/myorg/charts/nginx`. Use a [repository credential template](https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/#repository-credentials) covering the registry.
- **TLS errors** — an in-cluster ArgoCD usually serves its own certificate; pass `--argocd-insecure` or use a trusted endpoint.
- **Exit code 1 without an obvious error** — an application could not be checked. Inspect the skipped section of the report or run with `--verbosity=full`.
