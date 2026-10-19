#!/usr/bin/env python3
# /// script
# requires-python = ">=3.13"
# dependencies = ["pytest==8.3.5", "ruamel.yaml==0.18.15"]
# ///
from __future__ import annotations

import base64
import copy
import importlib.util
import re
import shutil
import subprocess
from pathlib import Path

import pytest
from ruamel.yaml import YAML

implementation_path = Path(__file__).with_name("midstream-only-rhoai-releases.py")
implementation_spec = importlib.util.spec_from_file_location(
    "midstream_only_rhoai_releases", implementation_path
)
if implementation_spec is None or implementation_spec.loader is None:
    raise ImportError(f"Cannot load {implementation_path}")
implementation = importlib.util.module_from_spec(implementation_spec)
implementation_spec.loader.exec_module(implementation)

BRANCH = "rhoai-3.6-ea.1"
MANIFESTS = Path(__file__).parents[2] / ".tekton"
EXPECTED_FILES = set(implementation.EXPECTED_FILES)
RELEASE_PREDICATE = implementation.RELEASE_PREDICATE


def copy_manifests(tmp_path: Path) -> Path:
    tekton_dir = tmp_path / ".tekton"
    shutil.copytree(MANIFESTS, tekton_dir)
    return tekton_dir


def read_document(file: Path) -> dict[str, object]:
    yaml = YAML(typ="rt")
    document = next(yaml.load_all(file.read_text()))
    assert isinstance(document, dict)
    return document


def read_documents(file: Path) -> list[object]:
    return list(YAML(typ="rt").load_all(file.read_text()))


def annotation(file: Path, name: str) -> str:
    document = read_document(file)
    metadata = document["metadata"]
    assert isinstance(metadata, dict)
    annotations = metadata["annotations"]
    assert isinstance(annotations, dict)
    value = annotations[name]
    assert isinstance(value, str)
    return value


def push_matches(file: Path, target_branch: str, exclusions: list[str]) -> bool:
    expression = annotation(file, implementation.CEL_ANNOTATION)
    if expression == 'event == "push" && target_branch == "main"':
        return target_branch == "main"
    assert expression == implementation.release_predicate(exclusions)
    return (
        re.fullmatch(r"rhoai-[A-Za-z0-9_][A-Za-z0-9_.-]{0,40}", target_branch)
        is not None
        and target_branch not in exclusions
    )


def assert_push_selection(
    tekton_dir: Path, prefix: str, exclusions: list[str], release_branch: str
) -> None:
    for ordinary, release in (
        (f"{prefix}-push.yaml", f"{prefix}-release-push.yaml"),
        (f"{prefix}-e2e-push.yaml", f"{prefix}-e2e-release-push.yaml"),
    ):
        for branch, expected in ((release_branch, [release]), ("main", [ordinary])):
            matched = [
                name
                for name in (ordinary, release)
                if push_matches(tekton_dir / name, branch, exclusions)
            ]
            assert matched == expected
    if "rhoai-3.6-ea.2" in exclusions:
        assert not any(
            push_matches(tekton_dir / name, "rhoai-3.6-ea.2", exclusions)
            for name in (
                f"{prefix}-push.yaml",
                f"{prefix}-release-push.yaml",
                f"{prefix}-e2e-push.yaml",
                f"{prefix}-e2e-release-push.yaml",
            )
        )


def parameter(file: Path, name: str) -> dict[str, object]:
    document = read_document(file)
    spec = document["spec"]
    assert isinstance(spec, dict)
    params = spec["params"]
    assert isinstance(params, list)
    matches = [
        param for param in params if isinstance(param, dict) and param["name"] == name
    ]
    assert len(matches) == 1
    return matches[0]


def test_rewrites_only_pull_request_predicates(tmp_path: Path) -> None:
    tekton_dir = copy_manifests(tmp_path)
    push_files = EXPECTED_FILES - implementation.REWRITE_FILES
    before = {name: (tekton_dir / name).read_bytes() for name in push_files}

    implementation.rewrite_tekton_filters(BRANCH, tekton_dir)

    assert implementation.REWRITE_FILES == {
        "odh-mcp-lifecycle-operator-pull-request.yaml",
        "odh-mcp-lifecycle-operator-e2e-pull-request.yaml",
    }
    for filename in implementation.REWRITE_FILES:
        cel = annotation(tekton_dir / filename, implementation.CEL_ANNOTATION)
        assert cel == f'event == "pull_request" && target_branch == "{BRANCH}"'
    assert all(
        (tekton_dir / name).read_bytes() == content for name, content in before.items()
    )
    assert (
        parameter(tekton_dir / implementation.OPERATOR_PUSH_FILE, "output-image")[
            "value"
        ]
        == "quay.io/opendatahub/odh-mcp-lifecycle-operator:odh-stable"
    )
    assert (
        parameter(tekton_dir / implementation.E2E_PUSH_FILE, "output-image")["value"]
        == "quay.io/opendatahub/odh-mcp-lifecycle-operator-e2e:main"
    )
    assert parameter(tekton_dir / implementation.E2E_PUSH_FILE, "additional-tags")[
        "value"
    ] == ["odh-stable"]


def test_preserves_unrelated_yaml_semantics_and_documents(tmp_path: Path) -> None:
    tekton_dir = copy_manifests(tmp_path)
    multi_doc = (
        tekton_dir / f"{implementation.DEFAULT_MANIFEST_PREFIX}-pull-request.yaml"
    )
    yaml = YAML(typ="rt")
    docs = read_documents(multi_doc)
    docs.append(
        {
            "apiVersion": "v1",
            "kind": "ConfigMap",
            "metadata": {"name": "unrelated"},
            "data": {"keep": "value"},
        }
    )
    with multi_doc.open("w") as stream:
        yaml.dump_all(docs, stream)

    original = {
        filename: read_documents(tekton_dir / filename)
        for filename in implementation.REWRITE_FILES
    }
    expected = copy.deepcopy(original)
    events = implementation.expected_files(implementation.DEFAULT_MANIFEST_PREFIX)
    for filename, documents in expected.items():
        document = next(
            doc
            for doc in documents
            if implementation.CEL_ANNOTATION in doc["metadata"]["annotations"]
        )
        annotations = document["metadata"]["annotations"]
        annotations[implementation.CEL_ANNOTATION] = (
            f'{events[filename]} && target_branch == "{BRANCH}"'
        )

    implementation.rewrite_tekton_filters(BRANCH, tekton_dir)

    assert {
        filename: read_documents(tekton_dir / filename)
        for filename in implementation.REWRITE_FILES
    } == expected


def test_preserves_all_push_templates_and_pr_images_and_tags(tmp_path: Path) -> None:
    tekton_dir = copy_manifests(tmp_path)
    unchanged = {
        filename: (tekton_dir / filename).read_bytes()
        for filename in EXPECTED_FILES - implementation.REWRITE_FILES
    }

    implementation.rewrite_tekton_filters(BRANCH, tekton_dir)

    assert all(
        (tekton_dir / filename).read_bytes() == content
        for filename, content in unchanged.items()
    )
    for filename in (
        "odh-mcp-lifecycle-operator-release-push.yaml",
        "odh-mcp-lifecycle-operator-e2e-release-push.yaml",
    ):
        assert (
            annotation(tekton_dir / filename, implementation.CEL_ANNOTATION)
            == RELEASE_PREDICATE
        )
    assert (
        parameter(
            tekton_dir / "odh-mcp-lifecycle-operator-pull-request.yaml", "output-image"
        )["value"]
        == "quay.io/opendatahub/odh-mcp-lifecycle-operator:odh-pr"
    )
    assert parameter(
        tekton_dir / "odh-mcp-lifecycle-operator-pull-request.yaml", "additional-tags"
    )["value"] == ["odh-pr-{{revision}}"]
    assert (
        parameter(
            tekton_dir / "odh-mcp-lifecycle-operator-e2e-pull-request.yaml",
            "output-image",
        )["value"]
        == "quay.io/opendatahub/odh-mcp-lifecycle-operator-e2e:odh-pr"
    )
    assert parameter(
        tekton_dir / "odh-mcp-lifecycle-operator-e2e-pull-request.yaml",
        "additional-tags",
    )["value"] == ["odh-pr-{{revision}}"]
    assert_push_selection(
        tekton_dir, implementation.DEFAULT_MANIFEST_PREFIX, [], BRANCH
    )


@pytest.mark.parametrize("filename", sorted(EXPECTED_FILES))
def test_requires_every_contract_file(tmp_path: Path, filename: str) -> None:
    tekton_dir = copy_manifests(tmp_path)
    (tekton_dir / filename).unlink()

    with pytest.raises(
        implementation.ProvisioningError, match="Missing expected Tekton files"
    ):
        implementation.rewrite_tekton_filters(BRANCH, tekton_dir)


@pytest.mark.parametrize("suffix", ["yaml", "yml"])
def test_rejects_unexpected_pipeline_file_without_writing(
    tmp_path: Path, suffix: str
) -> None:
    tekton_dir = copy_manifests(tmp_path)
    (tekton_dir / f"unrelated.{suffix}").write_text("kind: PipelineRun\n")
    before = {file.name: file.read_bytes() for file in tekton_dir.iterdir()}

    with pytest.raises(
        implementation.ProvisioningError, match="Unexpected Tekton files"
    ):
        implementation.rewrite_tekton_filters(BRANCH, tekton_dir)
    assert {file.name: file.read_bytes() for file in tekton_dir.iterdir()} == before


def test_rejects_extra_legacy_pipeline_run_document_without_writing(
    tmp_path: Path,
) -> None:
    tekton_dir = copy_manifests(tmp_path)
    file = tekton_dir / implementation.OPERATOR_PUSH_FILE
    yaml = YAML(typ="rt")
    documents = read_documents(file)
    documents.append(
        {
            "apiVersion": "tekton.dev/v1",
            "kind": "PipelineRun",
            "metadata": {
                "annotations": {
                    "pipelinesascode.tekton.dev/on-event": "push",
                    "pipelinesascode.tekton.dev/on-target-branch": "main",
                }
            },
        }
    )
    with file.open("w") as stream:
        yaml.dump_all(documents, stream)
    before = {path.name: path.read_bytes() for path in tekton_dir.glob("*.yaml")}

    with pytest.raises(implementation.ProvisioningError, match="one PipelineRun"):
        implementation.rewrite_tekton_filters(BRANCH, tekton_dir)
    assert {
        path.name: path.read_bytes() for path in tekton_dir.glob("*.yaml")
    } == before


@pytest.mark.parametrize(
    "mutation",
    [
        lambda directory: (
            directory / "odh-mcp-lifecycle-operator-push.yaml"
        ).write_text(
            (directory / "odh-mcp-lifecycle-operator-push.yaml")
            .read_text()
            .replace(
                'target_branch\n      == "main"', 'target_branch\n      == "other"'
            )
        ),
        lambda directory: (
            directory / "odh-mcp-lifecycle-operator-e2e-push.yaml"
        ).write_text(
            (directory / "odh-mcp-lifecycle-operator-e2e-push.yaml")
            .read_text()
            .replace("odh-stable", "unexpected")
        ),
    ],
)
def test_fails_closed_for_missing_expected_fields(
    tmp_path: Path, mutation: object
) -> None:
    tekton_dir = copy_manifests(tmp_path)
    assert callable(mutation)
    mutation(tekton_dir)

    with pytest.raises(implementation.ProvisioningError):
        implementation.rewrite_tekton_filters(BRANCH, tekton_dir)


def test_fails_closed_for_ambiguous_predicate(tmp_path: Path) -> None:
    tekton_dir = copy_manifests(tmp_path)
    file = tekton_dir / "odh-mcp-lifecycle-operator-push.yaml"
    file.write_text(
        file.read_text().replace('== "main"', '== "main" && target_branch == "main"')
    )

    with pytest.raises(implementation.ProvisioningError, match="Unexpected main CEL"):
        implementation.rewrite_tekton_filters(BRANCH, tekton_dir)


@pytest.mark.parametrize(
    "expression",
    [
        RELEASE_PREDICATE,
        'event == "push" && target_branch == "main" || target_branch == "other"',
        'event == "push" && target_branch == "main" && true',
    ],
)
def test_main_templates_require_exact_main_cel(tmp_path: Path, expression: str) -> None:
    tekton_dir = copy_manifests(tmp_path)
    file = tekton_dir / implementation.OPERATOR_PUSH_FILE
    file.write_text(
        file.read_text().replace(
            'event == "push" && target_branch\n      == "main"', expression
        )
    )

    with pytest.raises(implementation.ProvisioningError, match="Unexpected main CEL"):
        implementation.rewrite_tekton_filters(BRANCH, tekton_dir)


def test_release_cel_remains_strict(tmp_path: Path) -> None:
    tekton_dir = copy_manifests(tmp_path)
    file = tekton_dir / f"{implementation.DEFAULT_MANIFEST_PREFIX}-release-push.yaml"
    file.write_text(
        file.read_text().replace(
            RELEASE_PREDICATE, RELEASE_PREDICATE.replace(" && ", "  && ")
        )
    )

    with pytest.raises(
        implementation.ProvisioningError, match="Unexpected release CEL"
    ):
        implementation.rewrite_tekton_filters(BRANCH, tekton_dir)


def test_main_cel_normalizes_for_validation_without_rewriting(tmp_path: Path) -> None:
    tekton_dir = copy_manifests(tmp_path)
    file = tekton_dir / implementation.OPERATOR_PUSH_FILE
    file.write_text(
        file.read_text().replace(
            'event == "push" && target_branch\n      == "main"',
            ' event   == "push"  &&  target_branch == "main" ',
        )
    )
    original = file.read_bytes()

    implementation.rewrite_tekton_filters(BRANCH, tekton_dir)
    assert file.read_bytes() == original


@pytest.mark.parametrize(
    "filename",
    [
        implementation.OPERATOR_PUSH_FILE,
        f"{implementation.DEFAULT_MANIFEST_PREFIX}-release-push.yaml",
        f"{implementation.DEFAULT_MANIFEST_PREFIX}-e2e-release-push.yaml",
    ],
)
def test_rejects_additional_tags_on_operator_and_release_pushes(
    tmp_path: Path, filename: str
) -> None:
    tekton_dir = copy_manifests(tmp_path)
    file = tekton_dir / filename
    doc = read_document(file)
    params = doc["spec"]["params"]
    params.append({"name": "additional-tags", "value": ["odh-stable"]})
    yaml = YAML(typ="rt")
    with file.open("w") as stream:
        yaml.dump(doc, stream)

    with pytest.raises(implementation.ProvisioningError, match="no additional tags"):
        implementation.rewrite_tekton_filters(BRANCH, tekton_dir)


def test_fails_closed_for_ambiguous_output_image(tmp_path: Path) -> None:
    tekton_dir = copy_manifests(tmp_path)
    file = tekton_dir / implementation.OPERATOR_PUSH_FILE
    content = file.read_text()
    file.write_text(
        content.replace(
            "  - name: dockerfile",
            "  - name: output-image\n    value: quay.io/opendatahub/odh-mcp-lifecycle-operator:odh-stable\n  - name: dockerfile",
        )
    )

    with pytest.raises(
        implementation.ProvisioningError, match="exactly one output-image"
    ):
        implementation.rewrite_tekton_filters(BRANCH, tekton_dir)


def test_does_not_write_until_all_files_validate(tmp_path: Path) -> None:
    tekton_dir = copy_manifests(tmp_path)
    original = {file.name: file.read_bytes() for file in tekton_dir.glob("*.yaml")}
    late_file = tekton_dir / "odh-mcp-lifecycle-operator-e2e-release-push.yaml"
    late_file.write_text(
        late_file.read_text().replace(":{{target_branch}}", ":unexpected")
    )
    original[late_file.name] = late_file.read_bytes()

    with pytest.raises(implementation.ProvisioningError):
        implementation.rewrite_tekton_filters(BRANCH, tekton_dir)
    assert {
        file.name: file.read_bytes() for file in tekton_dir.glob("*.yaml")
    } == original


def test_remote_verification_fails_for_missing_or_wrong_sha(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(implementation, "remote_ref_sha", lambda remote, ref: None)
    with pytest.raises(
        implementation.ProvisioningError, match="Remote branch verification"
    ):
        implementation.verify_pushed_branch("remote", BRANCH, "expected")

    monkeypatch.setattr(implementation, "remote_ref_sha", lambda remote, ref: "wrong")
    with pytest.raises(
        implementation.ProvisioningError, match="expected expected, got wrong"
    ):
        implementation.verify_pushed_branch("remote", BRANCH, "expected")


def test_staged_diff_failure_is_not_treated_as_changes(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    monkeypatch.setattr(
        implementation,
        "git",
        lambda *args, **kwargs: (_ for _ in ()).throw(
            subprocess.CalledProcessError(128, args)
        ),
    )

    with pytest.raises(subprocess.CalledProcessError):
        implementation.has_staged_changes(tmp_path)


def test_remote_verification_propagates_remote_errors(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        implementation,
        "git",
        lambda *args, **kwargs: (_ for _ in ()).throw(
            subprocess.CalledProcessError(128, args)
        ),
    )

    with pytest.raises(subprocess.CalledProcessError):
        implementation.verify_pushed_branch("remote", BRANCH, "expected")


def test_default_configuration_matches_original_repository() -> None:
    assert (
        implementation.DEFAULT_DOWNSTREAM_REPOSITORY
        == "red-hat-data-services/mcp-lifecycle-operator"
    )
    assert implementation.DEFAULT_MANIFEST_PREFIX == "odh-mcp-lifecycle-operator"
    assert (
        implementation.DEFAULT_IMAGE_REPOSITORY
        == "quay.io/opendatahub/odh-mcp-lifecycle-operator"
    )
    assert implementation.DEFAULT_EXCLUDED_BRANCHES == ""


def test_empty_configuration_uses_python_defaults_without_github_io(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    for variable in (
        "DOWNSTREAM_REPOSITORY",
        "MANIFEST_PREFIX",
        "IMAGE_REPOSITORY",
        "EXCLUDED_BRANCHES",
    ):
        monkeypatch.setenv(variable, "")
    tekton_dir = copy_manifests(tmp_path)

    implementation.rewrite_tekton_filters(BRANCH, tekton_dir)

    assert (
        parameter(tekton_dir / implementation.OPERATOR_PUSH_FILE, "output-image")[
            "value"
        ]
        == f"{implementation.DEFAULT_IMAGE_REPOSITORY}:odh-stable"
    )
    assert implementation.configured_exclusions() == []

    monkeypatch.setenv("ODH_TOKEN", "test-token")
    monkeypatch.setenv("GITHUB_REPOSITORY", "opendatahub-io/mcp-lifecycle-operator")
    calls: list[tuple[str, ...]] = []

    def mock_git(*args: str, **kwargs: object) -> str:
        calls.append(args)
        if args == ("rev-parse", "HEAD"):
            return "target-main-sha"
        if args[0] == "ls-remote":
            return "" if args[-1] != "main" else "a" * 40 + "\trefs/heads/main\n"
        pytest.fail("unexpected git command")

    monkeypatch.setattr(implementation, "git", mock_git)
    implementation.main()

    target_url = "https://github.com/opendatahub-io/mcp-lifecycle-operator.git"
    downstream_url = (
        f"https://github.com/{implementation.DEFAULT_DOWNSTREAM_REPOSITORY}.git"
    )
    assert [call for call in calls if call[0] == "ls-remote"] == [
        ("ls-remote", "--exit-code", "--heads", downstream_url, "main"),
        ("ls-remote", "--exit-code", "--heads", target_url, "main"),
        ("ls-remote", "--heads", downstream_url),
    ]


@pytest.mark.parametrize("excluded", ["", " \n,\r\n "])
def test_empty_exclusions_exclude_no_branches(
    monkeypatch: pytest.MonkeyPatch, excluded: str
) -> None:
    monkeypatch.setenv("EXCLUDED_BRANCHES", excluded)
    assert implementation.configured_exclusions() == []


def test_exclusions_split_commas_and_newlines_and_strip_whitespace(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv(
        "EXCLUDED_BRANCHES",
        " rhoai-3.5 ,\r\n\trhoai-3.6-ea.1\n,  rhoai-3.7  ",
    )
    assert implementation.configured_exclusions() == [
        "rhoai-3.5",
        "rhoai-3.6-ea.1",
        "rhoai-3.7",
    ]


def test_workflow_defaults_permissions_and_shared_source_guard() -> None:
    workflow_path = (
        Path(__file__).parents[1] / "workflows/midstream-only-rhoai-releases.yml"
    )
    workflow = YAML(typ="rt").load(workflow_path.read_text())
    assert workflow["permissions"]["contents"] == "write"
    inputs = workflow["on"]["workflow_call"]["inputs"]
    assert all("default" not in input_config for input_config in inputs.values())

    job = workflow["jobs"]["provision"]
    assert job["if"] == "github.repository_owner == 'opendatahub-io'"
    assert set(workflow["on"]) == {"schedule", "workflow_dispatch", "workflow_call"}
    assert workflow["on"]["schedule"] == [{"cron": "17 */2 * * *"}]
    assert workflow["concurrency"] == {
        "group": "provision-rhoai-branches-${{ github.repository }}",
        "cancel-in-progress": False,
    }
    target_checkout = job["steps"][0]["with"]
    assert target_checkout["repository"] == "${{ github.repository }}"
    assert target_checkout["ref"] == "main"
    assert target_checkout["path"] == "target"
    assert target_checkout["persist-credentials"] is False
    assert target_checkout["token"] == "${{ secrets.target-token || github.token }}"
    run = job["steps"][-1]
    assert run["working-directory"] == "target"
    assert run["run"] == (
        "uv run --script ../provisioner/.github/scripts/midstream-only-rhoai-releases.py"
    )
    assert run["env"]["ODH_TOKEN"] == target_checkout["token"]
    source_checkout = next(
        step for step in job["steps"] if step["name"] == "Check out shared provisioner"
    )
    assert source_checkout["with"]["repository"] == "${{ job.workflow_repository }}"
    assert source_checkout["with"]["ref"] == "${{ job.workflow_sha }}"
    assert source_checkout["with"]["persist-credentials"] is False
    assert source_checkout["with"]["path"] == "provisioner"
    assert "token" not in source_checkout["with"]


def test_rewrites_renamed_manifest_and_image_configuration(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    tekton_dir = copy_manifests(tmp_path)
    prefix = "odh-mcp-lifecycle-module-operator"
    repository = "quay.io/opendatahub/odh-mcp-lifecycle-module-operator"
    exclusions = ["rhoai-3.6-ea.2", "rhoai-3.5", "rhoai-3.6-ea.1"]
    release_branch = "rhoai-3.7"
    for file in tuple(tekton_dir.glob("*.yaml")):
        renamed = file.name.replace("odh-mcp-lifecycle-operator", prefix)
        content = file.read_text().replace(
            "quay.io/opendatahub/odh-mcp-lifecycle-operator", repository
        )
        if "release-push" in renamed:
            content = content.replace(
                RELEASE_PREDICATE,
                implementation.release_predicate(exclusions),
            )
        file.rename(tekton_dir / renamed)
        (tekton_dir / renamed).write_text(content)
    monkeypatch.setenv("MANIFEST_PREFIX", prefix)
    monkeypatch.setenv("IMAGE_REPOSITORY", repository)
    monkeypatch.setenv("EXCLUDED_BRANCHES", ",\n".join(exclusions))
    release_files = [
        tekton_dir / f"{prefix}-release-push.yaml",
        tekton_dir / f"{prefix}-e2e-release-push.yaml",
    ]
    push_files = [
        tekton_dir / f"{prefix}-push.yaml",
        tekton_dir / f"{prefix}-e2e-push.yaml",
        *release_files,
    ]
    push_bytes = {file: file.read_bytes() for file in push_files}

    implementation.rewrite_tekton_filters(release_branch, tekton_dir)

    assert all(file.read_bytes() == content for file, content in push_bytes.items())
    assert parameter(tekton_dir / f"{prefix}-push.yaml", "output-image")["value"] == (
        f"{repository}:odh-stable"
    )
    assert (
        parameter(tekton_dir / f"{prefix}-e2e-push.yaml", "output-image")["value"]
        == f"{repository}-e2e:main"
    )
    assert parameter(tekton_dir / f"{prefix}-pull-request.yaml", "additional-tags")[
        "value"
    ] == ["odh-pr-{{revision}}"]
    assert all(
        annotation(file, implementation.CEL_ANNOTATION)
        == implementation.release_predicate(exclusions)
        for file in release_files
    )
    assert (
        annotation(
            tekton_dir / f"{prefix}-pull-request.yaml", implementation.CEL_ANNOTATION
        )
        == f'event == "pull_request" && target_branch == "{release_branch}"'
    )
    assert_push_selection(tekton_dir, prefix, exclusions, release_branch)


@pytest.mark.parametrize(
    ("variable", "value", "message"),
    [
        ("MANIFEST_PREFIX", "../escape", "safe filename prefix"),
        ("MANIFEST_PREFIX", "name.yaml", "safe filename prefix"),
        ("IMAGE_REPOSITORY", "quay.io/org/image:tag", "untagged registry/repository"),
        (
            "IMAGE_REPOSITORY",
            "quay.io/org/image@sha256:abc",
            "untagged registry/repository",
        ),
        ("IMAGE_REPOSITORY", "not-a-registry", "untagged registry/repository"),
    ],
)
def test_rejects_invalid_manifest_inputs_without_writing(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    variable: str,
    value: str,
    message: str,
) -> None:
    tekton_dir = copy_manifests(tmp_path)
    before = {file.name: file.read_bytes() for file in tekton_dir.glob("*.yaml")}
    monkeypatch.setenv(variable, value)

    with pytest.raises(implementation.ProvisioningError, match=message):
        implementation.rewrite_tekton_filters(BRANCH, tekton_dir)
    assert {
        file.name: file.read_bytes() for file in tekton_dir.glob("*.yaml")
    } == before


@pytest.mark.parametrize("excluded", ["rhoai-3.5,../main", '["rhoai-3.5"]'])
def test_rejects_invalid_excluded_branch_before_git(
    monkeypatch: pytest.MonkeyPatch, excluded: str
) -> None:
    monkeypatch.setenv("ODH_TOKEN", "token")
    monkeypatch.setenv("GITHUB_REPOSITORY", "target/operator")
    monkeypatch.setenv("DOWNSTREAM_REPOSITORY", "upstream/operator")
    monkeypatch.setenv("EXCLUDED_BRANCHES", excluded)
    monkeypatch.setattr(
        implementation, "git", lambda *args, **kwargs: pytest.fail("git must not run")
    )

    with pytest.raises(implementation.ProvisioningError, match="EXCLUDED_BRANCHES"):
        implementation.main()


def test_remote_ref_errors_only_treat_exit_two_as_absent(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        implementation,
        "git",
        lambda *args, **kwargs: (_ for _ in ()).throw(
            subprocess.CalledProcessError(2, args)
        ),
    )
    assert not implementation.remote_ref_exists(
        "https://github.com/o/r.git", "refs/heads/x"
    )
    monkeypatch.setattr(
        implementation,
        "git",
        lambda *args, **kwargs: (_ for _ in ()).throw(
            subprocess.CalledProcessError(128, args)
        ),
    )
    with pytest.raises(subprocess.CalledProcessError):
        implementation.remote_ref_exists("https://github.com/o/r.git", "refs/heads/x")


def test_git_auth_is_ephemeral_scoped_and_not_in_errors(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    secret = "token-do-not-leak"
    monkeypatch.setenv("ODH_TOKEN", secret)
    seen: dict[str, object] = {}

    def run(command: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
        seen.update(command=command, env=kwargs["env"])
        return subprocess.CompletedProcess(command, 128, "", "auth failure")

    monkeypatch.setattr(implementation.subprocess, "run", run)
    with pytest.raises(subprocess.CalledProcessError) as error:
        implementation.git("push", "https://github.com/o/r.git", "HEAD:refs/heads/x")
    assert secret not in str(error.value)
    assert secret not in " ".join(seen["command"])
    env = seen["env"]
    assert isinstance(env, dict)
    assert env["GIT_CONFIG_KEY_0"] == "http.https://github.com/.extraheader"
    assert (
        base64.b64encode(f"x-access-token:{secret}".encode()).decode()
        in env["GIT_CONFIG_VALUE_0"]
    )


def init_checkout(path: Path) -> None:
    path.mkdir()
    subprocess.run(["git", "init", "-q", str(path)], check=True)
    subprocess.run(["git", "-C", str(path), "config", "user.name", "Test"], check=True)
    subprocess.run(
        ["git", "-C", str(path), "config", "user.email", "test@example.com"], check=True
    )
    subprocess.run(
        ["git", "-C", str(path), "config", "commit.gpgsign", "false"], check=True
    )
    shutil.copytree(MANIFESTS, path / ".tekton")
    subprocess.run(["git", "-C", str(path), "add", ".tekton"], check=True)
    subprocess.run(["git", "-C", str(path), "commit", "-qm", "fixture"], check=True)


def test_push_new_branch_creates_ref_on_local_bare_remote(tmp_path: Path) -> None:
    checkout = tmp_path / "checkout"
    remote = tmp_path / "remote.git"
    init_checkout(checkout)
    subprocess.run(["git", "init", "--bare", "-q", str(remote)], check=True)

    implementation.push_new_branch(str(remote), BRANCH, checkout)

    assert (
        subprocess.run(
            ["git", "--git-dir", str(remote), "rev-parse", f"refs/heads/{BRANCH}"],
            check=True,
            capture_output=True,
            text=True,
        ).stdout.strip()
        == subprocess.run(
            ["git", "-C", str(checkout), "rev-parse", "HEAD"],
            check=True,
            capture_output=True,
            text=True,
        ).stdout.strip()
    )


def test_push_new_branch_rejects_concurrent_ref_without_changing_sha(
    tmp_path: Path,
) -> None:
    checkout = tmp_path / "checkout"
    remote = tmp_path / "remote.git"
    init_checkout(checkout)
    subprocess.run(["git", "init", "--bare", "-q", str(remote)], check=True)
    subprocess.run(
        ["git", "-C", str(checkout), "push", str(remote), "HEAD:refs/heads/main"],
        check=True,
        capture_output=True,
    )
    original_sha = subprocess.run(
        ["git", "-C", str(checkout), "rev-parse", "HEAD"],
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
    subprocess.run(
        [
            "git",
            "--git-dir",
            str(remote),
            "update-ref",
            f"refs/heads/{BRANCH}",
            original_sha,
        ],
        check=True,
    )
    (checkout / "new-file").write_text("local commit\n")
    subprocess.run(["git", "-C", str(checkout), "add", "new-file"], check=True)
    subprocess.run(
        ["git", "-C", str(checkout), "commit", "-qm", "provisioning commit"],
        check=True,
    )

    with pytest.raises(subprocess.CalledProcessError, match="exit status 1") as error:
        implementation.push_new_branch(str(remote), BRANCH, checkout)
    assert "Ref already exists" in (error.value.stderr or "")
    assert (
        subprocess.run(
            ["git", "--git-dir", str(remote), "rev-parse", f"refs/heads/{BRANCH}"],
            check=True,
            capture_output=True,
            text=True,
        ).stdout.strip()
        == original_sha
    )


def test_main_selects_exact_exclusions_and_commits_target_main(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    checkout = tmp_path / "target-checkout"
    init_checkout(checkout)
    restrictions = implementation.release_predicate(
        ["rhoai-3.5", "rhoai-3.6-ea.1", "rhoai-3.6-ea.2"]
    )
    for filename in (
        "odh-mcp-lifecycle-operator-release-push.yaml",
        "odh-mcp-lifecycle-operator-e2e-release-push.yaml",
    ):
        path = checkout / ".tekton" / filename
        path.write_text(path.read_text().replace(RELEASE_PREDICATE, restrictions))
    subprocess.run(["git", "add", ".tekton"], cwd=checkout, check=True)
    subprocess.run(
        ["git", "commit", "-qm", "configure exclusions"], cwd=checkout, check=True
    )
    monkeypatch.chdir(checkout)
    monkeypatch.setenv("ODH_TOKEN", "test-secret")
    monkeypatch.setenv("GITHUB_REPOSITORY", "opendatahub-io/mcp-lifecycle-operator")
    monkeypatch.setenv(
        "DOWNSTREAM_REPOSITORY", "red-hat-data-services/mcp-lifecycle-operator"
    )
    monkeypatch.setenv(
        "EXCLUDED_BRANCHES",
        "rhoai-3.5\nrhoai-3.6-ea.1\nrhoai-3.6-ea.2",
    )
    real_git = implementation.git
    target_url = "https://github.com/opendatahub-io/mcp-lifecycle-operator.git"
    downstream_url = (
        "https://github.com/red-hat-data-services/mcp-lifecycle-operator.git"
    )
    target_main = real_git(
        "rev-parse", "HEAD", cwd=checkout, capture_output=True
    ).strip()
    target_refs = {"rhoai-3.8-ea.2"}
    pushed: list[str] = []
    committed: dict[str, tuple[str, str, dict[str, str], list[str]]] = {}
    pushed_shas: dict[str, str] = {}
    branches = (
        "rhoai-3.4",
        "rhoai-3.5",
        "rhoai-3.6-ea.1",
        "rhoai-3.6-ea.2",
        "rhoai-3.7",
        "rhoai-3.8-ea.2",
        "rhoai-4.0",
        "feature/topic",
        "rhoai-3.9.1",
        "main",
    )
    refs = "".join(f"{'a' * 40}\trefs/heads/{branch}\n" for branch in branches)

    def mock_git(
        *args: str, cwd: Path | None = None, capture_output: bool = False
    ) -> str:
        if args[0] == "ls-remote":
            remote = (
                args[-2]
                if args[-1] == "main" or args[-1].startswith("refs/")
                else args[-1]
            )
            if remote == downstream_url and args[-1] == "main":
                return "a" * 40 + "\trefs/heads/main\n"
            if remote == target_url and args[-1] == "main":
                return "b" * 40 + "\trefs/heads/main\n"
            if remote == downstream_url:
                return refs
            if remote == target_url:
                branch = args[-1].removeprefix("refs/heads/")
                if branch in pushed_shas:
                    return pushed_shas[branch] + f"\trefs/heads/{branch}\n"
                if branch not in target_refs:
                    raise subprocess.CalledProcessError(2, ["git", *args])
                return "c" * 40 + f"\trefs/heads/{branch}\n"
        if "push" in args:
            push_index = args.index("push")
            assert args[push_index + 3] == target_url
            branch = args[-1].rsplit("/", 1)[-1]
            pushed.append(branch)
            commit = real_git("rev-parse", "HEAD", cwd=cwd, capture_output=True).strip()
            parent = real_git(
                "rev-parse", "HEAD^", cwd=cwd, capture_output=True
            ).strip()
            manifests = {
                filename: real_git(
                    "show", f"HEAD:.tekton/{filename}", cwd=cwd, capture_output=True
                )
                for filename in EXPECTED_FILES
            }
            changed_files = real_git(
                "diff-tree",
                "--no-commit-id",
                "--name-only",
                "-r",
                "HEAD",
                cwd=cwd,
                capture_output=True,
            ).splitlines()
            committed[branch] = (commit, parent, manifests, changed_files)
            pushed_shas[branch] = commit
            target_refs.add(branch)
            return ""
        return real_git(*args, cwd=cwd, capture_output=capture_output)

    monkeypatch.setattr(implementation, "git", mock_git)
    implementation.main()

    assert pushed == ["rhoai-3.4", "rhoai-3.7", "rhoai-4.0"]
    assert set(committed) == set(pushed)
    for branch, (_, parent, manifests, changed_files) in committed.items():
        assert parent == target_main
        assert changed_files == sorted(
            f".tekton/{filename}" for filename in implementation.REWRITE_FILES
        )
        for filename in implementation.REWRITE_FILES:
            assert f'target_branch == "{branch}"' in manifests[filename]
        for filename in EXPECTED_FILES - implementation.REWRITE_FILES:
            assert manifests[filename] == real_git(
                "show",
                f"{parent}:.tekton/{filename}",
                cwd=checkout,
                capture_output=True,
            )
    assert not list(checkout.glob(".git/worktrees/*"))
    assert "extraheader" not in (checkout / ".git/config").read_text()
    assert (
        subprocess.run(
            ["git", "status", "--porcelain"],
            cwd=checkout,
            check=True,
            capture_output=True,
            text=True,
        ).stdout
        == ""
    )
