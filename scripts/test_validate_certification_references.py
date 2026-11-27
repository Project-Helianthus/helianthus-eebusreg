#!/usr/bin/env python3
"""Tests of validate_certification_references.py."""
import contextlib
import importlib.util
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/validate_certification_references.py"
DATA = ROOT / "driverdata/certification/eebus-inbound.json"
VECTORS = ROOT / "driverdata/certification/testdata/vectors.json"
SCHEMA = ROOT / "driverdata/certification/references.schema.json"

spec = importlib.util.spec_from_file_location("validate_certification_references", SCRIPT)
validator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(validator)


def run(args):
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        status = validator.main(args)
    return status, out.getvalue(), err.getvalue()


def write(directory, name, text):
    path = Path(directory) / name
    path.write_text(text, encoding="utf-8")
    return str(path)


class VectorTests(unittest.TestCase):
    def test_every_vector_outcome_matches(self):
        schema = validator.load_json(SCHEMA)
        vectors = validator.load_json(VECTORS)["vectors"]
        self.assertGreater(len(vectors), 30)
        for vector in vectors:
            with self.subTest(vector["id"]):
                actual = validator.outcome(validator.check_document(vector["document"], schema))
                self.assertEqual(vector["expected"], actual)

    def test_every_vector_reason_is_exercised(self):
        reasons = {v["expected"].get("reason") for v in validator.load_json(VECTORS)["vectors"]} - {None}
        self.assertEqual(
            {"schema", "source_not_certification", "unknown_member", "missing_member", "stated_value_not_allowed",
             "refusal_missing", "invalid_value", "firmware_invalid", "firmware_upper_bound_unstated",
             "stated_value_invalid", "category_conflict"},
            reasons,
        )


class DataFileTests(unittest.TestCase):
    def test_data_file_is_one_gap_and_no_reference(self):
        entries = validator.load_json(DATA)["entries"]
        self.assertEqual(1, len(entries))
        self.assertEqual("gap", entries[0]["category"])
        self.assertEqual("model_not_named_by_driver", entries[0]["model"])
        self.assertEqual([], [e for e in entries if e["category"] == "reference"])
        self.assertNotIn("stated_value", entries[0])

    def test_command_accepts_data_file(self):
        status, out, _ = run([str(DATA)])
        self.assertEqual(0, status)
        self.assertIn("PASS", out)

    def test_mutation_check_passes_on_data_file(self):
        status, out, err = run(["--mutation-check", str(DATA)])
        self.assertEqual((0, ""), (status, err))
        self.assertIn("PASS", out)


class CommandTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.document = validator.load_json(DATA)

    def path(self, document):
        return write(self.directory.name, "doc.json", json.dumps(document))

    def test_rejects_device_source(self):
        self.document["entries"][0]["source"] = "device"
        status, _, err = run([self.path(self.document)])
        self.assertEqual(1, status)
        self.assertIn("source_not_certification", err)

    def test_rejects_gap_with_stated_value(self):
        self.document["entries"][0]["stated_value"] = {"measurement_type": "active_power", "measured": True}
        status, _, err = run([self.path(self.document)])
        self.assertEqual(1, status)
        self.assertIn("stated_value_not_allowed", err)

    def test_rejects_repeated_key(self):
        text = '{"contract": "a", "contract": "b", "entries": []}'
        status, _, err = run([write(self.directory.name, "dup.json", text)])
        self.assertEqual(1, status)
        self.assertIn("repeated object key", err)

    def test_rejects_missing_file(self):
        status, _, _ = run([str(Path(self.directory.name) / "absent.json")])
        self.assertEqual(1, status)

    def test_usage_errors(self):
        self.assertEqual(2, run([])[0])
        self.assertEqual(2, run(["--mutation-check"])[0])
        self.assertEqual(2, run(["a", "b"])[0])

    def test_mutation_check_refuses_invalid_document(self):
        self.document["entries"][0]["source"] = "device"
        status, _, err = run(["--mutation-check", self.path(self.document)])
        self.assertEqual(1, status)
        self.assertIn("the document itself is rejected", err)

    def test_mutation_check_covers_every_category(self):
        vector = validator.load_json(VECTORS)["vectors"][0]["document"]
        status, out, err = run(["--mutation-check", self.path(vector)])
        self.assertEqual((0, ""), (status, err))
        names = [name for name, _, _ in validator.mutations(vector)]
        for index in range(3):
            self.assertIn(f"entry {index}: other category for one identity", names)
        self.assertIn("entry 0: null stated member", names)
        self.assertIn("entry 2: empty refusal", names)
        self.assertIn("entry 1: refusal on gap", names)


class MutationCheckCatchesWeakValidators(unittest.TestCase):
    """Each case removes one rule from the validator; the mutation check must then fail."""

    def failures_without(self, check_name):
        saved = validator.ENTRY_CHECKS, validator.check_categories
        try:
            validator.ENTRY_CHECKS = tuple(c for c in saved[0] if c.__name__ != check_name)
            if check_name == "check_categories":
                validator.check_categories = lambda entries: None
            document = validator.load_json(VECTORS)["vectors"][0]["document"]
            return validator.mutation_check(document, validator.load_json(SCHEMA))
        finally:
            validator.ENTRY_CHECKS, validator.check_categories = saved

    def test_ignoring_source_is_caught(self):
        self.assertTrue(self.failures_without("check_source"))

    def test_ignoring_member_rules_is_caught(self):
        self.assertTrue(self.failures_without("check_members"))

    def test_ignoring_values_is_caught(self):
        self.assertTrue(self.failures_without("check_values"))

    def test_ignoring_firmware_is_caught(self):
        self.assertTrue(self.failures_without("check_firmware"))

    def test_ignoring_stated_value_is_caught(self):
        self.assertTrue(self.failures_without("check_stated_value"))

    def test_ignoring_category_conflict_is_caught(self):
        self.assertTrue(self.failures_without("check_categories"))

    def test_intact_validator_has_no_failures(self):
        document = validator.load_json(VECTORS)["vectors"][0]["document"]
        self.assertEqual([], validator.mutation_check(document, validator.load_json(SCHEMA)))


if __name__ == "__main__":
    sys.exit(unittest.main())
