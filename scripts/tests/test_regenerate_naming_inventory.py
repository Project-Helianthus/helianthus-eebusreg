import copy
import hashlib
import importlib.util
import json
import subprocess
import tempfile
import unittest
from pathlib import Path

MODULE_PATH = Path(__file__).resolve().parents[1] / "regenerate_naming_inventory.py"
spec = importlib.util.spec_from_file_location("regenerate_naming_inventory", MODULE_PATH)
generator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(generator)

RETAINED_PAGE = "docs/platform/modbus-foundation-profile-contract-v1.md"


def encode(inventory):
    return (json.dumps(inventory, indent=2, ensure_ascii=False) + "\n").encode("utf-8")


def synthetic_map(base_bytes):
    return {
        "schema": 1,
        "base": {"path": "naming-inventory.json", "sha256": hashlib.sha256(base_bytes).hexdigest()},
        "repositories": [
            {
                "old_repository": "helianthus-alpha",
                "new_repository": "helianthus-beta",
                "old_locator": "alpha",
                "new_locator": "beta",
            },
            {
                "old_repository": "helianthus-gamma-old",
                "new_repository": "helianthus-gamma-new",
                "old_locator": "gamma-old",
                "new_locator": "gamma-new",
            },
        ],
        "split": {
            "old_repository": "helianthus-src-docs",
            "old_locator": "src-docs",
            "destinations": [
                {
                    "new_repository": "helianthus-dest-one",
                    "new_locator": "dest-one",
                    "paths": ["pages/one.md", "pages/shared.md"],
                },
                {
                    "new_repository": "helianthus-dest-two",
                    "new_locator": "dest-two",
                    "paths": ["pages/two.md"],
                },
            ],
            "retained": [{"owner_locator": "src-docs", "path": "pages/shared.md"}],
        },
    }


CONSUMERS = [
    "alpha/cmd/main.go",
    "alphabet/cmd/main.go",
    "gamma-old/internal/x.go",
    "helianthus-alpha/cmd/main.go",
    "nested/alpha/cmd/main.go",
    "src-docs/pages/one.md",
    "src-docs/pages/one.md.bak",
    "src-docs/pages/two.md",
    "src-docs/pages/shared.md",
    "src-docs/pages/unlisted.md",
    "dest-one/pages/two.md",
    "other-docs/pages/one.md",
    "unrelated/file.go",
]

EXPECTED = [
    "beta/cmd/main.go",
    "alphabet/cmd/main.go",
    "gamma-new/internal/x.go",
    "helianthus-alpha/cmd/main.go",
    "nested/alpha/cmd/main.go",
    "dest-one/pages/one.md",
    "src-docs/pages/one.md.bak",
    "dest-two/pages/two.md",
    "src-docs/pages/shared.md",
    "src-docs/pages/unlisted.md",
    "dest-one/pages/two.md",
    "other-docs/pages/one.md",
    "unrelated/file.go",
]


def synthetic_inventory():
    return {
        "schema": 1,
        "scope": "synthetic alpha/scope mention stays",
        "source_files": ["alpha/source.go"],
        "rows": [
            {
                "owning_file": "alpha/owner.go",
                "identity": "alpha:Type.Field",
                "current_name": "alpha/current",
                "target": "src-docs/pages/one.md",
                "public_evidence": ["alpha/evidence.go"],
                "open_question": "see src-docs/pages/two.md",
                "status": "unchanged",
                "consumer_files": list(CONSUMERS),
            },
            {"identity": "second", "status": "unchanged", "consumer_files": []},
        ],
        "boundaries": {"alpha": "alpha/boundary"},
    }


class RegenerateNamingInventoryTests(unittest.TestCase):
    def setUp(self):
        self.base = encode(synthetic_inventory())
        self.map = synthetic_map(self.base)

    def regenerated(self, locator_map=None, base=None):
        return json.loads(generator.regenerate(base or self.base, locator_map or self.map))

    def test_exact_substitutions(self):
        rows = self.regenerated()["rows"]
        self.assertEqual(rows[0]["consumer_files"], EXPECTED)
        self.assertEqual(rows[1]["consumer_files"], [])

    def test_only_consumer_files_change(self):
        before = synthetic_inventory()
        after = self.regenerated()
        for key in ("schema", "scope", "source_files", "boundaries"):
            self.assertEqual(after[key], before[key])
        for old_row, new_row in zip(before["rows"], after["rows"]):
            for key in old_row:
                if key != "consumer_files":
                    self.assertEqual(new_row[key], old_row[key], key)
            self.assertEqual(len(new_row["consumer_files"]), len(old_row["consumer_files"]))

    def test_unmapped_map_reproduces_base_bytes(self):
        empty = copy.deepcopy(self.map)
        empty["repositories"] = []
        empty["split"]["destinations"] = []
        self.assertEqual(generator.regenerate(self.base, empty), self.base)

    def test_repeat_is_byte_identical(self):
        self.assertEqual(
            generator.regenerate(self.base, self.map),
            generator.regenerate(self.base, copy.deepcopy(self.map)),
        )

    def test_repository_old_locator_is_the_prefix_not_the_repository_name(self):
        changed = copy.deepcopy(self.map)
        changed["repositories"][0]["old_locator"] = "helianthus-alpha"
        consumers = self.regenerated(changed)["rows"][0]["consumer_files"]
        self.assertEqual(consumers[0], "alpha/cmd/main.go")
        self.assertEqual(consumers[3], "beta/cmd/main.go")

    def test_repository_new_locator_is_the_replacement_not_the_repository_name(self):
        changed = copy.deepcopy(self.map)
        changed["repositories"][1]["new_locator"] = "gamma-other"
        self.assertEqual(self.regenerated(changed)["rows"][0]["consumer_files"][2], "gamma-other/internal/x.go")

    def test_split_old_locator_selects_the_owner_prefix(self):
        changed = copy.deepcopy(self.map)
        changed["split"]["old_locator"] = "other-docs"
        consumers = self.regenerated(changed)["rows"][0]["consumer_files"]
        self.assertEqual(consumers[5], "src-docs/pages/one.md")
        self.assertEqual(consumers[11], "dest-one/pages/one.md")

    def test_destination_new_locator_is_the_replacement_not_the_repository_name(self):
        changed = copy.deepcopy(self.map)
        changed["split"]["destinations"][1]["new_locator"] = "dest-moved"
        self.assertEqual(self.regenerated(changed)["rows"][0]["consumer_files"][7], "dest-moved/pages/two.md")

    def test_destination_paths_decide_which_pages_move(self):
        changed = copy.deepcopy(self.map)
        changed["split"]["destinations"][0]["paths"].append("pages/unlisted.md")
        consumers = self.regenerated(changed)["rows"][0]["consumer_files"]
        self.assertEqual(consumers[9], "dest-one/pages/unlisted.md")
        changed["split"]["destinations"][1]["paths"] = []
        self.assertEqual(self.regenerated(changed)["rows"][0]["consumer_files"][7], "src-docs/pages/two.md")

    def test_retained_path_stays_at_its_owner_even_when_listed_for_a_move(self):
        consumers = self.regenerated()["rows"][0]["consumer_files"]
        self.assertEqual(consumers[8], "src-docs/pages/shared.md")
        changed = copy.deepcopy(self.map)
        changed["split"]["retained"][0]["path"] = "pages/one.md"
        consumers = self.regenerated(changed)["rows"][0]["consumer_files"]
        self.assertEqual(consumers[5], "src-docs/pages/one.md")
        self.assertEqual(consumers[8], "dest-one/pages/shared.md")

    def test_retained_owner_locator_is_part_of_the_retained_locator(self):
        changed = copy.deepcopy(self.map)
        changed["split"]["retained"][0]["owner_locator"] = "other-docs"
        self.assertEqual(self.regenerated(changed)["rows"][0]["consumer_files"][8], "dest-one/pages/shared.md")

    def test_page_listed_twice_is_rejected(self):
        changed = copy.deepcopy(self.map)
        changed["split"]["destinations"][1]["paths"].append("pages/one.md")
        with self.assertRaises(SystemExit):
            generator.regenerate(self.base, changed)

    def test_base_digest_mismatch_is_rejected(self):
        changed = copy.deepcopy(self.map)
        changed["base"]["sha256"] = hashlib.sha256(self.base + b" ").hexdigest()
        with self.assertRaises(SystemExit):
            generator.regenerate(self.base, changed)
        with self.assertRaises(SystemExit):
            generator.regenerate(self.base + b" ", self.map)


class PublishedMapTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.map = generator.load_map(generator.DEFAULT_MAP)
        cls.prefixes, cls.moved = generator.build_translation(cls.map)

    def translate(self, value):
        return generator.translate(value, self.prefixes, self.moved)

    def test_repository_locators(self):
        entries = {entry["old_locator"]: entry for entry in self.map["repositories"]}
        self.assertEqual(
            {old: (entry["old_repository"], entry["new_repository"], entry["new_locator"]) for old, entry in entries.items()},
            {
                "ebusgateway": ("helianthus-ebusgateway", "helianthus-gateway", "gateway"),
                "matter-binding": ("helianthus-matter-binding", "helianthus-matterreg", "matterreg"),
            },
        )
        self.assertEqual(self.translate("ebusgateway/cmd/x.go"), "gateway/cmd/x.go")
        self.assertEqual(self.translate("matter-binding/cmd/x.go"), "matterreg/cmd/x.go")

    def test_every_destination_translates_its_listed_pages(self):
        split = self.map["split"]
        self.assertEqual(
            {entry["new_locator"] for entry in split["destinations"]},
            {"docs", "docs-modbus", "docs-eebus"},
        )
        for destination in split["destinations"]:
            self.assertEqual(destination["paths"], sorted(set(destination["paths"])))
            self.assertTrue(destination["paths"])
            for page in destination["paths"]:
                self.assertEqual(
                    self.translate("docs-ebus/" + page), destination["new_locator"] + "/" + page
                )

    def test_retained_page_keeps_its_owner(self):
        retained = self.map["split"]["retained"]
        self.assertEqual(retained, [{"owner_locator": "docs-ebus", "path": RETAINED_PAGE}])
        for destination in self.map["split"]["destinations"]:
            self.assertNotIn(RETAINED_PAGE, destination["paths"])
        locator = "docs-ebus/" + RETAINED_PAGE
        self.assertEqual(self.translate(locator), locator)

    def test_unmapped_docs_ebus_pages_stay(self):
        self.assertEqual(self.translate("docs-ebus/not-a-listed-page.md"), "docs-ebus/not-a-listed-page.md")


class GitBaseTests(unittest.TestCase):
    def git(self, root, *args):
        return subprocess.run(
            ["git", "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-C", str(root), *args],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        ).stdout

    def test_main_writes_only_the_output_and_the_base_stays_in_git(self):
        with tempfile.TemporaryDirectory() as workspace:
            root = Path(workspace)
            base = encode(synthetic_inventory())
            (root / "naming-inventory.json").write_bytes(base)
            map_path = root / "naming-repository-map.json"
            map_path.write_text(json.dumps(synthetic_map(base), indent=2) + "\n", encoding="utf-8")
            self.git(root, "init", "-q")
            self.git(root, "add", "naming-inventory.json")
            self.git(root, "commit", "-q", "-m", "base")
            before = sorted(path.name for path in root.iterdir() if path.name != ".git")
            output = root / "naming-inventory.json"
            self.assertEqual(generator.read_base("HEAD", "naming-inventory.json", root), base)
            locator_map = generator.load_map(map_path)
            output.write_bytes(generator.regenerate(generator.read_base("HEAD", "naming-inventory.json", root), locator_map))
            self.assertEqual(sorted(path.name for path in root.iterdir() if path.name != ".git"), before)
            self.assertEqual(json.loads(output.read_bytes())["rows"][0]["consumer_files"], EXPECTED)
            self.assertEqual(self.git(root, "show", "HEAD:naming-inventory.json"), base)

    def test_command_line_writes_only_the_inventory_and_repeats_byte_identically(self):
        with tempfile.TemporaryDirectory() as workspace:
            root = Path(workspace)
            base = encode(synthetic_inventory())
            (root / "scripts").mkdir()
            (root / "scripts" / MODULE_PATH.name).write_bytes(MODULE_PATH.read_bytes())
            (root / "naming-inventory.json").write_bytes(base)
            (root / "naming-repository-map.json").write_text(json.dumps(synthetic_map(base), indent=2) + "\n", encoding="utf-8")
            self.git(root, "init", "-q")
            self.git(root, "add", ".")
            self.git(root, "commit", "-q", "-m", "base")
            before = sorted(str(path.relative_to(root)) for path in root.rglob("*") if ".git" not in path.relative_to(root).parts)
            command = ["python3", "-I", str(root / "scripts" / MODULE_PATH.name), "--base-rev", "HEAD"]
            subprocess.run(command, check=True, cwd=root)
            first = (root / "naming-inventory.json").read_bytes()
            subprocess.run(command, check=True, cwd=root)
            self.assertEqual((root / "naming-inventory.json").read_bytes(), first)
            after = sorted(str(path.relative_to(root)) for path in root.rglob("*") if ".git" not in path.relative_to(root).parts)
            self.assertEqual(after, before)
            self.assertEqual(json.loads(first)["rows"][0]["consumer_files"], EXPECTED)
            self.assertEqual(self.git(root, "show", "HEAD:naming-inventory.json"), base)


if __name__ == "__main__":
    unittest.main()
