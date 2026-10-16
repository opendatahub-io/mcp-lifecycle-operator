#!/usr/bin/env python3
# /// script
# requires-python = ">=3.13"
# dependencies = ["ruamel.yaml==0.18.15"]
# ///
from __future__ import annotations

import base64
import io
import json
import os
import re
import subprocess
import sys
from collections.abc import Iterator, Mapping
from contextlib import contextmanager
from pathlib import Path
from tempfile import TemporaryDirectory
from typing import Final

from ruamel.yaml import YAML
from ruamel.yaml.error import YAMLError

DEFAULT_DOWNSTREAM_REPOSITORY: Final = "red-hat-data-services/mcp-lifecycle-operator"
DEFAULT_MANIFEST_PREFIX: Final = "odh-mcp-lifecycle-operator"
DEFAULT_IMAGE_REPOSITORY: Final = "quay.io/opendatahub/odh-mcp-lifecycle-operator"
DEFAULT_EXCLUDED_BRANCHES: Final = ""
BRANCH_PATTERN: Final = re.compile(r"^rhoai-[0-9]+\.[0-9]+(?:-ea\.[0-9]+)?$")
TARGET_BRANCH_ANNOTATION: Final = "build.appstudio.redhat.com/target_branch"
CEL_ANNOTATION: Final = "pipelinesascode.tekton.dev/on-cel-expression"
MAIN_PREDICATE: Final = 'target_branch == "main"'
RELEASE_PREDICATE: Final = (
    'event == "push" && target_branch.matches("^rhoai-[A-Za-z0-9_]'
    '[A-Za-z0-9_.-]{0,40}$")'
)


def expected_files(prefix: str) -> dict[str, str]:
    return {
        f"{prefix}-push.yaml": 'event == "push"',
        f"{prefix}-pull-request.yaml": 'event == "pull_request"',
        f"{prefix}-e2e-push.yaml": 'event == "push"',
        f"{prefix}-e2e-pull-request.yaml": 'event == "pull_request"',
        f"{prefix}-release-push.yaml": 'event == "push"',
        f"{prefix}-e2e-release-push.yaml": 'event == "push"',
    }


def expected_images(prefix: str, repository: str) -> dict[str, str]:
    e2e_repository = f"{repository}-e2e"
    return {
        f"{prefix}-push.yaml": f"{repository}:odh-stable",
        f"{prefix}-pull-request.yaml": f"{repository}:odh-pr",
        f"{prefix}-e2e-push.yaml": f"{e2e_repository}:main",
        f"{prefix}-e2e-pull-request.yaml": f"{e2e_repository}:odh-pr",
        f"{prefix}-release-push.yaml": f"{repository}:{{{{target_branch}}}}",
        f"{prefix}-e2e-release-push.yaml": f"{e2e_repository}:{{{{target_branch}}}}",
    }


EXPECTED_FILES: Final = expected_files(DEFAULT_MANIFEST_PREFIX)
REWRITE_FILES: Final = {
    f"{DEFAULT_MANIFEST_PREFIX}-{kind}.yaml"
    for kind in ("pull-request", "e2e-pull-request")
}
OPERATOR_PUSH_FILE: Final = f"{DEFAULT_MANIFEST_PREFIX}-push.yaml"
E2E_PUSH_FILE: Final = f"{DEFAULT_MANIFEST_PREFIX}-e2e-push.yaml"
EXPECTED_IMAGES: Final = expected_images(
    DEFAULT_MANIFEST_PREFIX, DEFAULT_IMAGE_REPOSITORY
)


class ProvisioningError(RuntimeError):
    """A release branch could not be safely provisioned."""


def fail(message: str) -> None:
    """Abort provisioning with a clear, actionable error."""
    raise ProvisioningError(message)


def load_yaml_documents(file: Path) -> tuple[YAML, list[object]]:
    """Load every YAML document in a Tekton file with round-trip support."""
    yaml = YAML(typ="rt")
    yaml.preserve_quotes = True
    try:
        documents = list(yaml.load_all(file.read_text()))
    except (OSError, YAMLError, ValueError) as error:
        fail(f"Cannot parse Tekton YAML {file}: {error}")
    if not documents:
        fail(f"Expected at least one YAML document in {file}")
    return yaml, documents


def annotations_for(document: object, file: Path) -> Mapping[str, object]:
    """Return metadata annotations, rejecting malformed Tekton documents."""
    if not isinstance(document, Mapping):
        fail(f"Expected a mapping document in {file}")
    metadata = document.get("metadata")
    if not isinstance(metadata, Mapping):
        fail(f"Missing metadata mapping in {file}")
    annotations = metadata.get("annotations")
    if not isinstance(annotations, Mapping):
        fail(f"Missing metadata.annotations mapping in {file}")
    return annotations


def params_for(document: object, file: Path) -> list[Mapping[str, object]]:
    if not isinstance(document, Mapping):
        fail(f"Expected a mapping document in {file}")
    spec = document.get("spec")
    params = spec.get("params") if isinstance(spec, Mapping) else None
    if not isinstance(params, list):
        fail(f"Missing spec.params list in {file}")
    if not all(isinstance(param, Mapping) for param in params):
        fail(f"Expected mapping entries in spec.params in {file}")
    return params


def named_param(
    params: list[Mapping[str, object]], name: str, file: Path
) -> Mapping[str, object]:
    matches = [param for param in params if param.get("name") == name]
    if len(matches) != 1:
        fail(f"Expected exactly one {name} parameter in {file}")
    return matches[0]


def annotated_document(documents: list[object], file: Path) -> object:
    matches = []
    pipeline_runs = []
    for document in documents:
        if not isinstance(document, Mapping):
            fail(f"Expected a mapping document in {file}")
        if document.get("kind") == "PipelineRun":
            pipeline_runs.append(document)
        metadata = document.get("metadata")
        annotations = (
            metadata.get("annotations") if isinstance(metadata, Mapping) else None
        )
        if isinstance(annotations, Mapping) and CEL_ANNOTATION in annotations:
            matches.append(document)
    if len(pipeline_runs) != 1:
        fail(f"Expected exactly one PipelineRun document in {file}")
    if len(matches) != 1 or matches[0] is not pipeline_runs[0]:
        fail(f"Expected exactly one annotated Tekton document in {file}")
    return matches[0]


def configured_exclusions() -> list[str]:
    excluded = os.getenv("EXCLUDED_BRANCHES", DEFAULT_EXCLUDED_BRANCHES)
    branches = [branch.strip() for branch in re.split(r"[,\n]", excluded)]
    branches = [branch for branch in branches if branch]
    if any(not BRANCH_PATTERN.fullmatch(branch) for branch in branches):
        fail("EXCLUDED_BRANCHES must contain only valid RHOAI release branch names")
    return branches


def release_predicate(exclusions: list[str]) -> str:
    if not exclusions:
        return RELEASE_PREDICATE
    return (
        f"{RELEASE_PREDICATE} && !(target_branch in {json.dumps(sorted(exclusions))})"
    )


def validate_and_rewrite(
    document: object,
    branch: str,
    file: Path,
    expected_event: str,
    rewrite: bool,
    release: bool,
    exclusions: list[str],
) -> None:
    annotations = annotations_for(document, file)
    if annotations.get(TARGET_BRANCH_ANNOTATION) != "{{target_branch}}":
        fail(f"Missing expected target-branch annotation in {file}")
    expression = annotations.get(CEL_ANNOTATION)
    if not isinstance(expression, str):
        fail(f"Missing expected {expected_event} CEL predicate in {file}")
    normalized = " ".join(expression.split())
    if rewrite:
        expected = f"{expected_event} && {MAIN_PREDICATE}"
        if normalized != expected:
            fail(f"Unexpected main CEL predicate in {file}")
        annotations[CEL_ANNOTATION] = normalized.replace(
            MAIN_PREDICATE, f'target_branch == "{branch}"'
        )
    elif release:
        if expression != release_predicate(exclusions):
            fail(f"Unexpected release CEL predicate in {file}")
    elif normalized != f"{expected_event} && {MAIN_PREDICATE}":
        fail(f"Unexpected main CEL predicate in {file}")


def rewrite_tekton_filters(branch: str, tekton_dir: Path) -> None:
    """Validate and rewrite the approved release-branch Tekton fields."""
    if not BRANCH_PATTERN.fullmatch(branch):
        fail(f"Invalid release branch {branch!r}")
    prefix = os.getenv("MANIFEST_PREFIX") or DEFAULT_MANIFEST_PREFIX
    repository = os.getenv("IMAGE_REPOSITORY") or DEFAULT_IMAGE_REPOSITORY
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]*", prefix):
        fail("MANIFEST_PREFIX must be a safe filename prefix")
    if not re.fullmatch(
        r"[A-Za-z0-9.-]+(?::[0-9]{1,5})?/(?:[a-z0-9]+(?:[._-][a-z0-9]+)*)"
        r"(?:/(?:[a-z0-9]+(?:[._-][a-z0-9]+)*))*",
        repository,
    ):
        fail("IMAGE_REPOSITORY must be an untagged registry/repository")
    exclusions = configured_exclusions()
    images = expected_images(prefix, repository)
    files = expected_files(prefix)
    rewrite_files = {
        f"{prefix}-{kind}.yaml" for kind in ("pull-request", "e2e-pull-request")
    }
    operator_push_file = f"{prefix}-push.yaml"
    e2e_push_file = f"{prefix}-e2e-push.yaml"
    release_files = {
        f"{prefix}-release-push.yaml",
        f"{prefix}-e2e-release-push.yaml",
    }
    actual_files = {
        file.name
        for pattern in ("*.yaml", "*.yml")
        for file in tekton_dir.glob(pattern)
    }
    unexpected = actual_files - files.keys()
    missing = files.keys() - actual_files
    if missing:
        fail(f"Missing expected Tekton files: {', '.join(sorted(missing))}")
    if unexpected:
        fail(f"Unexpected Tekton files: {', '.join(sorted(unexpected))}")

    plans: list[tuple[Path, YAML, list[object], bool]] = []
    for filename, event in files.items():
        file = tekton_dir / filename
        yaml, documents = load_yaml_documents(file)
        document = annotated_document(documents, file)
        validate_and_rewrite(
            document,
            branch,
            file,
            event,
            filename in rewrite_files,
            filename in release_files,
            exclusions,
        )
        params = params_for(document, file)
        image_param = named_param(params, "output-image", file)
        expected_image = images[filename]
        if image_param.get("value") != expected_image:
            fail(f"Expected output image {expected_image!r} in {file}")
        changed = filename in rewrite_files
        if filename == e2e_push_file:
            tags = named_param(params, "additional-tags", file).get("value")
            if tags != ["odh-stable"]:
                fail(f"Expected exactly the odh-stable additional tag in {file}")
        elif filename == operator_push_file or filename in release_files:
            tags = [param for param in params if param.get("name") == "additional-tags"]
            if len(tags) > 1 or (tags and tags[0].get("value") not in (None, [])):
                fail(f"Expected no additional tags in {file}")
        elif "pull-request" in filename:
            tags = named_param(params, "additional-tags", file).get("value")
            expected_tag = "odh-pr-{{revision}}"
            if tags != [expected_tag]:
                fail(f"Expected unchanged PR additional tag in {file}")
        plans.append((file, yaml, documents, changed))

    for file, yaml, documents, changed in plans:
        if not changed:
            continue
        output = io.StringIO()
        yaml.dump_all(documents, output)
        file.write_text(output.getvalue())


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


def git(*args: str, cwd: Path | None = None, capture_output: bool = False) -> str:
    """Run git with ephemeral GitHub auth, returning captured stdout if requested."""
    env = os.environ.copy()
    token = env.get("ODH_TOKEN")
    if token:
        count = int(env.get("GIT_CONFIG_COUNT", "0"))
        env.update(
            GIT_CONFIG_COUNT=str(count + 1),
            **{
                f"GIT_CONFIG_KEY_{count}": "http.https://github.com/.extraheader",
                f"GIT_CONFIG_VALUE_{count}": "AUTHORIZATION: basic "
                + base64.b64encode(f"x-access-token:{token}".encode()).decode(),
            },
        )
    encoded_token = (
        base64.b64encode(f"x-access-token:{token}".encode()).decode() if token else ""
    )

    def redact(output: str) -> str:
        if not token:
            return output
        return output.replace(token, "[REDACTED]").replace(encoded_token, "[REDACTED]")

    result = subprocess.run(
        ["git", *args], cwd=cwd, capture_output=True, text=True, env=env, check=False
    )
    if result.returncode:
        raise subprocess.CalledProcessError(
            result.returncode,
            ["git", *args],
            output=redact(result.stdout),
            stderr=redact(result.stderr),
        )
    if capture_output:
        return redact(result.stdout)
    for output, stream in ((result.stdout, sys.stdout), (result.stderr, sys.stderr)):
        stream.write(redact(output))
    return ""


def remote_ref_exists(remote: str, ref: str) -> bool:
    """Return whether a remote ref exists, preserving git failure semantics."""
    try:
        git("ls-remote", "--exit-code", "--heads", remote, ref)
    except subprocess.CalledProcessError as error:
        if error.returncode == 2:
            return False
        raise
    return True


def remote_ref_sha(remote: str, ref: str) -> str | None:
    """Return a remote ref's SHA, or None if the ref does not exist."""
    try:
        output = git(
            "ls-remote", "--exit-code", "--heads", remote, ref, capture_output=True
        )
    except subprocess.CalledProcessError as error:
        if error.returncode == 2:
            return None
        raise
    return output.split(maxsplit=1)[0]


def verify_pushed_branch(remote: str, branch: str, expected_sha: str) -> None:
    """Fail unless the created remote branch points at the provisioned commit."""
    ref = f"refs/heads/{branch}"
    actual_sha = remote_ref_sha(remote, ref)
    if actual_sha != expected_sha:
        fail(
            f"Remote branch verification failed for {ref}: expected {expected_sha}, "
            f"got {actual_sha or 'missing'}"
        )


def push_new_branch(remote: str, branch: str, worktree: Path) -> None:
    """Push only if the advertised remote ref is absent; Git enforces the race CAS."""
    with TemporaryDirectory(prefix="mcp-lifecycle-operator-hooks-") as directory:
        hooks = Path(directory)
        pre_push = hooks / "pre-push"
        pre_push.write_text(
            "#!/bin/sh\n"
            "while read local_ref local_sha remote_ref remote_sha; do\n"
            "  case \"$remote_sha\" in ''|*[!0]*)\n"
            '    echo "Ref already exists: $remote_ref" >&2; exit 1;;\n'
            "  esac\n"
            "done\n"
        )
        pre_push.chmod(0o755)
        git(
            "-c",
            f"core.hooksPath={hooks}",
            "push",
            "--no-force",
            "--no-follow-tags",
            remote,
            f"HEAD:refs/heads/{branch}",
            cwd=worktree,
        )


def has_staged_changes(worktree: Path) -> bool:
    """Return whether the worktree has staged changes."""
    try:
        git("diff", "--cached", "--quiet", cwd=worktree)
    except subprocess.CalledProcessError as error:
        if error.returncode == 1:
            return True
        raise
    return False


def main() -> None:
    """Create missing release branches in the configured downstream repository."""
    downstream_repository = (
        os.getenv("DOWNSTREAM_REPOSITORY") or DEFAULT_DOWNSTREAM_REPOSITORY
    )
    target_repository = os.environ.get("GITHUB_REPOSITORY", "")
    if not os.environ.get("ODH_TOKEN"):
        fail("ODH_TOKEN is required")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", target_repository):
        fail("GITHUB_REPOSITORY must be an owner/repository")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", downstream_repository):
        fail("DOWNSTREAM_REPOSITORY must be an owner/repository")
    downstream_url = f"https://github.com/{downstream_repository}.git"
    target_url = f"https://github.com/{target_repository}.git"
    excluded_branches = configured_exclusions()
    main_sha = git("rev-parse", "HEAD", capture_output=True).strip()

    git("ls-remote", "--exit-code", "--heads", downstream_url, "main")
    git("ls-remote", "--exit-code", "--heads", target_url, "main")

    refs = git("ls-remote", "--heads", downstream_url, capture_output=True)
    for line in refs.splitlines():
        downstream_sha, downstream_ref = line.split(maxsplit=1)
        branch = downstream_ref.removeprefix("refs/heads/")
        if not BRANCH_PATTERN.fullmatch(branch) or branch in excluded_branches:
            continue
        if remote_ref_exists(target_url, f"refs/heads/{branch}"):
            print(f"Skipping existing target ref {branch}.")
            continue

        print(
            f"Creating target ref {branch} from downstream {branch} at {downstream_sha} "
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
                fail(f"No branch-specific Tekton changes generated for {branch}")
            git("diff", "--cached", "--", ".tekton", cwd=worktree)
            git(
                "commit", "-m", f"CI: target Tekton pipelines at {branch}", cwd=worktree
            )
            expected_sha = git(
                "rev-parse", "HEAD", cwd=worktree, capture_output=True
            ).strip()
            push_new_branch(target_url, branch, worktree)
            verify_pushed_branch(target_url, branch, expected_sha)
        print(f"Created target ref: {branch} with branch-specific Tekton filters")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:  # noqa: BLE001, BROAD_EXCEPT_OK
        detail = (
            f": {error.stderr.strip()}"
            if isinstance(error, subprocess.CalledProcessError) and error.stderr
            else ""
        )
        print(
            f"::error title=RHOAI release branch provisioning::{error}{detail}",
            file=sys.stderr,
        )
        print(
            f"RHOAI release branch provisioning failed: {error}{detail}",
            file=sys.stderr,
        )
        raise
