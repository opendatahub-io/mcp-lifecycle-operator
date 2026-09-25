#!/usr/bin/env python3
# /// script
# requires-python = ">=3.13"
# dependencies = []
# ///
from __future__ import annotations

import importlib.util
from collections.abc import Callable
from pathlib import Path

implementation_path = Path(__file__).with_name("midstream-only-rhoai-releases.py")
implementation_spec = importlib.util.spec_from_file_location(
    "midstream_only_rhoai_releases",
    implementation_path,
)
if implementation_spec is None or implementation_spec.loader is None:
    raise ImportError(f"Cannot load {implementation_path}")
implementation = importlib.util.module_from_spec(implementation_spec)
implementation_spec.loader.exec_module(implementation)
rewrite_tekton_filters: Callable[[str, Path], None] = getattr(
    implementation, "rewrite_tekton_filters"
)


def test_rewrite_tekton_filters(tmp_path: Path) -> None:
    """Verify rewriting against the real .tekton annotation layout."""
    fixture = """annotations:
  build.appstudio.redhat.com/target_branch: '{{target_branch}}'
  pipelinesascode.tekton.dev/cancel-in-progress: "false"
  pipelinesascode.tekton.dev/max-keep-runs: "3"
  pipelinesascode.tekton.dev/on-cel-expression: event == "push" && target_branch
    == "main"
---
annotations:
  build.appstudio.redhat.com/target_branch: '{{target_branch}}'
  pipelinesascode.tekton.dev/cancel-in-progress: "true"
  pipelinesascode.tekton.dev/max-keep-runs: "3"
  pipelinesascode.tekton.dev/on-cel-expression: event == "pull_request" && target_branch == "main"
---
annotations:
  build.appstudio.redhat.com/target_branch: '{{target_branch}}'
  pipelinesascode.tekton.dev/cancel-in-progress: "false"
  pipelinesascode.tekton.dev/max-keep-runs: "3"
  pipelinesascode.tekton.dev/on-cel-expression: event == "push" && target_branch.matches("^rhoai-[A-Za-z0-9_][A-Za-z0-9_.-]{0,40}$")
"""
    tekton_dir = tmp_path / ".tekton"
    tekton_dir.mkdir()
    pipeline_file = tekton_dir / "pipelines.yaml"
    pipeline_file.write_text(fixture)

    rewrite_tekton_filters("rhoai-3.6-ea.1", tekton_dir)

    rewritten = pipeline_file.read_text()
    assert rewritten.count('== "rhoai-3.6-ea.1"') == 2
    assert (
        'event == "push" && target_branch.matches("^rhoai-[A-Za-z0-9_][A-Za-z0-9_.-]{0,40}$")'
        in rewritten
    )
    assert '== "main"' not in rewritten
