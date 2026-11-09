"""Synthetic tests for finite diagnostic identities and tool failures."""
import importlib.util
import json
import re
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('guard', Path(__file__).resolve().parents[1] / 'check_go_lint_baseline.py')
guard = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(guard)


class BaselineTests(unittest.TestCase):
    def row(self, **changes):
        return dict(dict(path='sample.go', symbol='ReadValue', rule='revive/exported', count=2), **changes)

    def load(self, rows):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'baseline.json'
            path.write_text(json.dumps(dict(version=1, golangci_lint=guard.VERSION, entries=rows)))
            return guard.load_baseline(path)

    def test_equal(self):
        expected = self.load([self.row()])
        self.assertEqual([], guard.compare(expected, expected))

    def test_reduction(self):
        expected = self.load([self.row()])
        self.assertEqual([], guard.compare(expected, {next(iter(expected)): 1}))

    def test_growth(self):
        expected = self.load([self.row()])
        self.assertTrue(guard.compare(expected, {next(iter(expected)): 3}))

    def test_path_move(self):
        expected = self.load([self.row()])
        self.assertTrue(guard.compare(expected, self.load([self.row(path='other.go')])))

    def test_symbol_change(self):
        expected = self.load([self.row()])
        self.assertTrue(guard.compare(expected, self.load([self.row(symbol='OtherValue')])))

    def test_rule_change(self):
        expected = self.load([self.row()])
        self.assertTrue(guard.compare(expected, self.load([self.row(rule='revive/package-comments')])))

    def test_duplicate_rows(self):
        with self.assertRaises(ValueError):
            self.load([self.row(), self.row()])

    def test_stale_debt(self):
        self.assertTrue(guard.compare(self.load([self.row()]), {}))

    def test_invalid_counts(self):
        for count in (0, -1, True, 1.5, '2'):
            with self.subTest(count=count), self.assertRaises(ValueError):
                self.load([self.row(count=count)])

    def test_invalid_path(self):
        for path in ('../sample.go', '/sample.go', './sample.go'):
            with self.subTest(path=path), self.assertRaises(ValueError):
                self.load([self.row(path=path)])

    def test_unknown_rule(self):
        with self.assertRaises(ValueError):
            self.load([self.row(rule='govet')])

    def test_nonzero_tool_exit(self):
        result = subprocess.CompletedProcess([], 7, '', 'synthetic failure')
        with patch.object(guard.subprocess, 'run', return_value=result), self.assertRaisesRegex(ValueError, 'tool exit 7'):
            guard.checked_run(['synthetic-linter'])

    def test_timeout(self):
        with patch.object(guard.subprocess, 'run', side_effect=subprocess.TimeoutExpired('synthetic', 150)), self.assertRaises(subprocess.TimeoutExpired):
            guard.checked_run(['synthetic-linter'])

    def test_duplicate_json_fields(self):
        with self.assertRaises(ValueError):
            json.loads('{"version":1,"version":1}', object_pairs_hook=guard.unique_object)

    def diagnostic(self, linter, text):
        return guard.diagnostic_key(dict(Pos=dict(Filename='sample.go'), FromLinter=linter, Text=text))

    def test_standard_linter_identities(self):
        cases = [
            ('errcheck', 'Error return value of `io.WriteString` is not checked', 'io.WriteString', 'errcheck'),
            ('errcheck', 'Error return value is not checked', '<unnamed>', 'errcheck'),
            ('ineffassign', 'ineffectual assignment to value', 'value', 'ineffassign'),
            ('unused', 'func readValue is unused', 'readValue', 'unused'),
            ('staticcheck', 'SA1019: `Reader.Read` is deprecated', 'Reader.Read', 'staticcheck/SA1019'),
            ('staticcheck', 'QF1003: could use tagged switch', 'could', 'staticcheck/QF1003'),
            ('staticcheck', 'S1000: !!!', '<check>', 'staticcheck/S1000'),
            ('govet', 'printf: `fmt.Printf` format mismatch', 'fmt.Printf', 'govet/printf'),
            ('govet', 'copylocks: call copies lock value', 'call', 'govet/copylocks'),
        ]
        for linter, text, symbol, rule in cases:
            with self.subTest(linter=linter, text=text):
                key = self.diagnostic(linter, text)
                self.assertEqual(('sample.go', symbol, rule), key)
                self.assertEqual({key: 2}, self.load([self.row(symbol=symbol, rule=rule)]))
                self.assertTrue(guard.compare({key: 2}, {key: 3}))
                self.assertTrue(guard.compare({key: 2}, {}))

    def test_unbaselined_linter(self):
        for linter in ('other', 'errcheck', 'ineffassign', 'unused', 'staticcheck', 'govet'):
            with self.subTest(linter=linter), self.assertRaises(ValueError):
                self.diagnostic(linter, 'synthetic')

    def test_standard_rule_validation(self):
        for rule in ('staticcheck', 'staticcheck/unknown', 'staticcheck/SA1019/extra',
                     'govet/', 'govet/*', 'govet/analyzer/extra', 'other'):
            with self.subTest(rule=rule), self.assertRaises(ValueError):
                self.load([self.row(rule=rule)])

    def test_symbol_validation(self):
        for symbol in ('', ' ', '*', '?', '[value]', 'reader/read', 'reader\\read'):
            with self.subTest(symbol=symbol), self.assertRaises(ValueError):
                self.load([self.row(symbol=symbol, rule='errcheck')])

    def test_exported_identity(self):
        issue = dict(Pos=dict(Filename='sample.go'), FromLinter='revive', Text='exported: exported method Reader.ReadValue should have comment or be unexported')
        self.assertEqual(('sample.go', 'Reader.ReadValue', 'revive/exported'), guard.diagnostic_key(issue))

    def test_main_fails_closed(self):
        with patch('sys.argv', ['guard', '--check']), patch.object(guard, 'collect', side_effect=ValueError('synthetic tool failure')):
            self.assertEqual(1, guard.main())

    def test_diagnostic_path_identity(self):
        def issue(path):
            return dict(Pos=dict(Filename=path), FromLinter='revive', Text='exported: exported type ReadValue should have comment or be unexported')
        self.assertNotEqual(guard.diagnostic_key(issue('sample.go')), guard.diagnostic_key(issue('other.go')))

    def test_diagnostic_symbol_identity(self):
        def issue(symbol):
            return dict(Pos=dict(Filename='sample.go'), FromLinter='revive', Text=f'exported: exported type {symbol} should have comment or be unexported')
        self.assertNotEqual(guard.diagnostic_key(issue('ReadValue')), guard.diagnostic_key(issue('OtherValue')))

    def test_diagnostic_rule_identity(self):
        first = dict(Pos=dict(Filename='sample.go'), FromLinter='revive', Text='exported: exported type ReadValue should have comment or be unexported')
        second = dict(Pos=dict(Filename='sample.go'), FromLinter='revive', Text='package-comments: should have a package comment')
        self.assertNotEqual(guard.diagnostic_key(first)[2], guard.diagnostic_key(second)[2])

    def test_naming_identity(self):
        issue = dict(Pos=dict(Filename='sample.go', Column=9), FromLinter='revive', Text="var-naming: don't use an underscore in package name", SourceLines=['package sample_name'])
        self.assertEqual(('sample.go', 'sample_name', 'revive/var-naming'), guard.diagnostic_key(issue))
        self.assertTrue(guard.compare(self.load([self.row(rule='revive/var-naming')]), {guard.diagnostic_key(issue): 1}))

    def test_unbaselined_diagnostic(self):
        expected = self.load([self.row()])
        actual = dict(expected)
        actual[('new.go', 'NewValue', 'revive/exported')] = 1
        self.assertTrue(guard.compare(expected, actual))

    def test_wildcard_and_path_escape(self):
        for path in ('*.go', 'dir/../sample.go', 'dir\\sample.go'):
            with self.subTest(path=path), self.assertRaises(ValueError):
                self.load([self.row(path=path)])
        with self.assertRaises(ValueError):
            self.load([self.row(symbol='*')])

    def test_baseline_platform_matches_workflow_runner(self):
        root = Path(__file__).resolve().parents[2]
        workflow = (root / '.github' / 'workflows' / 'go-readability.yml').read_text()
        self.assertIn('runs-on: macos-latest', workflow)
        self.assertNotIn('ubuntu', workflow)
        entries = guard.load_baseline(root / 'scripts' / 'go-lint-baseline.json')
        for path, _symbol, _rule in entries:
            source = root / path
            self.assertTrue(source.is_file(), path)
            first = source.read_text().splitlines()[0]
            if first.startswith('//go:build'):
                expression = first[len('//go:build'):].strip()
                tags = {'darwin', 'cgo', 'unix'}
                evaluated = re.sub(r'[A-Za-z_][A-Za-z0-9_.]*', lambda m: str(m[0] in tags), expression)
                evaluated = evaluated.replace('&&', ' and ').replace('||', ' or ').replace('!', ' not ')
                self.assertTrue(eval(evaluated, {'__builtins__': {}}, {'True': True, 'False': False}), path)
