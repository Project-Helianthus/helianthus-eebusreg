import importlib.util
import json
from pathlib import Path
import tempfile
import contextlib
import io
from unittest.mock import patch
import unittest

MODULE_PATH = Path(__file__).resolve().parents[1] / "check_size_ratchet.py"
spec = importlib.util.spec_from_file_location("size_ratchet", MODULE_PATH)
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)


class SizeRatchetTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        scratch = guard.ROOT / ".tmp"
        scratch.mkdir(exist_ok=True)
        cls.workspace = tempfile.TemporaryDirectory(dir=scratch)
        cls.root = Path(cls.workspace.name)
        cls.binary = guard.build_parser(guard.ROOT, cls.root)

    @classmethod
    def tearDownClass(cls):
        cls.workspace.cleanup()

    def setUp(self):
        self.fixture = tempfile.TemporaryDirectory(dir=self.root)
        self.addCleanup(self.fixture.cleanup)
        self.tree = Path(self.fixture.name)

    def write(self, path, source):
        dest = self.tree / path
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(source)

    def measure(self):
        paths, _ = guard.discover(self.tree)
        return guard.measure(self.tree, self.binary, paths)

    def baseline(self, rows):
        path = self.tree / "baseline.json"
        path.write_text(json.dumps({"version": 1, "violations": rows}))
        return guard.load_baseline(path)

    def test_every_threshold_below_equal_above(self):
        cases = 0
        for kind, budgets in guard.BUDGETS.items():
            path = "sample_test.go" if kind == "test" else "sample.go"
            for metric, thresholds in budgets.items():
                for threshold in thresholds:
                    for delta in (-1, 0, 1):
                        value = threshold + delta
                        with self.subTest(kind=kind, metric=metric, threshold=threshold, delta=delta):
                            if metric == "file_lines":
                                source = "package sample\n" + "// padding\n" * (value - 1)
                                symbol = "<file>"
                            elif metric == "function_lines":
                                source = "package sample\nfunc F() {\n" + "\n" * (value - 2) + "}\n"
                                symbol = "F"
                            else:
                                source = "package sample\n" + "".join(
                                    f"func F{i}() {{}}\n" for i in range(value))
                                symbol = "<file>"
                            self.write(path, source)
                            row = next(r for r in self.measure() if r["path"] == path
                                       and r["metric"] == metric and r["symbol"] == symbol)
                            self.assertEqual(row["value"], value)
                            soft, hard = guard.evaluate([row], [])
                            self.assertEqual(bool(soft), value > thresholds[0])
                            self.assertEqual(bool(hard), value > thresholds[1])
                            cases += 1
        self.assertEqual(cases, 36)

    def test_debt_reduction_requires_tightening_before_regrowth(self):
        cases = (("file_lines", "<file>", 1713, 1200, 1000),
                 ("function_lines", "F", 170, 160, 150),
                 ("functions_per_file", "<file>", 70, 65, 60))
        for metric, symbol, original, reduced, cleared in cases:
            with self.subTest(metric=metric):
                debt = {"path": "runtime.go", "symbol": symbol,
                        "metric": metric, "value": original}
                baseline = self.baseline([debt])
                self.assertFalse(guard.evaluate([debt], baseline)[1])
                smaller = dict(debt, value=reduced)
                with self.assertRaisesRegex(ValueError, "tighten or remove"):
                    guard.evaluate([smaller], baseline)
                tightened = self.baseline([smaller])
                self.assertFalse(guard.evaluate([smaller], tightened)[1])
                self.assertEqual(guard.evaluate([debt], tightened)[1], [debt])
                for rows in ([dict(debt, value=cleared)], []):
                    with self.assertRaisesRegex(ValueError, "tighten or remove"):
                        guard.evaluate(rows, baseline)
                    self.assertFalse(guard.evaluate(rows, [])[1])
                self.assertEqual(guard.evaluate([debt], [])[1], [debt])
                self.assertEqual(guard.evaluate([dict(debt, value=original + 1)],
                                                baseline)[1],
                                 [dict(debt, value=original + 1)])

    def test_directory_read_errors_are_fatal(self):
        self.write("visible.go", "package sample\n")
        self.write("internal/hidden.go", "package sample\n")
        real_scandir = guard.os.scandir

        def unreadable(path):
            if Path(path) == self.tree / "internal":
                raise PermissionError("unreadable internal directory")
            return real_scandir(path)

        with patch.object(guard.os, "scandir", side_effect=unreadable):
            with self.assertRaisesRegex(PermissionError, "unreadable internal"):
                guard.discover(self.tree)
            self.write("scripts/size-baseline.json", '{"version":1,"violations":[]}')
            errors = io.StringIO()
            with patch("sys.argv", ["guard", "--root", str(self.tree)]), \
                    patch.object(guard, "build_parser") as build, \
                    contextlib.redirect_stderr(errors):
                self.assertEqual(guard.main(), 2)
            build.assert_not_called()
            self.assertIn("unreadable internal", errors.getvalue())

    def test_each_identity_and_value_field_is_independent(self):
        debt = {"path": "runtime.go", "symbol": "F", "metric": "function_lines", "value": 170}
        baseline = self.baseline([debt])
        mutations = {
            "path": dict(debt, path="renamed.go"),
            "symbol": dict(debt, symbol="Renamed"),
            "metric": dict(debt, metric="functions_per_file", symbol="<file>"),
            "value": dict(debt, value=171),
        }
        for field, changed in mutations.items():
            with self.subTest(field=field):
                if field != "value":
                    with self.assertRaisesRegex(ValueError, "tighten or remove"):
                        guard.evaluate([changed], baseline)
                self.assertEqual(guard.evaluate([debt, changed], baseline)[1], [changed])
        # Vary metric alone on a valid file identity as well.
        file_debt = dict(debt, symbol="<file>", metric="file_lines", value=1001)
        self.assertTrue(guard.evaluate([file_debt, dict(file_debt, metric="functions_per_file")],
                                       self.baseline([file_debt]))[1])

    def test_malformed_baselines_fail_closed(self):
        debt = {"path": "runtime.go", "symbol": "F", "metric": "function_lines", "value": 170}
        invalid = [None, [], {}, {"version": True, "violations": []},
                   {"version": 2, "violations": []}, {"version": 1, "violations": {}},
                   {"version": 1, "violations": [], "extra": 1}]
        rows = [[debt, debt], [dict(debt, path="*.go")], [dict(debt, path="../x.go")],
                [dict(debt, path="/x.go")], [dict(debt, path="x//y.go")],
                [dict(debt, path="vendor/x.go")], [dict(debt, symbol="*")],
                [dict(debt, symbol="")], [dict(debt, symbol="<file>")],
                [dict(debt, metric="bogus")], [dict(debt, metric=[])], [dict(debt, path=[])], [dict(debt, symbol=[])], [dict(debt, value=True)],
                [dict(debt, value=170.0)], [dict(debt, value=150)],
                [dict(debt, extra=1)], [{k: v for k, v in debt.items() if k != "symbol"}]]
        invalid.extend({"version": 1, "violations": row} for row in rows)
        path = self.tree / "bad.json"
        for data in invalid:
            with self.subTest(data=data):
                path.write_text(json.dumps(data))
                with self.assertRaises(ValueError):
                    guard.load_baseline(path)
        for text in ('{"version":1,"version":1,"violations":[]}', '{'):
            path.write_text(text)
            with self.assertRaises(ValueError):
                guard.load_baseline(path)

    def test_methods_literals_and_build_constraints_all_measured(self):
        self.write("platform_windows.go", """//go:build windows

package sample
type T struct{}
func (t *T) Run() {
 f := func() { _ = func() {} }
 _ = f
}
func (t T) Value() {}
var global = func() {}
func init() {}
func init() {}
""")
        rows = self.measure()
        functions = {r["symbol"]: r["value"] for r in rows if r["metric"] == "function_lines"}
        self.assertEqual(functions, {"(*T).Run": 4, "(*T).Run/literal#1": 1,
                                    "(*T).Run/literal#1/literal#1": 1, "(T).Value": 1,
                                    "<file>/value:global#1/literal#1": 1, "init#1": 1, "init#2": 1})
        self.assertEqual(next(r["value"] for r in rows if r["metric"] == "functions_per_file"), 7)

    def test_literal_identity_ignores_line_movement(self):
        source = "package sample\nfunc F() { _ = func() {} }\n"
        self.write("f.go", source)
        before = [(r["symbol"], r["value"]) for r in self.measure() if r["metric"] == "function_lines"]
        self.write("f.go", "\n\n" + source)
        self.assertEqual(before, [(r["symbol"], r["value"]) for r in self.measure()
                                  if r["metric"] == "function_lines"])

    def test_local_value_literals_retain_enclosing_identity(self):
        self.write("f.go", """package sample
func F() {
 var f = func() {}
 _ = f
 { var f = func() {}; _ = f }
}
func G() { var f = func() {}; _ = f }
""")
        symbols = [r["symbol"] for r in self.measure() if r["metric"] == "function_lines"]
        self.assertEqual(symbols, ["F", "F/value:f#1/literal#1",
                                   "F/value:f#2/literal#1", "G", "G/value:f#1/literal#1"])

    def test_line_directives_do_not_change_physical_spans(self):
        self.write("f.go", "package sample\nfunc F() {\n//line synthetic.go:900\n}\n")
        self.assertEqual(next(r["value"] for r in self.measure() if r["symbol"] == "F"), 3)

    def test_final_newline_does_not_add_a_line(self):
        for ending in ("", "\n"):
            self.write("f.go", "package sample" + ending)
            self.assertEqual(next(r["value"] for r in self.measure() if r["metric"] == "file_lines"), 1)

    def test_malformed_go_is_fatal_even_on_other_platform(self):
        self.write("bad_windows.go", "//go:build windows\n\npackage sample\nfunc broken(\n")
        with self.assertRaisesRegex(ValueError, "Go parse failed"):
            self.measure()

    def test_report_exclusions_and_measure_unverified_generated_files(self):
        for directory in (".tmp", "vendor", "node_modules", "references", ".git"):
            self.write(directory + "/bad.go", "malformed")
        self.write("generated.go", "// Code generated by synthetic; DO NOT EDIT.\npackage sample\n")
        self.write("normal.go", "package sample\n")
        paths, excluded = guard.discover(self.tree)
        self.assertEqual(paths, ["generated.go", "normal.go"])
        self.assertEqual({p for p, _ in excluded}, set(guard.EXCLUDED))
        self.assertEqual(len(self.measure()), 4)

    def test_wrapper_exit_status_and_exclusion_reporting(self):
        self.write("scripts/size-baseline.json", '{"version":1,"violations":[]}')
        self.write(".tmp/ignored.go", "malformed")
        self.write("long.go", "package sample\nfunc F() {\n" + "\n" * 149 + "}\n")
        output, errors = io.StringIO(), io.StringIO()
        with patch("sys.argv", ["guard", "--root", str(self.tree)]), \
                contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            self.assertEqual(guard.main(), 1)
        self.assertIn("EXCLUDED .tmp: tool scratch", output.getvalue())
        self.assertIn('"symbol": "F"', errors.getvalue())
        self.write("long.go", "package sample\nfunc F() {}\n")
        with patch("sys.argv", ["guard", "--root", str(self.tree)]), \
                contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(guard.main(), 0)
        self.write("long.go", "package sample\nfunc F(\n")
        with patch("sys.argv", ["guard", "--root", str(self.tree)]), \
                contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(guard.main(), 2)
        (self.tree / "long.go").unlink()
        with patch("sys.argv", ["guard", "--root", str(self.tree)]), \
                contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(guard.main(), 2)

    def test_symlink_is_fatal(self):
        self.write("real.go", "package sample\n")
        (self.tree / "alias.go").symlink_to("real.go")
        with self.assertRaisesRegex(ValueError, "symlink"):
            guard.discover(self.tree)


if __name__ == "__main__":
    unittest.main()
