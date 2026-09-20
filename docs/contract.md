# Contract

How Argazer decides that an application needs attention, what it does with that decision, and the exact shape of its machine-readable output.

- [Semver logic](#semver-logic)
- [Notify-on](#notify-on)
- [Exit codes](#exit-codes)
- [Fail-on](#fail-on)
- [JSON contract](#json-contract)

## Semver logic

Argazer classifies each application by comparing its `targetRevision` with the versions the chart repository offers. Only updates that require editing the Application manifest are reported as updates (`has_update: true`).

| `targetRevision` | Newer version available | `update_type` | `has_update` | Required action |
|------------------|-------------------------|---------------|--------------|-----------------|
| `1.2.3` (pinned) | `1.3.0` | `pinned` | `true` | Bump `targetRevision` to `latest_version` |
| `~1.2.0` (range) | `1.2.5`, the newest version published and inside the range | `in-range` | `false` | None — ArgoCD deploys `1.2.5` on its own |
| `~1.2.0` (range) | `1.2.5` inside the range, `2.0.1` above it | `out-of-range` | `true` | Widen the range to reach `latest_version_all`; until then ArgoCD keeps deploying `1.2.5` |
| `~1.2.0` (range) | `1.3.0`, above the range, nothing inside it | `out-of-range` | `true` | Widen the range up to `latest_version_all` |
| `main` (non-semver revision) | `1.3.0` | `pinned` | `true` | Manual change; bump severity is unknown |
| any | none | `none` | `false` | None |

A range is `in-range` only when no published version sits above it. As soon as one does, the type is `out-of-range` and `has_update` is `true`, whether or not the range still matches something — the newest matching version is reported in `latest_version` and the one the range has to be widened to in `latest_version_all`.

Supported range syntax is that of [Masterminds/semver](https://github.com/Masterminds/semver): `~1.2.0`, `^1.2.0`, `1.2.*`, `>=1.2.0, <1.4.0`.

Edge cases:

- If a range matches nothing but a newer version exists above it (`~1.2.0` with only `1.3.0` published), the type is `out-of-range` and `latest_version` repeats the range expression.
- If a range matches nothing and every available version is below it (`~3.0.0` with only `1.x` published), the type is `none`.
- A `targetRevision` that is neither a version nor a range (a branch, tag or commit SHA) is treated as pinned: ArgoCD will never move it on its own, but the size of the bump cannot be computed.

## Notify-on

`--notify-on` narrows which newer versions count as an in-policy update **for a pinned `targetRevision`**:

| Value | In-policy versions for `1.2.3` |
|-------|--------------------------------|
| `major` (default) | any newer version |
| `minor` | same major (`1.3.0`, not `2.0.0`) |
| `patch` | same major and minor (`1.2.5`, not `1.3.0`) |

For a range, the range itself is the policy and `--notify-on` is not applied. When an application is in policy but a newer version exists outside it, `has_update_outside_constraint` is set and the version is still shown in the report.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Scan completed successfully, and no updates crossed the `--fail-on` threshold |
| `1` | The scan could not be completed: ArgoCD unreachable, invalid configuration, or at least one application could not be checked |
| `2` | Updates matching `--fail-on` were found |

Code `1` takes precedence over code `2`: if any application ends up in `errors`, the run exits `1` regardless of the updates found.

## Fail-on

`--fail-on` reacts to the given severity **and above**:

| Value | Exits `2` on |
|-------|--------------|
| `none` (default) | never |
| `any` | any update with `has_update: true`, including ones of unknown severity |
| `patch` | patch, minor and major updates |
| `minor` | minor and major updates |
| `major` | major updates only |

Severity is measured between `current_version` and the version the application would end up on: `latest_version_all` for `out-of-range`, `latest_version` otherwise. A range is represented by its lowest possible version, so `~1.2.0` → `2.0.0` is a major bump. `in-range` updates never affect the exit code, and updates whose severity cannot be computed only count for `--fail-on=any`.

## JSON contract

`--output-format=json` writes the full report to stdout. Combine it with `--verbosity=off` to keep operational logs out of the stream:

```bash
argazer --output-format="json" --verbosity="off"
```

```json
{
  "summary": {
    "total": 42,
    "up_to_date": 40,
    "updates_available": 2,
    "skipped": 0
  },
  "updates_available": [
    {
      "app_name": "nginx",
      "project": "production",
      "chart_name": "nginx",
      "current_version": "~1.2.0",
      "latest_version": "1.2.5",
      "latest_version_all": "2.0.1",
      "repo_url": "https://charts.example.com",
      "has_update": true,
      "update_type": "out-of-range",
      "constraint_applied": "minor",
      "has_update_outside_constraint": true
    },
    {
      "app_name": "redis",
      "project": "production",
      "chart_name": "redis",
      "current_version": "4.1.0",
      "latest_version": "4.3.2",
      "latest_version_all": "5.0.0",
      "repo_url": "https://charts.example.com",
      "has_update": true,
      "update_type": "pinned",
      "constraint_applied": "minor",
      "has_update_outside_constraint": true
    }
  ],
  "up_to_date_with_constraint": null,
  "up_to_date": null,
  "errors": null
}
```

### Top-level fields

| Field | Contents |
|-------|----------|
| `summary.total` | Helm applications checked. Applications without a Helm source are not counted |
| `summary.up_to_date` | Applications requiring no manifest change, `in-range` ones included |
| `summary.updates_available` | Applications requiring a manifest change |
| `summary.skipped` | Applications that could not be checked |
| `updates_available` | Applications with `has_update: true` (`pinned` or `out-of-range`) |
| `up_to_date_with_constraint` | Pinned applications only: a newer version exists but does not fit `--notify-on`, so `has_update_outside_constraint` is `true` while no manifest change is required. A range never lands here — a version above it makes the application `out-of-range`, i.e. an update |
| `up_to_date` | Nothing newer to report at all |
| `errors` | Applications that could not be checked; `error` holds the reason |

An empty list is serialized as `null`, not `[]`.

### Application fields

| Field | Type | Contents |
|-------|------|----------|
| `app_name` | string | ArgoCD Application name |
| `project` | string | ArgoCD project of the application |
| `chart_name` | string | Chart name, or the path inside the repository for a chart kept in Git |
| `current_version` | string | `targetRevision` as written in the manifest: a pinned version, a range, or a Git revision |
| `latest_version` | string | Newest version allowed by the current policy — for a range, the newest version satisfying it; for a pinned revision, the newest version within `--notify-on`. Repeats `current_version` when nothing qualifies |
| `latest_version_all` | string | Newest version in the repository, ignoring both the range and `--notify-on`. Omitted when the check failed |
| `repo_url` | string | Chart repository as registered in the Application |
| `has_update` | bool | `true` when the manifest has to be edited |
| `update_type` | string | `none`, `in-range`, `out-of-range` or `pinned` |
| `constraint_applied` | string | The `--notify-on` value of this run; identical for every application |
| `has_update_outside_constraint` | bool | `true` when a newer version exists beyond the range or beyond `--notify-on` |
| `error` | string | Why the application could not be checked. Present only in `errors` |

### Which field to bump to

Which field is the bump target depends on `update_type`:

- `pinned` (`redis` above): `latest_version` is the bump target. `4.3.2` is the newest version within `--notify-on=minor`; `5.0.0` exists but crosses the major boundary, hence `has_update_outside_constraint: true`.
- `out-of-range` (`nginx` above): `latest_version` is **not** a bump target. `1.2.5` satisfies `~1.2.0`, so ArgoCD deploys it without any change to the manifest. A manual bump means widening the range to reach `latest_version_all`, `2.0.1`.
- `in-range`: no field is a bump target; the application appears under `up_to_date`.
