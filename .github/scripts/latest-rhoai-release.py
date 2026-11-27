#!/usr/bin/env python3
import re
import sys
from collections.abc import Iterable


def latest_release(branches: Iterable[str]) -> str:
    releases = {}
    for branch in branches:
        branch = branch.strip()
        match = re.fullmatch(r"rhoai-([0-9]+)\.([0-9]+)(?:-ea\.([0-9]+))?", branch)
        if match:
            major, minor, ea = match.groups()
            version = (int(major), int(minor), ea is None, int(ea or 0))
            releases[version] = branch
    if not releases:
        raise ValueError("No RHOAI release branches found")
    return releases[max(releases)]


if __name__ == "__main__":
    try:
        print(latest_release(sys.stdin))
    except ValueError as error:
        sys.exit(str(error))
