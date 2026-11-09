#!/usr/bin/env python3
"""Measure physical file lines and inclusive AST spans, across every build target.

Named functions and methods use their declared identity. Literals use a lexical
ordinal within their enclosing function or value declaration; inserting a sibling
may change its identity and therefore requires clearing any resulting hard debt.
Generated Go is measured too: a generated header alone proves no ownership.
Existing baseline debt remains release debt; this guard only prevents growth.
"""
import argparse
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import sys
import tempfile

BUDGETS = {
    "production": {"file_lines": (500, 1000), "function_lines": (80, 150),
                   "functions_per_file": (40, 60)},
    "test": {"file_lines": (800, 1500), "function_lines": (120, 200),
             "functions_per_file": (40, 60)},
}
EXCLUDED = {".git": "version-control metadata", ".tmp": "tool scratch",
            "vendor": "vendored dependencies", "node_modules": "dependencies",
            "references": "reference inputs"}
ROOT = Path(__file__).resolve().parents[1]


def limits(row):
    kind = "test" if row["path"].endswith("_test.go") else "production"
    return BUDGETS[kind][row["metric"]]


def key(row):
    return row["path"], row["symbol"], row["metric"]


def discover(root):
    paths, exclusions = [], []
    for parent, dirs, files in os.walk(root, followlinks=False):
        for name in sorted(dirs[:]):
            path = Path(parent) / name
            rel = path.relative_to(root).as_posix()
            if name in EXCLUDED:
                exclusions.append((rel, EXCLUDED[name]))
                dirs.remove(name)
            elif path.is_symlink():
                raise ValueError("unverifiable directory symlink: " + rel)
        for name in sorted(files):
            path = Path(parent) / name
            rel = path.relative_to(root).as_posix()
            if name in EXCLUDED:
                exclusions.append((rel, EXCLUDED[name]))
            elif name.endswith(".go"):
                if path.is_symlink():
                    raise ValueError("unverifiable Go symlink: " + rel)
                paths.append(rel)
    return sorted(paths), sorted(exclusions)


def build_parser(root, scratch):
    source = ROOT / "scripts/sizeratchet/parser.go.src"
    copied = scratch / "parser.go"
    shutil.copyfile(source, copied)
    binary = scratch / "parser"
    env = dict(os.environ, GOWORK="off", GOPROXY="off", GOSUMDB="off",
               GOTOOLCHAIN="local", GOFLAGS="-buildvcs=false")
    result = subprocess.run(["go", "build", "-o", str(binary), str(copied)],
                            cwd=root, env=env, capture_output=True, text=True,
                            timeout=120)
    if result.returncode:
        raise ValueError("parser build failed: " + result.stderr)
    return binary


def measure(root, binary, paths):
    result = subprocess.run([str(binary)], input=json.dumps(
        {"Root": str(root), "Paths": paths}), capture_output=True, text=True,
        timeout=120)
    if result.returncode:
        raise ValueError("Go parse failed: " + result.stderr)
    return json.loads(result.stdout)


def load_baseline(path):
    def unique_object(pairs):
        result = {}
        for name, value in pairs:
            if name in result:
                raise ValueError("duplicate JSON key: " + name)
            result[name] = value
        return result
    data = json.loads(path.read_text(), object_pairs_hook=unique_object)
    if (not isinstance(data, dict) or set(data) != {"version", "violations"}
            or type(data["version"]) is not int or data["version"] != 1
            or not isinstance(data["violations"], list)):
        raise ValueError("invalid baseline envelope")
    seen = set()
    for row in data["violations"]:
        if not isinstance(row, dict) or set(row) != {"path", "symbol", "metric", "value"}:
            raise ValueError("invalid baseline row")
        path, symbol, metric, value = (row[n] for n in ("path", "symbol", "metric", "value"))
        if (not isinstance(path, str) or not path.endswith(".go")
                or PurePosixPath(path).is_absolute() or ".." in path.split("/")
                or PurePosixPath(path).as_posix() != path
                or any(c in path for c in "*?[]\\")
                or any(part in EXCLUDED for part in path.split("/"))):
            raise ValueError("invalid baseline path")
        if (not isinstance(symbol, str) or not symbol.strip() or "*" == symbol
                or not isinstance(metric, str) or metric not in BUDGETS["production"] or type(value) is not int
                or (metric != "function_lines" and symbol != "<file>")
                or (metric == "function_lines" and symbol == "<file>")):
            raise ValueError("invalid baseline identity or value")
        if value <= limits(row)[1] or key(row) in seen:
            raise ValueError("baseline must contain unique exact hard violations")
        seen.add(key(row))
    return data["violations"]


def evaluate(rows, baseline):
    allowed = {key(row): row["value"] for row in baseline}
    soft, failures = [], []
    for row in rows:
        low, high = limits(row)
        if row["value"] > low:
            soft.append(row)
        if row["value"] > high and row["value"] > allowed.get(key(row), high):
            failures.append(row)
    return soft, failures


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument("--baseline", type=Path)
    args = parser.parse_args()
    root = args.root.resolve()
    try:
        baseline = load_baseline(args.baseline or root / "scripts/size-baseline.json")
        paths, exclusions = discover(root)
        for path, reason in exclusions:
            print(f"EXCLUDED {path}: {reason}")
        if not paths:
            raise ValueError("no Go files measured")
        scratch = root / ".tmp"
        scratch.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="sizeratchet-", dir=scratch) as work:
            binary = build_parser(root, Path(work))
            rows = measure(root, binary, paths)
        soft, failures = evaluate(rows, baseline)
        for row in soft:
            print("SOFT " + json.dumps(row, sort_keys=True))
        for row in failures:
            print("HARD " + json.dumps(row, sort_keys=True), file=sys.stderr)
        print(f"Measured {len(paths)} Go files; {len(soft)} soft violations; "
              f"{len(failures)} new/growing hard violations. Baseline debt must clear before release.")
        return int(bool(failures))
    except (ValueError, OSError, subprocess.TimeoutExpired) as error:
        print(str(error), file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
