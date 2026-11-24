"""Synthetic preflight, closure, and additive-route tests.

Revisions and digests are computed while a case runs. The three baseline
files are compared with their bytes from before the run.
"""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import sys
import unittest
from pathlib import Path

import routing

REPO = Path(__file__).resolve().parents[2]
BASELINES = (
    "scripts/size-baseline.json",
    "scripts/behavior-name-baseline.json",
    "scripts/go-lint-baseline.json",
)
WORKFLOWS = (
    ".github/workflows/ci.yml",
    ".github/workflows/size-ratchet.yml",
    ".github/workflows/behavior-names.yml",
    ".github/workflows/go-readability.yml",
)
INPUTS = {"raw": "020001000300", "qualifiers": "header", "clock": "t0", "state": "idle"}
MEAS = {
    "driver_instance": "drv",
    "remote_device": "remote-1",
    "entity": "ent-1",
    "feature_type": "Measurement",
    "function": "measurementListData",
    "measurementId": "326",
    "scopeType": "instant",
    "unit": "V",
    "valueType": "voltage",
}
EXEMPLAR_PATH = "driver/examples/peer/measurement_rows.go"
ORIGINALS = {}


def setUpModule():
    scratch = REPO / ".tmp"
    scratch.mkdir(parents=True, exist_ok=True)
    (scratch / "gocache").mkdir(exist_ok=True)
    (scratch / "lint").mkdir(exist_ok=True)
    os.environ["TMPDIR"] = str(scratch)
    os.environ["GOCACHE"] = str(scratch / "gocache")
    os.environ["GOLANGCI_LINT_CACHE"] = str(scratch / "lint")
    os.environ["GOWORK"] = "off"
    os.environ["GOFLAGS"] = "-mod=mod"
    os.environ["GOTOOLCHAIN"] = "local"
    os.environ["GOPROXY"] = "off"
    os.environ["GOSUMDB"] = "off"
    os.environ["PYTHONDONTWRITEBYTECODE"] = "1"
    for name in BASELINES:
        ORIGINALS[name] = (REPO / name).read_bytes()


def tool_root():
    raw = os.environ.get("DOCS_SEMANTIC_CHECKOUT", "")
    if not raw:
        raise AssertionError("DOCS_SEMANTIC_CHECKOUT is required")
    root = Path(raw)
    main = root / "acceptance" / "tools" / "manifest" / "main.py"
    if not main.is_file():
        raise AssertionError("QQ06 main.py is not in the tool checkout")
    return root


def detail(completed):
    return "exit={0}\nSTDOUT:\n{1}\nSTDERR:\n{2}".format(
        completed.returncode, completed.stdout, completed.stderr
    )


def git(repo, *args, check=True):
    completed = subprocess.run(
        [
            "git", "-C", str(repo),
            "-c", "user.email=manifest@example.com",
            "-c", "user.name=Manifest Fixture",
            "-c", "commit.gpgsign=false",
            "-c", "core.autocrlf=false",
            *args,
        ],
        text=True,
        capture_output=True,
        check=False,
    )
    if check and completed.returncode != 0:
        raise RuntimeError(detail(completed))
    return completed


def git_out(repo, *args):
    return git(repo, *args).stdout.strip()


def move_driver():
    path = REPO / "driver"
    if not path.exists():
        return
    sink = REPO / ".tmp" / "preflight-created"
    sink.mkdir(parents=True, exist_ok=True)
    shutil.move(str(path), str(sink / ("driver-" + str(len(list(sink.iterdir()))))))


def write_inbound(name, text):
    directory = REPO / "driver" / "rows" / "inbound"
    directory.mkdir(parents=True, exist_ok=True)
    (directory / name).write_text(text, encoding="utf-8")


def package_file(function, body):
    return (
        "// Package inbound is a synthetic destination package.\n"
        "package inbound\n"
        "\n"
        "// {0} is a synthetic destination function.\n"
        "{1}\n"
    ).format(function, body)


def run_preflight(manifest, donor):
    return subprocess.run(
        [
            sys.executable,
            str(REPO / "scripts/preflight/preflight.py"),
            "--manifest", str(manifest),
            "--donor", str(donor),
        ],
        cwd=str(REPO),
        text=True,
        capture_output=True,
        check=False,
        timeout=600,
    )


def load_signature(tool):
    text = (tool / "acceptance" / "tools" / "manifest" / "testdata" / "exemplar" / "measurement_rows.go").read_text(encoding="utf-8")
    payload = text.split("QQ06ROW", 1)[1].strip()
    return json.loads(payload)["signature"]


def selector_map(signature, values):
    return {field: values[field] for field in signature["selector_fields"]}


def fill(signature, values):
    return {field: values[field] for field in signature["selector_fields"]}


class BaselineCase(unittest.TestCase):
    def setUp(self):
        move_driver()

    def tearDown(self):
        move_driver()
        for name, payload in ORIGINALS.items():
            self.assertEqual((REPO / name).read_bytes(), payload, name)


def suppressing_result(**overrides):
    result = {
        "head_sha": "head-one",
        "base_sha": "base-one",
        "docs_semantic_sha": "docs-one",
        "manifest": "acceptance/manifests/peer-batch.json",
        "classification": "proven additive-only",
        "suppress_full_suite": True,
        "edits_export_inventory_registration": False,
        "run_package_tests": True,
        "run_compare": True,
        "run_preflight": True,
    }
    result.update(overrides)
    return result


def pull(number, head_ref, head_repo, base_ref, base_repo):
    return {
        "number": number,
        "head": {"ref": head_ref, "repo": None if head_repo is None else {"id": head_repo}},
        "base": {"ref": base_ref, "repo": None if base_repo is None else {"id": base_repo}},
    }


class RoutingTest(BaselineCase):
    """Each identity field is varied on its own.

    head_sha catches a saved result applied to another head.
    base_sha catches a retargeted base.
    docs_semantic_sha catches a result with no recorded tool revision.
    manifest catches a no-match result treated as selected.
    classification catches a tip or unresolved result with the suppress flag set.
    suppress_full_suite catches a suppressing class whose flag was cleared.
    edits_export_inventory_registration catches a registration edit inside a suppressing class.
    head_repo_id and base_repo_id catch a same-named ref in another repository.
    head_ref and base_ref catch a ref that no longer meets the parent or the child.
    """

    def test_each_result_field_changes_suppression(self):
        saved = suppressing_result()
        self.assertTrue(routing.consumer_suppresses(saved, "head-one", "base-one"))
        self.assertEqual(
            routing.test_routes(saved, "pull_request", "head-one", "base-one"),
            ["fast-path"],
        )
        cases = (
            ("head_sha", "head-two"),
            ("base_sha", "base-two"),
            ("docs_semantic_sha", ""),
            ("manifest", None),
            ("classification", "tip"),
            ("classification", "unresolved"),
            ("classification", "no applicable manifest"),
            ("suppress_full_suite", False),
            ("edits_export_inventory_registration", True),
        )
        for field, value in cases:
            with self.subTest(field=field, value=value):
                mutated = suppressing_result(**{field: value})
                self.assertFalse(routing.consumer_suppresses(mutated, "head-one", "base-one"))
                self.assertIn(
                    "full-suite",
                    routing.test_routes(mutated, "pull_request", "head-one", "base-one"),
                )

    def test_resolved_lower_member_suppresses_without_a_registration_edit(self):
        saved = suppressing_result(
            classification="resolved lower member",
            run_preflight=False,
        )
        self.assertTrue(routing.consumer_suppresses(saved, "head-one", "base-one"))
        self.assertEqual(
            routing.test_routes(saved, "pull_request", "head-one", "base-one"),
            ["package-and-compare"],
        )

    def test_stack_ids_and_refs(self):
        current = {
            "number": 1,
            "head_repo_id": 10,
            "head_ref": "feature",
            "base_repo_id": 10,
            "base_ref": "main",
        }
        child = pull(2, "follow", 10, "feature", 10)
        parent = pull(3, "main", 10, "older", 10)
        self.assertEqual(routing.stack_position(current, [child], True), "lower")
        self.assertEqual(routing.stack_position(current, [parent], True), "tip")
        self.assertEqual(routing.stack_position(current, [parent, child], True), "lower")
        self.assertEqual(routing.stack_position(current, [], True), "unstacked")
        self.assertEqual(routing.stack_position(current, [child], False), "unresolved")
        missing = dict(current)
        missing["head_repo_id"] = None
        self.assertEqual(routing.stack_position(missing, [child], True), "unresolved")
        self.assertEqual(
            routing.stack_position(current, [pull(2, "follow", 10, "feature", 11)], True),
            "unstacked",
        )
        self.assertEqual(
            routing.stack_position(current, [pull(2, "follow", 10, "other", 10)], True),
            "unstacked",
        )
        self.assertEqual(
            routing.stack_position(current, [pull(3, "other", 10, "older", 10)], True),
            "unstacked",
        )
        self.assertEqual(
            routing.stack_position(current, [pull(3, "main", 11, "older", 10)], True),
            "unstacked",
        )
        self.assertEqual(
            routing.stack_position(current, [pull(4, "elsewhere", None, "unrelated", None)], True),
            "unstacked",
        )
        self.assertEqual(
            routing.stack_position(current, [pull(4, "follow", None, "feature", 10)], True),
            "unresolved",
        )

    def test_no_match_stays_one_full_suite_after_a_later_manifest(self):
        saved = suppressing_result(
            manifest=None,
            classification="no applicable manifest",
            suppress_full_suite=False,
            run_package_tests=False,
            run_compare=False,
            run_preflight=False,
        )
        covering = [{
            "paths": [{
                "repository": "eebusreg",
                "paths": ["driver/rows/inbound/helper.go"],
            }],
            "donor": {"repository": "gateway"},
        }]
        selected, _manifest = routing.select_manifest(
            [("acceptance/manifests/later.json", covering[0])],
            ["driver/rows/inbound/helper.go"],
        )
        self.assertEqual(selected, "acceptance/manifests/later.json")
        self.assertEqual(
            routing.test_routes(saved, "pull_request", "head-one", "base-one"),
            ["full-suite"],
        )
        self.assertFalse(saved["run_package_tests"])
        self.assertFalse(saved["run_compare"])

    def test_schedule_runs_the_full_suite_without_reading_a_result(self):
        saved = suppressing_result()
        self.assertIsNone(routing.NIGHTLY_CRON)
        self.assertFalse(routing.reads_routing_result("schedule"))
        self.assertTrue(routing.full_suite_runs("schedule", "true"))
        self.assertEqual(
            routing.test_routes(saved, "schedule", "head-one", "base-one"),
            ["full-suite"],
        )
        self.assertTrue(routing.full_suite_runs("push", "true"))
        self.assertFalse(routing.reads_routing_result("push"))

    def test_missing_and_unreadable_results_keep_every_full_suite(self):
        self.assertEqual(routing.test_routes(None, "pull_request", "head-one", "base-one"), ["full-suite"])
        self.assertFalse(routing.consumer_suppresses("{", "head-one", "base-one"))
        self.assertFalse(routing.consumer_suppresses([], "head-one", "base-one"))
        foreign = suppressing_result(head_sha="head-two")
        self.assertFalse(routing.consumer_suppresses(foreign, "head-one", "base-one"))
        self.assertTrue(routing.full_suite_runs("pull_request", "false"))

    def test_read_result_without_a_token_exits_zero_and_does_not_suppress(self):
        env = os.environ.copy()
        env.pop("GITHUB_TOKEN", None)
        env["GITHUB_REPOSITORY"] = "Project-Helianthus/helianthus-eebusreg"
        output = REPO / ".tmp" / "github-output-read.txt"
        env["GITHUB_OUTPUT"] = str(output)
        completed = subprocess.run(
            [
                sys.executable,
                str(REPO / "scripts/preflight/routing.py"),
                "read-result",
                "--head", "head-one",
                "--base", "base-one",
            ],
            cwd=str(REPO),
            env=env,
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(completed.returncode, 0, detail(completed))
        self.assertIn("suppress=false", output.read_text(encoding="utf-8"))

    def test_recorded_tool_sha_is_the_checkout_and_a_mismatch_is_not(self):
        work = REPO / ".tmp" / "recorded-tool"
        if work.exists():
            shutil.move(str(work), str(work.parent / ("recorded-tool-old-" + str(len(list(work.parent.iterdir()))))))
        work.mkdir(parents=True)
        source = work / "source"
        source.mkdir()
        git(source, "init", "-b", "main")
        (source / "marker.txt").write_text("first\n", encoding="utf-8")
        git(source, "add", "marker.txt")
        git(source, "commit", "-m", "first")
        first = git_out(source, "rev-parse", "HEAD")
        (source / "marker.txt").write_text("second\n", encoding="utf-8")
        git(source, "add", "marker.txt")
        git(source, "commit", "-m", "second")
        result_path = work / "routing-result.json"
        result_path.write_text(json.dumps(suppressing_result(docs_semantic_sha=first)), encoding="utf-8")
        git(source, "config", "uploadpack.allowReachableSHA1InWant", "true")
        git(source, "config", "uploadpack.allowAnySHA1InWant", "true")
        dest = work / "tool"
        checked = routing.checkout_tool(dest, "head-one", "base-one", result_path, url=str(source))
        self.assertEqual(checked, first)
        self.assertEqual(git_out(dest, "rev-parse", "HEAD"), first)
        other = work / "other-tool"
        resolved = routing.checkout_tool(other, "head-two", "base-one", result_path, url=str(source))
        self.assertEqual(resolved, git_out(source, "rev-parse", "HEAD"))
        self.assertNotEqual(resolved, first)

    def test_selection_coverage_and_additive_qualification(self):
        helper = "driver/rows/inbound/helper.go"
        peer = "internal/eebusfacade/remote_peer.go"
        sealed = {helper}
        self.assertTrue(routing.proven_additive([("A", helper)], sealed))
        self.assertFalse(routing.proven_additive([("M", helper)], sealed))
        self.assertFalse(routing.proven_additive([("R", helper)], sealed))
        self.assertFalse(routing.proven_additive([("A", "driver/rows/inbound/other.go")], sealed))
        self.assertFalse(routing.proven_additive([("A", helper), ("A", peer)], sealed | {peer}))
        self.assertFalse(routing.proven_additive([], sealed))
        self.assertTrue(routing.is_registration_path("internal/apiboundary/export_inventory.go"))
        self.assertTrue(routing.is_registration_path("internal/apiboundary/testdata/exports/driver.txt"))
        self.assertFalse(routing.is_registration_path("internal/apiboundary/export_inventory_test.go"))
        self.assertEqual(routing.packages_of([helper, "scripts/preflight/preflight.py"]), ["./driver/rows/inbound"])
        self.assertEqual(routing.packages_of(["scripts/preflight/preflight.py", ".github/workflows/ci.yml"]), [])
        self.assertEqual(routing.packages_of(["root.go"]), ["."])
        donor_only = {
            "donor": {"repository": "gateway"},
            "paths": [{"repository": "gateway", "paths": [helper]}],
        }
        self.assertEqual(routing.select_manifest([("a.json", donor_only)], [helper]), (None, None))
        small = {
            "donor": {"repository": "gateway"},
            "paths": [{"repository": "eebusreg", "paths": [helper]}],
        }
        large = {
            "donor": {"repository": "gateway"},
            "paths": [{"repository": "helianthus-eebusreg", "paths": [helper, peer]}],
        }
        chosen, body = routing.select_manifest([("large.json", large), ("small.json", small)], [helper])
        self.assertEqual(chosen, "small.json")
        self.assertEqual(body, small)
        self.assertEqual(
            routing.select_manifest([("one.json", small), ("two.json", dict(small))], [helper])[0],
            "ambiguous",
        )
        self.assertEqual(routing.select_manifest([("small.json", small)], []), (None, None))
        decision = routing.decide("lower", True, True, True, True)
        self.assertEqual(decision["classification"], "resolved lower member")
        self.assertFalse(decision["suppress_full_suite"])
        self.assertTrue(decision["run_package_tests"])
        self.assertTrue(decision["run_compare"])
        self.assertFalse(decision["run_preflight"])
        routes = routing.test_routes(
            routing.routing_result("head-one", "base-one", "docs-one", "m.json", decision),
            "pull_request",
            "head-one",
            "base-one",
        )
        self.assertEqual(routes, ["package-and-compare", "full-suite"])
        invalid = routing.decide("lower", True, True, False, False)
        self.assertEqual(invalid["classification"], "selected manifest failed validation")
        self.assertNotEqual(invalid["classification"], "no applicable manifest")
        self.assertTrue(invalid["fail"])
        self.assertFalse(invalid["suppress_full_suite"])
        self.assertFalse(invalid["run_package_tests"])
        tip = routing.decide("tip", True, True, False, True)
        self.assertEqual(tip["classification"], "tip")
        self.assertFalse(tip["run_package_tests"])
        self.assertFalse(tip["run_compare"])
        self.assertFalse(tip["suppress_full_suite"])
        unresolved = routing.decide("unresolved", True, False, False, True)
        self.assertEqual(unresolved["classification"], "unresolved")
        self.assertTrue(unresolved["run_compare"])
        self.assertFalse(unresolved["suppress_full_suite"])
        unstacked = routing.decide("unstacked", True, False, False, True)
        self.assertEqual(unstacked["classification"], "unstacked")
        self.assertTrue(unstacked["run_compare"])
        self.assertFalse(unstacked["suppress_full_suite"])
        none = routing.decide("unstacked", False, False, False, False)
        self.assertEqual(none["classification"], "no applicable manifest")
        self.assertFalse(none["run_package_tests"])
        self.assertFalse(none["run_compare"])
        ambiguous = routing.decide("lower", True, True, False, False, ambiguous=True)
        self.assertEqual(ambiguous["classification"], "ambiguous manifest")
        self.assertTrue(ambiguous["fail"])
        self.assertNotEqual(ambiguous["classification"], "no applicable manifest")
        self.assertEqual(
            routing.public_repository_name("helianthus-ebusgateway"),
            "helianthus-gateway",
        )
        self.assertTrue(
            routing.public_repository_url("gateway").endswith("/helianthus-gateway.git")
        )

    def test_name_status_keeps_an_added_path_and_a_rename_target(self):
        changes = routing.parse_name_status(
            "A\tdriver/rows/inbound/helper.go\n"
            "M\tdriver/rows/inbound/edited.go\n"
            "R100\tdriver/rows/old.go\tdriver/rows/inbound/helper.go\n"
        )
        self.assertEqual(changes[0], ("A", "driver/rows/inbound/helper.go"))
        self.assertEqual(changes[1], ("M", "driver/rows/inbound/edited.go"))
        self.assertEqual(changes[2], ("R", "driver/rows/inbound/helper.go"))


class WorkflowTest(BaselineCase):
    def text(self, name):
        return (REPO / name).read_text(encoding="utf-8")

    def test_consumers_read_the_saved_result_and_do_not_schedule(self):
        for name in WORKFLOWS:
            body = self.text(name)
            with self.subTest(name=name):
                self.assertIn(routing.FULL_SUITE_IF, body)
                self.assertIn(routing.READ_RESULT_IF, body)
                self.assertIn("read-result", body)
                self.assertNotIn("schedule:", body)
                self.assertNotIn("cron:", body)
                self.assertIn("push:", body)
        additive = self.text(".github/workflows/additive-rows.yml")
        self.assertIn("pull_request:", additive)
        self.assertNotIn("push:", additive)
        self.assertNotIn("schedule:", additive)
        self.assertNotIn("cron:", additive)
        self.assertNotIn("workflow_dispatch:", additive)
        self.assertNotIn("permissions:", additive)
        self.assertNotIn("scripts/ci_local.sh", additive)
        self.assertIn("actions/checkout@v4", additive)
        self.assertIn("clean: true", additive)
        self.assertIn("persist-credentials: false", additive)
        self.assertIn("go-version: '1.22'", additive)
        self.assertIn("additive-routing-result", additive)
        order = [
            "go test -race -count=1",
            " compare ",
            "golangci-lint/v2/cmd/golangci-lint@v2.11.4",
            "scripts/preflight/preflight.py",
            "scripts/check_size_ratchet.py",
            "scripts/check_behavior_names.py",
            "scripts/check_go_lint_baseline.py",
            "go run ./internal/apiboundary",
            "TestExportInventoryDrift",
        ]
        positions = [additive.index(token) for token in order]
        self.assertEqual(positions, sorted(positions))
        ci = self.text(".github/workflows/ci.yml")
        self.assertIn(
            "HELIANTHUS_EEBUSREG_MAX_GO_VERSION=1.22 GOTOOLCHAIN=local ./scripts/ci_local.sh",
            ci,
        )
        self.assertIn("./scripts/toolchain_boundary_proof.sh", ci)
        self.assertIn("DOCS_SEMANTIC_CHECKOUT:", ci)
        self.assertIn("python3 -m unittest discover -s scripts/preflight -p 'test_*.py'", ci)
        generate = ci.split("- name: Generate API boundary manifest\n", 1)[1]
        self.assertTrue(generate.startswith("        run:"))
        upload = ci.split("- name: Upload API boundary manifest\n", 1)[1]
        self.assertTrue(upload.startswith("        uses:"))
        surface = ci.split("api-surface-attestation:", 1)[1]
        self.assertNotIn("read-result", surface)
        self.assertNotIn("suppress", surface)
        self.assertFalse(any(line.startswith("permissions:") for line in ci.splitlines()))
        self.assertIn("workflow_dispatch:", ci)
        for name in (
            ".github/workflows/size-ratchet.yml",
            ".github/workflows/behavior-names.yml",
            ".github/workflows/go-readability.yml",
        ):
            body = self.text(name)
            self.assertIn("actions: read", body)
            self.assertIn("contents: read", body)
            self.assertNotIn("workflow_dispatch:", body)
        readability = self.text(".github/workflows/go-readability.yml")
        self.assertIn(
            'GOBIN="$RUNNER_TEMP/golangci-bin" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4',
            readability,
        )
        proof = ci.split("- name: Run toolchain proof in build container\n", 1)[1]
        self.assertTrue(proof.startswith("        if: " + routing.FULL_SUITE_IF))


class ToolFixture:
    def __init__(self, root, source):
        self.root = root
        self.dest = root.parent / "dest"
        self.sha = ""
        self.signature = load_signature(source)
        self._build(source)

    def _build(self, source):
        donor_files = source / "acceptance" / "tools" / "manifest" / "testdata" / "donor" / "closed"
        for name in ("moved.go", "shared.go"):
            target = self.root / "closure" / "closed" / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy(donor_files / name, target)
        exemplar = self.root / EXEMPLAR_PATH
        exemplar.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy(
            source / "acceptance" / "tools" / "manifest" / "testdata" / "exemplar" / "measurement_rows.go",
            exemplar,
        )
        tool = self.root / "acceptance" / "tools" / "manifest"
        tool.mkdir(parents=True, exist_ok=True)
        for name in ("main.py", "manifestlib.py", "closure.py", "gitstore.py", "patterns.py"):
            shutil.copy(source / "acceptance" / "tools" / "manifest" / name, tool / name)
        (self.root / "acceptance" / "manifests").mkdir(parents=True, exist_ok=True)
        selectors = selector_map(self.signature, MEAS)
        self._write_json("vec/meas.json", {"fields": {"reading": "230"}, "inputs": {}, "selectors": selectors})
        self._write_json("vec/north.json", {"fields": {"kept": "1"}, "inputs": {}, "selectors": selectors})
        self._write_json("vec/diff-north.json", {
            "approval": "approved-transition",
            "case": "north",
            "field": "northbound",
            "new": "present",
            "old": None,
            "old_present": False,
            "source": "synthetic-lead",
        })
        git(self.root, "init", "-b", "main")
        git(self.root, "add", "-A")
        git(self.root, "commit", "-m", "fixture")
        self.sha = git_out(self.root, "rev-parse", "HEAD")
        git(self.root, "remote", "add", "origin", "https://github.com/Project-Helianthus/helianthus-gateway.git")
        shutil.copytree(
            source / "acceptance" / "tools" / "manifest" / "testdata" / "destination",
            self.dest,
        )
        git(self.dest, "init", "-b", "main")
        git(self.dest, "add", "-A")
        git(self.dest, "commit", "-m", "destination")

    def _write_json(self, rel, body):
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(body, indent=2, sort_keys=True) + "\n", encoding="utf-8")

    def main(self):
        return self.root / "acceptance" / "tools" / "manifest" / "main.py"

    def manifest_path(self, row):
        return self.root / "acceptance" / "manifests" / (row + ".json")

    def run(self, *args):
        return subprocess.run(
            [sys.executable, str(self.main()), *args],
            cwd=str(self.root),
            env=os.environ.copy(),
            text=True,
            capture_output=True,
            check=False,
        )

    def generate(self, spec):
        path = self.root / "specs" / (spec["row"] + ".json")
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(spec), encoding="utf-8")
        return self.run("generate", "--input", str(path), "--donor", str(self.root))

    def check(self, manifest, donor=None):
        return self.run("check", "--manifest", str(manifest), "--donor", str(donor or self.root))

    def compare(self, manifest, destination):
        return self.run("compare", "--manifest", str(manifest), "--destination", str(destination))

    def base(self, row, **kwargs):
        spec = {
            "row": row,
            "donor_repository": "gateway",
            "donor_sha": self.sha,
            "moves": [],
            "new_files": [],
            "shared": [],
            "specimens": [],
            "assertions": [],
            "mapping_rows": [],
            "expectations": [],
            "expected_differences": [],
        }
        spec.update(kwargs)
        return spec

    def move(self, path, dest, repository="eebusreg"):
        return {"path": path, "destination_repository": repository, "destination_path": dest}

    def pattern(self, row, case, assertion, test):
        return {
            "row": row,
            "kind": "specification",
            "cases": [case],
            "assertion": assertion,
            "old_symbol": "",
            "bom_entry": "",
            "signature": self.signature,
            "exemplar": {
                "repository": "eebusreg",
                "path": EXEMPLAR_PATH,
                "revision": self.sha,
            },
            "candidate": {"form": "inline", "operands": fill(self.signature, MEAS)},
        }

    def expectation(self, case, row, assertion, test, vector):
        return {
            "case": case,
            "kind": "specification",
            "rows": [{
                "row": row,
                "assertion": assertion,
                "destination": {"repository": "ebusgo", "path": "dest/echo_test.go", "test": test},
            }],
            "selectors": selector_map(self.signature, MEAS),
            "inputs": dict(INPUTS),
            "references": [{
                "role": "specification",
                "repository": "docs-semantic",
                "revision": self.sha,
                "path": vector,
            }],
        }

    def difference(self, path):
        body = json.loads((self.root / path).read_text(encoding="utf-8"))
        body.update({"repository": "docs-semantic", "revision": self.sha, "path": path})
        return body


class ClosureTest(BaselineCase):
    fixture = None

    @classmethod
    def setUpClass(cls):
        source = tool_root()
        root = REPO / ".tmp" / "preflight-fixture" / "donor"
        if root.exists():
            shutil.move(str(root.parent), str(root.parent.parent / ("preflight-fixture-old-" + str(len(list(root.parent.parent.iterdir()))))))
        root.mkdir(parents=True)
        cls.fixture = ToolFixture(root, source)
        peer = cls.fixture.base(
            "peer-batch",
            moves=[
                cls.fixture.move("closure/closed/moved.go", "driver/rows/inbound/peer_one.go"),
                cls.fixture.move("closure/closed/shared.go", "driver/rows/inbound/peer_two.go"),
            ],
            mapping_rows=[cls.fixture.pattern("peer-row", "peer-case", "peer-assert", "TestEcho")],
            expectations=[cls.fixture.expectation("peer-case", "peer-row", "peer-assert", "TestEcho", "vec/meas.json")],
        )
        generated = cls.fixture.generate(peer)
        if generated.returncode != 0:
            raise AssertionError(detail(generated))
        equal = cls.fixture.base(
            "equal-batch",
            mapping_rows=[cls.fixture.pattern("equal-row", "equal-case", "equal-assert", "TestEcho")],
            expectations=[cls.fixture.expectation("equal-case", "equal-row", "equal-assert", "TestEcho", "vec/meas.json")],
        )
        generated = cls.fixture.generate(equal)
        if generated.returncode != 0:
            raise AssertionError(detail(generated))
        north = cls.fixture.base(
            "north-batch",
            mapping_rows=[cls.fixture.pattern("north-row", "north", "north-assert", "TestEcho")],
            expectations=[cls.fixture.expectation("north", "north-row", "north-assert", "TestEcho", "vec/north.json")],
            expected_differences=[cls.fixture.difference("vec/diff-north.json")],
        )
        generated = cls.fixture.generate(north)
        if generated.returncode != 0:
            raise AssertionError(detail(generated))
        for row, dest in (
            ("small-batch", "driver/rows/inbound/small_func.go"),
            ("wide-func-batch", "driver/rows/inbound/wide_func.go"),
            ("wide-file-batch", "driver/rows/inbound/wide_file.go"),
        ):
            generated = cls.fixture.generate(cls.fixture.base(
                row,
                moves=[cls.fixture.move("closure/closed/moved.go", dest)],
            ))
            if generated.returncode != 0:
                raise AssertionError(detail(generated))

    def peer_files(self):
        write_inbound("peer_one.go", package_file("PeerOne", "func PeerOne() int { return 1 }"))
        write_inbound("peer_two.go", package_file("PeerTwo", "func PeerTwo() int { return 1 }"))

    def test_peer_files_under_the_soft_limits_give_no_finding(self):
        self.peer_files()
        manifest = self.fixture.manifest_path("peer-batch")
        before = manifest.read_bytes()
        completed = run_preflight(manifest, self.fixture.root)
        self.assertEqual(completed.returncode, 0, detail(completed))
        self.assertNotIn("peer_one.go", completed.stdout)
        self.assertNotIn("peer_two.go", completed.stdout)
        self.assertNotIn("split required:", completed.stdout)
        self.assertEqual(manifest.read_bytes(), before)

    def test_small_destination_gives_no_size_line(self):
        write_inbound("small_func.go", package_file("Small", "func Small() int { return 1 }"))
        completed = run_preflight(self.fixture.manifest_path("small-batch"), self.fixture.root)
        self.assertEqual(completed.returncode, 0, detail(completed))
        self.assertNotIn("small_func.go", completed.stdout)

    def test_function_over_the_hard_limit_is_soft_and_split_required(self):
        source = (
            "// Package inbound is a synthetic destination package.\n"
            "package inbound\n"
            "\n"
            "// Wide is a synthetic destination function.\n"
            "func Wide() {\n"
            + "\n" * (151 - 2)
            + "}\n"
        )
        write_inbound("wide_func.go", source)
        completed = run_preflight(self.fixture.manifest_path("wide-func-batch"), self.fixture.root)
        self.assertNotEqual(completed.returncode, 0, detail(completed))
        self.assertTrue(any(
            line.startswith("SOFT ") and "wide_func.go" in line and "function_lines" in line and '"value": 151' in line
            for line in completed.stdout.splitlines()
        ), detail(completed))
        self.assertIn(
            "split required: driver/rows/inbound/wide_func.go Wide function_lines 151 > 150",
            completed.stdout,
        )

    def test_file_over_the_hard_limit_names_the_file(self):
        source = (
            "// Package inbound is a synthetic destination package.\n"
            "package inbound\n"
            + "// padding\n" * 999
        )
        write_inbound("wide_file.go", source)
        completed = run_preflight(self.fixture.manifest_path("wide-file-batch"), self.fixture.root)
        self.assertNotEqual(completed.returncode, 0, detail(completed))
        self.assertTrue(any(
            line.startswith("SOFT ") and "wide_file.go" in line and "file_lines" in line and '"value": 1001' in line
            for line in completed.stdout.splitlines()
        ), detail(completed))
        self.assertIn(
            "split required: driver/rows/inbound/wide_file.go <file> file_lines 1001 > 1000",
            completed.stdout,
        )

    def test_incomplete_closure_is_the_checker_finding(self):
        self.peer_files()
        tree = self._clone_tool()
        listed = tree / "acceptance" / "manifests" / "peer-batch.eebusreg.lst"
        shutil.move(str(listed), str(tree / "removed-list.lst"))
        completed = run_preflight(tree / "acceptance" / "manifests" / "peer-batch.json", self.fixture.root)
        self.assertNotEqual(completed.returncode, 0, detail(completed))
        self.assertIn("sealed-list:", completed.stdout)

    def test_stale_signature_stays_selected_and_fails_validation(self):
        self.peer_files()
        tree = self._clone_tool()
        path = tree / "acceptance" / "manifests" / "peer-batch.json"
        body = json.loads(path.read_text(encoding="utf-8"))
        body["patterns"][0]["signature"] = {
            "family": "spine",
            "inverse": "other-pattern",
            "selector_fields": ["phase"],
        }
        path.write_text(json.dumps(body), encoding="utf-8")
        sealed = routing.destination_sealed(body, routing.THIS_REPOSITORY)
        self.assertIn("driver/rows/inbound/peer_one.go", sealed)
        selected, _manifest = routing.select_manifest(
            [(path, body)],
            ["driver/rows/inbound/peer_one.go", "driver/rows/inbound/peer_two.go"],
        )
        self.assertEqual(selected, path)
        checked = self.fixture.check(path, self.fixture.root)
        self.assertNotEqual(checked.returncode, 0, detail(checked))
        self.assertIn("signature-mismatch row: peer-row", checked.stdout)
        decision = routing.decide("unstacked", True, True, False, False)
        self.assertEqual(decision["classification"], "selected manifest failed validation")
        self.assertNotEqual(decision["classification"], "no applicable manifest")
        self.assertTrue(decision["fail"])
        completed = run_preflight(path, self.fixture.root)
        self.assertNotEqual(completed.returncode, 0, detail(completed))
        self.assertIn("signature-mismatch row: peer-row", completed.stdout)

    def test_wrong_donor_fails(self):
        self.peer_files()
        moved = self.fixture.root.parent / "moved-donor"
        git(self.fixture.root.parent, "clone", str(self.fixture.root), str(moved))
        git(moved, "remote", "set-url", "origin", "https://github.com/Project-Helianthus/helianthus-gateway.git")
        git(moved, "commit", "--allow-empty", "-m", "later")
        self.assertNotEqual(git_out(moved, "rev-parse", "HEAD"), self.fixture.sha)
        completed = run_preflight(self.fixture.manifest_path("peer-batch"), moved)
        self.assertNotEqual(completed.returncode, 0, detail(completed))
        self.assertIn("donor-sha", completed.stdout)

    def test_missing_donor_fails_before_a_check(self):
        completed = run_preflight(self.fixture.manifest_path("peer-batch"), self.fixture.root.parent / "absent-donor")
        self.assertNotEqual(completed.returncode, 0, detail(completed))
        self.assertIn("missing donor", completed.stdout)
        self.assertNotIn("check: ok", completed.stdout)

    def test_missing_and_mutated_expectations(self):
        path = self.fixture.manifest_path("equal-batch")
        original = path.read_bytes()
        body = json.loads(original)
        body["expectations"][0]["references"][0]["path"] = "vec/missing.json"
        missing_path = self.fixture.root / "acceptance" / "manifests" / "missing-batch.json"
        missing_path.write_text(json.dumps(body), encoding="utf-8")
        missing = self.fixture.check(missing_path)
        self.assertNotEqual(missing.returncode, 0, detail(missing))
        self.assertIn("unavailable:", missing.stdout)
        self.assertNotIn("check: ok", missing.stdout)
        mutated_body = json.loads(original)
        mutated_body["expectations"][0]["references"][0]["blob_sha256"] = hashlib.sha256(b"mutated").hexdigest()
        mutated_path = self.fixture.root / "acceptance" / "manifests" / "mutated-batch.json"
        mutated_path.write_text(json.dumps(mutated_body), encoding="utf-8")
        mutated = self.fixture.check(mutated_path)
        self.assertNotEqual(mutated.returncode, 0, detail(mutated))
        self.assertIn("mutated:", mutated.stdout)
        self.assertEqual(path.read_bytes(), original)

    def test_compare_equality_approved_delta_and_extra_field(self):
        equal = self.fixture.compare(self.fixture.manifest_path("equal-batch"), self.fixture.dest)
        self.assertEqual(equal.returncode, 0, detail(equal))
        self.assertIn("compare: ok", equal.stdout)
        self.assertIn("result=pass", equal.stdout)
        approved = self.fixture.root.parent / "approved-dest"
        shutil.copytree(self.fixture.dest, approved)
        (approved / "dest" / "approved-new").write_text("northbound=present\n", encoding="utf-8")
        delta = self.fixture.compare(self.fixture.manifest_path("north-batch"), approved)
        self.assertEqual(delta.returncode, 0, detail(delta))
        self.assertIn("compare: ok", delta.stdout)
        extra = self.fixture.root.parent / "extra-dest"
        shutil.copytree(approved, extra)
        (extra / "dest" / "extra-field").write_text("extra a b\n", encoding="utf-8")
        mismatch = self.fixture.compare(self.fixture.manifest_path("north-batch"), extra)
        self.assertNotEqual(mismatch.returncode, 0, detail(mismatch))
        self.assertIn("unapproved-difference: extra", mismatch.stdout)
        self.assertNotIn("compare: ok", mismatch.stdout)

    def test_manifest_bytes_stay_after_check_and_compare(self):
        path = self.fixture.manifest_path("equal-batch")
        before = path.read_bytes()
        checked = self.fixture.check(path)
        self.assertEqual(checked.returncode, 0, detail(checked))
        self.assertIn("check: ok", checked.stdout)
        compared = self.fixture.compare(path, self.fixture.dest)
        self.assertEqual(compared.returncode, 0, detail(compared))
        self.assertEqual(path.read_bytes(), before)

    def _clone_tool(self):
        tree = self.fixture.root.parent / ("clone-" + str(len(list(self.fixture.root.parent.iterdir()))))
        git(self.fixture.root.parent, "clone", str(self.fixture.root), str(tree))
        manifests = tree / "acceptance" / "manifests"
        manifests.mkdir(parents=True, exist_ok=True)
        for path in (self.fixture.root / "acceptance" / "manifests").glob("peer-batch.*"):
            shutil.copy(path, manifests / path.name)
        return tree


class ShallowTest(BaselineCase):
    def test_exemplar_in_another_repository_needs_the_fetch(self):
        source = tool_root()
        work = REPO / ".tmp" / "shallow-exemplar"
        if work.exists():
            shutil.move(str(work), str(work.parent / ("shallow-exemplar-old-" + str(len(list(work.parent.iterdir()))))))
        donor = work / "donor"
        donor.mkdir(parents=True)
        closed = source / "acceptance" / "tools" / "manifest" / "testdata" / "donor" / "closed" / "moved.go"
        target = donor / "closure" / "closed" / "moved.go"
        target.parent.mkdir(parents=True)
        shutil.copy(closed, target)
        tool = donor / "acceptance" / "tools" / "manifest"
        tool.mkdir(parents=True)
        for name in ("main.py", "manifestlib.py", "closure.py", "gitstore.py", "patterns.py"):
            shutil.copy(source / "acceptance" / "tools" / "manifest" / name, tool / name)
        (donor / "acceptance" / "manifests").mkdir(parents=True)
        signature = load_signature(source)
        vector = donor / "vec" / "meas.json"
        vector.parent.mkdir(parents=True)
        vector.write_text(
            json.dumps({
                "fields": {"reading": "230"},
                "inputs": {},
                "selectors": selector_map(signature, MEAS),
            }, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        git(donor, "init", "-b", "main")
        git(donor, "add", "-A")
        git(donor, "commit", "-m", "donor")
        donor_sha = git_out(donor, "rev-parse", "HEAD")
        shallow = work / "shallow"
        git(work, "clone", "--depth", "1", "--no-local", str(donor), str(shallow))
        exemplar = work / "exemplar"
        exemplar.mkdir()
        git(exemplar, "init", "-b", "main")
        planted = exemplar / EXEMPLAR_PATH
        planted.parent.mkdir(parents=True)
        shutil.copy(
            source / "acceptance" / "tools" / "manifest" / "testdata" / "exemplar" / "measurement_rows.go",
            planted,
        )
        git(exemplar, "add", "-A")
        git(exemplar, "commit", "-m", "exemplar")
        exemplar_sha = git_out(exemplar, "rev-parse", "HEAD")
        spec = {
            "row": "shallow-exemplar",
            "donor_repository": "gateway",
            "donor_sha": donor_sha,
            "moves": [],
            "new_files": [],
            "shared": [],
            "specimens": [],
            "assertions": [],
            "mapping_rows": [{
                "row": "exemplar-row",
                "kind": "specification",
                "cases": ["exemplar-case"],
                "assertion": "exemplar-assert",
                "old_symbol": "",
                "bom_entry": "",
                "signature": signature,
                "exemplar": {
                    "repository": "eebusreg",
                    "path": EXEMPLAR_PATH,
                    "revision": exemplar_sha,
                },
                "candidate": {"form": "inline", "operands": fill(signature, MEAS)},
            }],
            "expectations": [{
                "case": "exemplar-case",
                "kind": "specification",
                "rows": [{
                    "row": "exemplar-row",
                    "assertion": "exemplar-assert",
                    "destination": {
                        "repository": "ebusgo",
                        "path": "dest/echo_test.go",
                        "test": "TestEcho",
                    },
                }],
                "selectors": selector_map(signature, MEAS),
                "inputs": dict(INPUTS),
                "references": [{
                    "role": "specification",
                    "repository": "docs-semantic",
                    "revision": donor_sha,
                    "path": "vec/meas.json",
                }],
            }],
            "expected_differences": [],
        }
        spec_path = donor / "specs" / "shallow-exemplar.json"
        spec_path.parent.mkdir(parents=True, exist_ok=True)
        spec_path.write_text(json.dumps(spec), encoding="utf-8")
        routing.fetch_revision(donor, "eebusreg", exemplar_sha, url=str(exemplar))
        generated = subprocess.run(
            [sys.executable, str(tool / "main.py"), "generate", "--input", str(spec_path), "--donor", str(donor)],
            text=True, capture_output=True, check=False,
        )
        self.assertEqual(generated.returncode, 0, detail(generated))
        manifest = donor / "acceptance" / "manifests" / "shallow-exemplar.json"
        present = subprocess.run(
            [sys.executable, str(tool / "main.py"), "check", "--manifest", str(manifest), "--donor", str(donor)],
            text=True, capture_output=True, check=False,
        )
        self.assertEqual(present.returncode, 0, detail(present))
        self.assertEqual(git_out(donor, "rev-parse", "HEAD"), donor_sha)
        for path in (donor / "acceptance" / "manifests").glob("shallow-exemplar.*"):
            destination = shallow / "acceptance" / "manifests" / path.name
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy(path, destination)
        shallow_main = shallow / "acceptance" / "tools" / "manifest" / "main.py"
        absent = subprocess.run(
            [sys.executable, str(shallow_main), "check", "--manifest", str(shallow / "acceptance" / "manifests" / "shallow-exemplar.json"), "--donor", str(shallow)],
            text=True, capture_output=True, check=False,
        )
        self.assertNotEqual(absent.returncode, 0, detail(absent))
        self.assertIn("unavailable:", absent.stdout)
        self.assertNotIn("check: ok", absent.stdout)
        routing.fetch_revision(shallow, "eebusreg", exemplar_sha, url=str(exemplar))
        fetched = subprocess.run(
            [sys.executable, str(shallow_main), "check", "--manifest", str(shallow / "acceptance" / "manifests" / "shallow-exemplar.json"), "--donor", str(shallow)],
            text=True, capture_output=True, check=False,
        )
        self.assertEqual(fetched.returncode, 0, detail(fetched))
        self.assertIn("check: ok", fetched.stdout)
        self.assertEqual(git_out(shallow, "rev-parse", "HEAD"), donor_sha)

    def test_reference_newer_than_the_tool_head_needs_the_fetch(self):
        source = tool_root()
        work = REPO / ".tmp" / "shallow-reference"
        if work.exists():
            shutil.move(str(work), str(work.parent / ("shallow-reference-old-" + str(len(list(work.parent.iterdir()))))))
        full = work / "full"
        full.mkdir(parents=True)
        tool = full / "acceptance" / "tools" / "manifest"
        tool.mkdir(parents=True)
        for name in ("main.py", "manifestlib.py", "closure.py", "gitstore.py", "patterns.py"):
            shutil.copy(source / "acceptance" / "tools" / "manifest" / name, tool / name)
        (full / "acceptance" / "manifests").mkdir(parents=True)
        vector = full / "vec" / "ref.json"
        vector.parent.mkdir(parents=True)
        vector.write_text('{"fields": {"reading": "1"}, "inputs": {}, "selectors": {}}\n', encoding="utf-8")
        git(full, "init", "-b", "main")
        git(full, "add", "-A")
        git(full, "commit", "-m", "reference")
        old = git_out(full, "rev-parse", "HEAD")
        vector.unlink()
        git(full, "add", "-A")
        git(full, "commit", "-m", "newer")
        new = git_out(full, "rev-parse", "HEAD")
        git(full, "config", "uploadpack.allowReachableSHA1InWant", "true")
        git(full, "config", "uploadpack.allowAnySHA1InWant", "true")
        self.assertNotEqual(old, new)
        spec = {
            "row": "shallow-ref",
            "donor_repository": "docs-semantic",
            "donor_sha": new,
            "moves": [],
            "new_files": [],
            "shared": [],
            "specimens": [],
            "assertions": [],
            "mapping_rows": [],
            "expectations": [{
                "case": "ref-case",
                "kind": "specification",
                "rows": [],
                "selectors": {},
                "inputs": dict(INPUTS),
                "references": [{
                    "role": "specification",
                    "repository": "docs-semantic",
                    "revision": old,
                    "path": "vec/ref.json",
                }],
            }],
            "expected_differences": [],
        }
        spec_path = full / "specs" / "shallow-ref.json"
        spec_path.parent.mkdir(parents=True, exist_ok=True)
        spec_path.write_text(json.dumps(spec), encoding="utf-8")
        generated = subprocess.run(
            [sys.executable, str(tool / "main.py"), "generate", "--input", str(spec_path), "--donor", str(full)],
            text=True, capture_output=True, check=False,
        )
        self.assertEqual(generated.returncode, 0, detail(generated))
        shallow = work / "shallow"
        git(work, "clone", "--depth", "1", "--no-local", str(full), str(shallow))
        self.assertEqual(git_out(shallow, "rev-parse", "HEAD"), new)
        absent_object = git(shallow, "cat-file", "-e", old + "^{commit}", check=False)
        self.assertNotEqual(absent_object.returncode, 0)
        for path in (full / "acceptance" / "manifests").glob("shallow-ref.*"):
            destination = shallow / "acceptance" / "manifests" / path.name
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy(path, destination)
        dest = work / "dest"
        shutil.copytree(source / "acceptance" / "tools" / "manifest" / "testdata" / "destination", dest)
        shallow_main = shallow / "acceptance" / "tools" / "manifest" / "main.py"
        manifest = shallow / "acceptance" / "manifests" / "shallow-ref.json"
        for command, extra in (
            ("check", ["--donor", str(shallow)]),
            ("compare", ["--destination", str(dest)]),
        ):
            completed = subprocess.run(
                [sys.executable, str(shallow_main), command, "--manifest", str(manifest), *extra],
                text=True, capture_output=True, check=False,
            )
            self.assertNotEqual(completed.returncode, 0, detail(completed))
            self.assertIn("unavailable:", completed.stdout)
            self.assertNotIn(command + ": ok", completed.stdout)
        routing.fetch_revision(shallow, "docs-semantic", old, url=str(full))
        self.assertEqual(git_out(shallow, "rev-parse", "HEAD"), new)
        for command, extra in (
            ("check", ["--donor", str(shallow)]),
            ("compare", ["--destination", str(dest)]),
        ):
            completed = subprocess.run(
                [sys.executable, str(shallow_main), command, "--manifest", str(manifest), *extra],
                text=True, capture_output=True, check=False,
            )
            self.assertEqual(completed.returncode, 0, detail(completed))
            self.assertIn(command + ": ok", completed.stdout)
        self.assertEqual(git_out(shallow, "rev-parse", "HEAD"), new)


if __name__ == "__main__":
    unittest.main()
