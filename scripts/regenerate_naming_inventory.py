#!/usr/bin/env python3
"""Regenerate naming-inventory.json from its pinned published base and the public locator map.

The only inputs are the published naming-inventory.json read from an explicit
git revision and naming-repository-map.json. The base is accepted only when its
sha256 equals the digest recorded in the map. Only the strings of each row's
consumer_files are translated; every other byte of the base is preserved.
"""
import argparse
import hashlib
import json
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_MAP = ROOT / "naming-repository-map.json"
DEFAULT_OUTPUT = ROOT / "naming-inventory.json"


def load_map(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def build_translation(locator_map):
    prefixes = [
        (entry["old_locator"] + "/", entry["new_locator"] + "/")
        for entry in locator_map["repositories"]
    ]
    split = locator_map["split"]
    old_prefix = split["old_locator"] + "/"
    retained = {entry["owner_locator"] + "/" + entry["path"] for entry in split["retained"]}
    moved = {}
    for destination in split["destinations"]:
        for page in destination["paths"]:
            old = old_prefix + page
            if old in retained:
                continue
            if old in moved:
                raise SystemExit("locator path mapped twice: " + old)
            moved[old] = destination["new_locator"] + "/" + page
    return prefixes, moved


def translate(value, prefixes, moved):
    if value in moved:
        return moved[value]
    for old, new in prefixes:
        if value.startswith(old):
            return new + value[len(old):]
    return value


def regenerate(base_bytes, locator_map):
    expected = locator_map["base"]["sha256"]
    actual = hashlib.sha256(base_bytes).hexdigest()
    if actual != expected:
        raise SystemExit("base digest " + actual + " does not match the map digest " + expected)
    prefixes, moved = build_translation(locator_map)
    inventory = json.loads(base_bytes.decode("utf-8"))
    for row in inventory["rows"]:
        row["consumer_files"] = [translate(value, prefixes, moved) for value in row["consumer_files"]]
    return (json.dumps(inventory, indent=2, ensure_ascii=False) + "\n").encode("utf-8")


def read_base(revision, path, root=ROOT):
    result = subprocess.run(
        ["git", "-C", str(root), "show", revision + ":" + path],
        check=True,
        stdout=subprocess.PIPE,
    )
    return result.stdout


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-rev", required=True, help="git revision holding the published base")
    parser.add_argument("--map", default=str(DEFAULT_MAP))
    parser.add_argument("--output", default=str(DEFAULT_OUTPUT))
    args = parser.parse_args(argv)
    locator_map = load_map(args.map)
    base = read_base(args.base_rev, locator_map["base"]["path"])
    Path(args.output).write_bytes(regenerate(base, locator_map))


if __name__ == "__main__":
    main()
