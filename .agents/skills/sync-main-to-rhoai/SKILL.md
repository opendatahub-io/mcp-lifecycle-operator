---
name: sync-main-to-rhoai
description: Use when syncing the opendatahub/main payload (rebasebot upstream syncs PLUS the midstream-only commits like image sha bumps, Konflux/security config, renovate) down onto a release branch (default opendatahub/rhoai-3.6). Triggers include "sync main to rhoai-3.6", "create a merge/sync branch", "bring main down to the release branch", "update rhoai-3.6 from main". Only relevant in the mcp-lifecycle-operator repo.
---

# Sync opendatahub/main to a release branch (rhoai-3.x)

Creates a sync branch that brings the **full** `opendatahub/main` payload onto a release
branch (default `rhoai-3.6`), preserving only the branch-specific `.tekton` overrides, then
**stops before push** and hands you the exact push + PR commands.

## Why merge-take-main's-tree, not cherry-pick

- We want the *entire* new payload of main: rebasebot upstream syncs **and** midstream-only
  commits (image sha bumps, Konflux/security config, renovate). Cherry-picking means
  hand-selecting "everything since last time" and drifts.
- main is **rebased/force-pushed** by rebasebot. Cherry-picking across rewritten history
  makes duplicate-but-different commits and recurring conflicts.
- The release branch is identical to main **except** the `.tekton/` files (Konflux trigger
  branch targeting + operator image tag). So: snap the tree to main, re-apply the `.tekton`
  delta. This reproduces the proven recipe (merge commit whose tree == main's tree, then
  the `.tekton` fixups).

## The ONLY branch-specific override set

Verified empirically: after a sync, the release branch differs from main **only** in
`.tekton/*`. `Dockerfile.konflux`, `renovate.json`, `config/overlays/odh/` are NOT
branch-specific; they track main and the tree-snap takes main's version. Do not preserve them.

## Procedure

Run from the repo root. The `opendatahub` remote must point at
`opendatahub-io/mcp-lifecycle-operator`.
Let `TARGET` = the release branch (default `rhoai-3.6`; honor an arg like `rhoai-3.7`).

### 1. Preflight
```bash
cd "$(git rev-parse --show-toplevel)"
git fetch opendatahub --prune
git status --porcelain --untracked-files=no   # MUST be empty; untracked scratch files are fine
```
Abort only if there are **tracked** changes (staged/unstaged). Untracked files are left
untouched. Confirm both refs exist:
`git rev-parse opendatahub/main opendatahub/$TARGET`.

Check there is actually something to sync:
```bash
git rev-list --count opendatahub/$TARGET..opendatahub/main
```
If `0`, report that main is already contained and stop.

### 2. Capture the .tekton override patch (release-branch-owned delta vs main)
```bash
PATCH=$(mktemp /tmp/tekton-override.XXXX.patch)
git diff opendatahub/main opendatahub/$TARGET -- .tekton/ > "$PATCH"
```
`git diff A B` = changes to turn A into B, so this is "what the release branch does to
main's `.tekton`" - exactly what we re-apply onto main's tree. If the patch is empty, warn
(unexpected) but continue; the release branch would then equal main.

### 3. Create the sync branch
```bash
BR=sync-main-to-$TARGET-$(date +%Y%m%d)
git switch -c "$BR" opendatahub/$TARGET
```
If a branch with that name already exists (e.g. a same-day rerun after a conflict), delete or
rename the old one, or append a suffix - `git switch -c` aborts on a name clash.

### 4. Merge main, snapping the tree to main
```bash
git merge --no-ff --no-commit -s ours opendatahub/main
git read-tree -u --reset opendatahub/main
git commit -s -m "Merge main into $TARGET using the main tree"
```
`-s ours` sets up a 2-parent merge (release first-parent, main second) without touching the
tree; `read-tree -u --reset main` makes index+worktree equal main's tree; the commit records
both parents with tree == main.

**Caution:** `read-tree -u --reset` overwrites working-tree paths to match main and (unlike
`-m -u`) does **not** abort to protect untracked files - an untracked file that shares a path
with something tracked in main's tree will be silently removed/overwritten. The preflight only
guarantees no *tracked* changes, so if you keep untracked scratch at such a path, move it out
first.

**Assert tree == main, or STOP:**
```bash
git diff --quiet opendatahub/main && echo "OK: tree matches main" || echo "FAIL: tree != main"
```
If FAIL, stop and investigate - do not continue.

### 5. Re-apply the .tekton overrides
```bash
git apply --3way "$PATCH"
```
- If it applies cleanly: `git add .tekton/` then
  `git commit -s -m "fix: retarget .tekton triggers and image tag to $TARGET"`.
- If the step-2 patch was **empty** (release branch's `.tekton` already equals main): skip this
  step - there is nothing to re-apply and nothing to commit. `git add .tekton/` would stage
  nothing and the commit would fail with "nothing to commit". The sync branch already equals main.
- If it conflicts (main reworked `.tekton`): **STOP**. Show the conflict and let a human
  resolve. Do not auto-resolve.

### 6. Sanity report
```bash
echo "=== Payload brought in (should be the whole main delta) ==="
git diff --stat opendatahub/$TARGET..HEAD | tail -3
echo "=== Final diff vs main (MUST be only .tekton/) ==="
git diff --name-only opendatahub/main..HEAD
```
The second list must contain only `.tekton/*`. If anything else appears, flag it.

### 7. Stop before push - hand over the commands
Do NOT push and do NOT open the PR. Push the sync branch to **your fork remote** and open a
cross-fork PR against the release branch. Print, for a human to run (replace `<your-fork>`
with your fork remote name and `<you>` with your GitHub user):
```bash
git push <your-fork> $BR
gh pr create --repo opendatahub-io/mcp-lifecycle-operator \
  --base $TARGET --head <you>:$BR \
  --title "Sync main into $TARGET" \
  --body "Brings the opendatahub/main payload (upstream rebasebot syncs + midstream commits) onto $TARGET; tree snapped to main with .tekton retargeted to $TARGET."
```

## Conventions
- Commits signed off (`-s`). No issue-number hashes, no Jira keys, no `Co-Authored-By` in
  commit messages.
- Never force-push. Never modify untracked working files.
- Leave the sync branch checked out so a human can inspect before pushing.
