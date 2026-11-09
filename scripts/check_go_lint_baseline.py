#!/usr/bin/env python3
"""Check finite lint debt while keeping new or increased findings fatal."""
import argparse
from collections import Counter
import json
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
VERSION = '2.11.4'


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('duplicate JSON key: ' + key)
        result[key] = value
    return result


def load_baseline(path):
    data = json.loads(path.read_text(), object_pairs_hook=unique_object)
    if set(data) != {'version', 'golangci_lint', 'entries'} or type(data['version']) is not int or data['version'] != 1 or data['golangci_lint'] != VERSION:
        raise ValueError('unsupported baseline format or linter version')
    if not isinstance(data['entries'], list):
        raise ValueError('entries must be a list')
    counts = {}
    for row in data['entries']:
        if not isinstance(row, dict) or set(row) != {'path', 'symbol', 'rule', 'count'}:
            raise ValueError('malformed baseline row')
        if any(not isinstance(row[k], str) or not row[k].strip() for k in ('path', 'symbol', 'rule')):
            raise ValueError('empty baseline identity')
        path = PurePosixPath(row['path'])
        if path.is_absolute() or '..' in path.parts or str(path) != row['path'] or not row['path'].endswith('.go') or any(c in row['path'] for c in '*?[]\\') or any(c in row['symbol'] for c in '*?[]/\\'):
            raise ValueError('noncanonical baseline path')
        if row['rule'] not in {'revive/exported', 'revive/package-comments', 'revive/var-naming', 'errcheck', 'ineffassign', 'unused'} and not re.fullmatch(r'(?:govet/[A-Za-z_][A-Za-z0-9_]*|staticcheck/[A-Z]+[0-9]+)', row['rule']):
            raise ValueError('rule cannot be exempted')
        key = tuple(row[k] for k in ('path', 'symbol', 'rule'))
        if key in counts or type(row['count']) is not int or row['count'] <= 0:
            raise ValueError('duplicate row or invalid finite count')
        counts[key] = row['count']
    return counts


def diagnostic_key(issue):
    path = Path(issue['Pos']['Filename'])
    if path.is_absolute():
        path = path.relative_to(ROOT)
    path = path.as_posix()
    if '..' in PurePosixPath(path).parts or str(PurePosixPath(path)) != path or not path.endswith('.go') or any(c in path for c in '*?[]\\'):
        raise ValueError('invalid diagnostic path')
    text = issue['Text']
    linter = issue['FromLinter']
    if linter != 'revive':
        if linter == 'errcheck':
            match = re.fullmatch(r'Error return value(?: of `([^`]+)`)? is not checked', text)
            if match:
                return path, match[1] or '<unnamed>', linter
        elif linter == 'ineffassign':
            match = re.fullmatch(r'ineffectual assignment to ([A-Za-z_][A-Za-z0-9_]*)', text)
            if match:
                return path, match[1], linter
        elif linter == 'unused':
            match = re.fullmatch(r'.*\b([A-Za-z_][A-Za-z0-9_.]*) is unused', text)
            if match:
                return path, match[1], linter
        elif linter in {'staticcheck', 'govet'}:
            code_pattern = r'[A-Z]+[0-9]+' if linter == 'staticcheck' else r'[A-Za-z_][A-Za-z0-9_]*'
            match = re.fullmatch('(' + code_pattern + r'):\s*(.+)', text)
            if match:
                token = re.search(r'`([^`]+)`|([A-Za-z_][A-Za-z0-9_.]*)', match[2])
                symbol = (token[1] or token[2]) if token else '<check>'
                return path, symbol, linter + '/' + match[1]
        raise ValueError('unrecognized lint diagnostic: ' + text)
    rule, _, message = text.partition(': ')
    if rule == 'exported':
        match = re.match(r'exported (?:const|var|type|function|method) ([\w.]+)\b', message)
        if not match:
            raise ValueError('unrecognized exported diagnostic: ' + text)
        symbol = match[1]
    elif rule == 'package-comments':
        symbol = '<package>'
    elif rule == 'var-naming':
        match = re.search(r"(?:^|; )(?:var|const|type|func|method|receiver|parameter|argument|struct field) ([A-Za-z_][A-Za-z0-9_]*) should be ", message)
        if match:
            symbol = match[1]
        else:
            lines = issue.get('SourceLines')
            column = issue['Pos'].get('Column')
            if not isinstance(lines, list) or not lines or not isinstance(lines[0], str) or type(column) is not int or column < 1:
                raise ValueError('missing var-naming source position')
            match = re.match(r'[A-Za-z_][A-Za-z0-9_]*', lines[0][column - 1:])
            if not match:
                raise ValueError('unrecognized var-naming diagnostic: ' + text)
            symbol = match[0]
    else:
        raise ValueError('unbaselined readability diagnostic: ' + text)
    return path, symbol, 'revive/' + rule


def compare(expected, actual):
    errors = []
    for key, count in sorted(actual.items()):
        if count > expected.get(key, 0):
            errors.append(f'new or increased debt {key}: {count} > {expected.get(key, 0)}')
    for key in sorted(expected):
        if not actual.get(key):
            errors.append(f'unused exemption {key}: remove the baseline row')
    return errors


def checked_run(command):
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=150)
    if result.returncode:
        raise ValueError(f'tool exit {result.returncode}: {result.stderr.strip()}')
    return result


def collect():
    version = checked_run(['golangci-lint', 'version']).stdout
    if not re.search(r'\bversion ' + re.escape(VERSION) + r'\b', version):
        raise ValueError('golangci-lint version mismatch')
    with tempfile.TemporaryDirectory() as directory:
        output = Path(directory) / 'issues.json'
        checked_run(['golangci-lint', 'run', '--config=.golangci.yml', '--enable=revive',
                     '--issues-exit-code=0', '--uniq-by-line=false', '--output.json.path=' + str(output),
                     '--output.text.path=/dev/null', './...'])
        data = json.loads(output.read_text(), object_pairs_hook=unique_object)
        if not isinstance(data, dict) or set(data) != {'Issues', 'Report'} or not isinstance(data['Issues'], (list, type(None))) or not isinstance(data['Report'], dict) or not isinstance(data['Report'].get('Linters'), list):
            raise ValueError('malformed linter output')
        issues = data['Issues'] or []
        for issue in issues:
            print(f"debt: {issue['Pos']['Filename']}: {issue['Text']}")
        return Counter(diagnostic_key(issue) for issue in issues)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', required=True)
    parser.parse_args()
    try:
        expected = load_baseline(ROOT / 'scripts/go-lint-baseline.json')
        actual = collect()
        errors = compare(expected, actual)
        for error in errors:
            print(error, file=sys.stderr)
        print(f'{sum(actual.values())} diagnostics; {len(expected)} finite exemptions')
        return bool(errors)
    except (OSError, ValueError, KeyError, TypeError, subprocess.TimeoutExpired) as error:
        print(f'lint baseline failed: {error}', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
