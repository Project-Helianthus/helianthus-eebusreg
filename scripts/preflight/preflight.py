#!/usr/bin/env python3
"""Candidate preflight: QQ06 closure, then this repository's existing gates.

The candidate checkout is the working directory. The donor checkout is the
required --donor input and is passed unchanged to the QQ06 check command.
This command does not fetch, publish, or write a baseline.
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import subprocess
import sys
import tempfile
from pathlib import Path

MODULE_ROOT = Path(__file__).resolve().parents[2]
SCRIPTS = MODULE_ROOT / "scripts"
THIS_REPOSITORY = frozenset({"eebusreg", "helianthus-eebusreg"})


def load_script(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def git_output(repo, *args):
    completed = subprocess.run(
        ["git", "-C", str(repo), *args],
        text=True,
        capture_output=True,
        check=False,
    )
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "").strip()
        raise RuntimeError(detail or "git failed")
    return completed.stdout.strip()


def repository_name_from_url(url):
    name = url.rstrip("/").split("/")[-1]
    if name.endswith(".git"):
        name = name[:-4]
    return name


def tool_main(manifest_path):
    manifests = manifest_path.resolve().parent
    if manifests.name != "manifests" or manifests.parent.name != "acceptance":
        raise RuntimeError("manifest is not under acceptance/manifests")
    main = manifests.parent / "tools" / "manifest" / "main.py"
    if not main.is_file():
        raise RuntimeError("QQ06 main.py is not beside the manifest")
    return main


def destination_paths(manifest):
    paths = []
    for entry in manifest.get("destinations") or []:
        repository = entry.get("repository")
        path = entry.get("path")
        if repository in THIS_REPOSITORY and path and path not in paths:
            paths.append(path)
    for entry in manifest.get("paths") or []:
        if entry.get("repository") not in THIS_REPOSITORY:
            continue
        for path in entry.get("paths") or []:
            if path not in paths:
                paths.append(path)
    return paths


def outside_module(candidate, paths):
    outside = []
    if candidate.resolve() != MODULE_ROOT:
        return list(paths)
    for rel in paths:
        path = Path(rel)
        if path.is_absolute() or ".." in path.parts:
            outside.append(rel)
            continue
        resolved = (candidate / rel).resolve()
        try:
            resolved.relative_to(candidate)
        except ValueError:
            outside.append(rel)
    return outside


def donor_repository_finding(donor, manifest):
    expected = manifest.get("donor") or {}
    repository = expected.get("repository")
    if not repository:
        return "missing donor"
    try:
        url = git_output(donor, "remote", "get-url", "origin")
    except RuntimeError:
        return "donor-repository: " + str(repository)
    # Imported lazily so a missing map fails closed rather than matching a URL by coincidence.
    from routing import public_repository_name

    try:
        public = public_repository_name(repository)
    except KeyError:
        return "donor-repository: " + str(repository)
    if repository_name_from_url(url) != public:
        return "donor-repository: " + str(repository)
    return None


def closure_findings(manifest_path, donor):
    main = tool_main(manifest_path)
    completed = subprocess.run(
        [
            sys.executable,
            str(main),
            "check",
            "--manifest",
            str(manifest_path),
            "--donor",
            str(donor),
        ],
        text=True,
        capture_output=True,
        check=False,
    )
    lines = []
    for line in completed.stdout.splitlines():
        if line and line != "check: ok":
            lines.append(line)
    if completed.returncode != 0 and not lines:
        detail = (completed.stderr or completed.stdout or "check failed").strip()
        if detail:
            lines.append(detail)
    return lines


def size_report(candidate):
    size = load_script("size_ratchet", SCRIPTS / "check_size_ratchet.py")
    baseline = size.load_baseline(candidate / "scripts" / "size-baseline.json")
    paths, _exclusions = size.discover(candidate)
    soft_lines = []
    findings = []
    if not paths:
        return soft_lines, ["no Go files measured"]
    scratch = candidate / ".tmp"
    scratch.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="sizeratchet-", dir=scratch) as work:
        binary = size.build_parser(candidate, Path(work))
        rows = size.measure(candidate, binary, paths)
    try:
        soft, failures = size.evaluate(rows, baseline)
    except ValueError as error:
        return soft_lines, [str(error)]
    for row in soft:
        soft_lines.append("SOFT " + json.dumps(row, sort_keys=True))
    for row in failures:
        _low, high = size.limits(row)
        findings.append(
            "split required: {path} {symbol} {metric} {value} > {limit}".format(
                path=row["path"],
                symbol=row["symbol"],
                metric=row["metric"],
                value=row["value"],
                limit=high,
            )
        )
    return soft_lines, findings


def name_findings(candidate, destinations):
    command = [sys.executable, str(SCRIPTS / "check_behavior_names.py"), "--check"]
    if candidate.resolve() != MODULE_ROOT:
        command.extend(
            [
                "--root",
                str(candidate),
                "--baseline",
                str(MODULE_ROOT / "scripts" / "behavior-name-baseline.json"),
            ]
        )
    completed = subprocess.run(command, text=True, capture_output=True, check=False)
    findings = []
    if completed.returncode == 2:
        detail = (completed.stderr or completed.stdout or "behavior-name scan failed").strip()
        return [detail or "behavior-name scan failed"]
    for line in completed.stdout.splitlines():
        if not line or line.startswith("behavior-name guard:"):
            continue
        path = line.split(":", 1)[0]
        if path in destinations:
            findings.append("rename or fix required: " + line)
        else:
            findings.append(line)
    return findings


def lint_findings(destinations):
    completed = subprocess.run(
        [sys.executable, str(SCRIPTS / "check_go_lint_baseline.py"), "--check"],
        text=True,
        capture_output=True,
        check=False,
    )
    findings = []
    for line in completed.stderr.splitlines():
        if not line.strip():
            continue
        if not (
            line.startswith("new or increased debt")
            or line.startswith("unused exemption")
            or line.startswith("lint baseline failed:")
        ):
            continue
        if any(path in line for path in destinations):
            findings.append("rename or fix required: " + line)
        else:
            findings.append(line)
    if completed.returncode != 0 and not findings:
        detail = (completed.stderr or completed.stdout or "lint baseline failed").strip()
        if detail:
            findings.append(detail)
    return findings


def missing_destinations(candidate, paths):
    missing = []
    for rel in paths:
        path = candidate / rel
        if not path.is_file():
            missing.append("missing destination: " + rel)
    return missing


def run(manifest_path, donor, candidate):
    manifest_path = Path(manifest_path)
    donor = Path(donor)
    candidate = Path(candidate).resolve()
    soft_lines = []
    findings = []
    if not manifest_path.is_file():
        return soft_lines, ["missing manifest"]
    if not donor.is_dir():
        return soft_lines, ["missing donor"]
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    repository_finding = donor_repository_finding(donor, manifest)
    if repository_finding:
        findings.append(repository_finding)
    findings.extend(closure_findings(manifest_path, donor))
    destinations = destination_paths(manifest)
    findings.extend(missing_destinations(candidate, destinations))
    size_soft, size_findings = size_report(candidate)
    soft_lines.extend(size_soft)
    findings.extend(size_findings)
    outside = outside_module(candidate, destinations)
    if outside:
        findings.append(
            "rule 11: destination file is not in this module: " + ", ".join(outside)
        )
        return soft_lines, findings
    findings.extend(name_findings(candidate, set(destinations)))
    findings.extend(lint_findings(set(destinations)))
    return soft_lines, findings


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", required=True)
    parser.add_argument("--donor", required=True)
    args = parser.parse_args(argv)
    try:
        soft_lines, findings = run(args.manifest, args.donor, Path.cwd())
    except (OSError, RuntimeError, ValueError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        print(str(error))
        return 1
    for line in findings:
        print(line)
    for line in soft_lines:
        print(line)
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
