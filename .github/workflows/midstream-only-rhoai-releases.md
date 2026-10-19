# RHOAI release-branch provisioner

MCPLO owns the reusable workflow and Python implementation. Its scheduled
(every two hours) and manual runs retain the MCPLO defaults. Runs are limited
to the `opendatahub-io` organization, so copies synced downstream do not run.
The target checkout always uses `main`.

## Thin MCPLMO caller

Add `.github/workflows/midstream-only-rhoai-releases.yml` in MCPLMO **after**
its operator and E2E Tekton templates are ready:

```yaml
name: Provision RHOAI release branches
on:
  schedule:
    - cron: '17 */2 * * *'
  workflow_dispatch:
permissions:
  contents: write
jobs:
  provision:
    uses: opendatahub-io/mcp-lifecycle-operator/.github/workflows/midstream-only-rhoai-releases.yml@main
    with:
      downstream-repository: red-hat-data-services/mcp-lifecycle-module-operator
      manifest-prefix: odh-mcp-lifecycle-module-operator
      image-repository: quay.io/opendatahub/odh-mcp-lifecycle-module-operator
      excluded-branches: |
        rhoai-3.5
        rhoai-3.6-ea.1
        rhoai-3.6-ea.2
```

The caller must grant `contents: write`; a reusable workflow cannot elevate
its permissions. By default, target writes use the caller's `github.token`.
If branch creation must trigger other GitHub Actions workflows (which writes
using `GITHUB_TOKEN` normally do not), explicitly pass an appropriately scoped
App token via `secrets: {target-token: "${{ secrets.RELEASE_BRANCH_TOKEN }}"}`.
The workflow does not create an App token itself. Neither checkout persists
credentials, and the provider checkout never receives the forwarded token.

## Inputs and template contract

Defaults live only in the Python implementation. The workflow forwards inputs
unchanged; omitted or empty values use the defaults below.

| Input | MCPLO default | Derived values |
| --- | --- | --- |
| `downstream-repository` | `red-hat-data-services/mcp-lifecycle-operator` | GitHub Git URL |
| `manifest-prefix` | `odh-mcp-lifecycle-operator` | Operator and `<prefix>-e2e` Tekton filenames |
| `image-repository` | `quay.io/opendatahub/odh-mcp-lifecycle-operator` | E2E image repository adds `-e2e` |
| `excluded-branches` | Empty (none) | Newline- or comma-delimited exact names; no version comparisons |

Use a YAML multiline string as shown above, or a comma-delimited list.
Surrounding whitespace and empty entries are ignored.

There is no target-repository or provider-ref input. The target is the caller's
`github.repository`, checked out at `main` under `target`. The implementation
is checked out under `provisioner` at `job.workflow_repository` and
`job.workflow_sha`, the exact commit supplying the workflow even with `@main`.
Concurrency is scoped to the caller repository.

Required `.tekton` templates are `<prefix>-{push,pull-request,release-push}.yaml`
and `<prefix>-e2e-{push,pull-request,release-push}.yaml`. They must match the
validated MCPLO template contract: main/PR CEL filters, release CEL predicate,
target-branch annotation, output images, and PR revision tags. With exclusions,
the release CEL predicate must append this exact restrictive clause (sorted
names, JSON array formatting), which is preserved without rewriting:

```text
 && !(target_branch in ["rhoai-3.5", "rhoai-3.6-ea.1", "rhoai-3.6-ea.2"])
```

All templates are validated before any rewritten files are written. Unexpected
YAML or missing/malformed templates abort provisioning.

Downstream release names matching `rhoai-N.N` or `rhoai-N.N-ea.N` are mirrored
exactly, except exclusions. Existing target branches are skipped, never reset
or repaired. Each missing branch starts from **target main**, not downstream
product code, with only PR filters retargeted to the new branch. Ordinary push
templates remain main-only. Existing release-push templates handle release
builds with `{{target_branch}}` image tags, without publishing `odh-stable`.
All push templates, PR revision tags and unrelated YAML are preserved. Pushes
are non-force and the created remote ref is verified. Git/auth/network errors
abort the run.

Before enabling MCPLMO provisioning, supply its templates and apply the same
three exclusions to its release build triggers. Product source synchronization
and changes to central Konflux remain separate rollout work.

## Offline checks

```sh
uv run --with pytest==8.3.5 --with ruamel.yaml==0.18.15 python -m pytest .github/scripts/test_midstream_only_rhoai_releases.py
```

Tests use temporary manifests and mocked/local Git operations. Do not run the
provisioner itself against GitHub as a validation check.
