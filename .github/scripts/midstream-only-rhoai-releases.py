#!/usr/bin/env python3
# /// script
# requires-python = ">=3.13"
# dependencies = []
# ///
from __future__ import annotations

import os
import re
import subprocess
from contextlib import contextmanager
from pathlib import Path
from tempfile import TemporaryDirectory
from typing import Final, Iterator

DOWNSTREAM_URL: Final = "https://github.com/red-hat-data-services/mcp-lifecycle-operator.git"
BRANCH_PATTERN: Final = re.compile(r"^rhoai-[0-9]+\.[0-9]+(?:-ea\.[0-9]+)?$")
FILTER_BLOCK_PATTERN: Final = re.compile(
    r"(?m)^[^\n]*target_branch:[^\n]*\n(?:[^\n]*\n){3}[^\n]*"
)


def git(*args: str, cwd: Path | None = None, capture_output: bool = False) -> str:
    """Run git and return captured standard output when requested."""
    result = subprocess.run(
        ["git", *args],
        cwd=cwd,
        check=True,
        capture_output=capture_output,
        text=True,
    )
    return result.stdout if capture_output else ""


def rewrite_tekton_filters(branch: str, tekton_dir: Path) -> None:
    """Rewrite multiline main-branch CEL filters in Tekton YAML files."""
    for file in tekton_dir.glob("*.yaml"):
        content = file.read_text()
        rewritten = FILTER_BLOCK_PATTERN.sub(
            lambda match: match.group().replace('== "main"', f'== "{branch}"'),
            content,
        )
        if rewritten != content:
            file.write_text(rewritten)


@contextmanager
def detached_worktree(main_sha: str) -> Iterator[Path]:
    """Create and remove a temporary detached worktree."""
    with TemporaryDirectory(prefix="mcp-lifecycle-operator-") as directory:
        worktree = Path(directory) / "worktree"
        git("worktree", "add", "--detach", str(worktree), main_sha)
        try:
            yield worktree
        finally:
            git("worktree", "remove", "--force", str(worktree))


def remote_ref_exists(remote: str, ref: str) -> bool:
    """Return whether a remote ref exists, preserving git failure semantics."""
    try:
        git("ls-remote", "--exit-code", "--heads", remote, ref)
    except subprocess.CalledProcessError:
        return False
    return True


def has_staged_changes(worktree: Path) -> bool:
    """Return whether the worktree has staged changes."""
    try:
        git("diff", "--cached", "--quiet", cwd=worktree)
    except subprocess.CalledProcessError:
        return True
    return False


def main() -> None:
    """Create missing RHOAI release branches in the ODH repository."""
    odh_url = f"https://x-access-token:{os.environ['ODH_TOKEN']}@github.com/{os.environ['GITHUB_REPOSITORY']}.git"
    main_sha = git("rev-parse", "HEAD", capture_output=True).strip()

    git("ls-remote", "--exit-code", "--heads", DOWNSTREAM_URL, "main")
    git("ls-remote", "--exit-code", "--heads", odh_url, "main")

    refs = git("ls-remote", "--heads", DOWNSTREAM_URL, capture_output=True)
    for line in refs.splitlines():
        downstream_sha, downstream_ref = line.split(maxsplit=1)
        branch = downstream_ref.removeprefix("refs/heads/")
        if not BRANCH_PATTERN.fullmatch(branch):
            continue
        if remote_ref_exists(odh_url, f"refs/heads/{branch}"):
            print(f"Skipping existing ODH ref {branch}.")
            continue

        print(
            f"Creating ODH ref {branch} from downstream {branch} at {downstream_sha} "
            f"using ODH main at {main_sha}"
        )
        with detached_worktree(main_sha) as worktree:
            git("config", "user.name", "github-actions[bot]", cwd=worktree)
            git(
                "config",
                "user.email",
                "github-actions[bot]@users.noreply.github.com",
                cwd=worktree,
            )
            rewrite_tekton_filters(branch, worktree / ".tekton")
            git("add", ".tekton", cwd=worktree)
            if not has_staged_changes(worktree):
                print(f"No branch-specific Tekton changes needed for {branch}.")
                git("push", odh_url, f"{main_sha}:refs/heads/{branch}", cwd=worktree)
            else:
                git("diff", "--cached", "--", ".tekton", cwd=worktree)
                git("commit", "-m", f"CI: target Tekton pipelines at {branch}", cwd=worktree)
                git("push", odh_url, f"HEAD:refs/heads/{branch}", cwd=worktree)
        print(f"Created ODH ref: {branch} with branch-specific Tekton filters")


if __name__ == "__main__":
    main()
