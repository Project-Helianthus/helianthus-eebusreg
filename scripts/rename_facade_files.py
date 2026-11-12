#!/usr/bin/env python3
"""Reproduce the bounded first-trust source rename without changing literals."""
from pathlib import Path
import re

MOVES = {
    'internal/eebusfacade/msp04c_backoff.go': 'internal/eebusfacade/first_trust_backoff.go',
    'internal/eebusfacade/msp04c_coordinator.go': 'internal/eebusfacade/first_trust_coordinator.go',
    'internal/eebusfacade/msp04c_revocation_repair.go': 'internal/eebusfacade/first_trust_revocation_repair.go',
    'internal/eebusfacade/msp04c_types.go': 'internal/eebusfacade/first_trust_types.go',
    'internal/eebusfacade/msp04cr2_predial.go': 'internal/eebusfacade/first_trust_predial.go',
    'internal/eebusinteropsmoke/msp04c_artifact.go': 'internal/eebusinteropsmoke/first_trust_artifact.go',
}
LEXEME = re.compile(r'//[^\n]*|/\*[\s\S]*?\*/|"(?:\\.|[^"\\])*"|`[^`]*`|\'(?:\\.|[^\'\\])*\'|[A-Za-z_][A-Za-z_0-9]*')


def rewrite(source):
    def token(match):
        value = match.group()
        if re.fullmatch(r'[A-Za-z_][A-Za-z_0-9]*', value):
            return value.replace('msp04c', 'firstTrust').replace('MSP04C', 'FirstTrust')
        return value
    return LEXEME.sub(token, source)


def main():
    for old, new in MOVES.items():
        source, target = Path(old), Path(new)
        if source.exists():
            if target.exists():
                raise SystemExit(f'refusing to overwrite {new}')
            source.rename(target)
    source = Path('internal/eebusinteropsmoke/first_trust_artifact.go')
    original = source.read_text()
    identifiers = {token for token in LEXEME.findall(original) if re.fullmatch(r'[A-Za-z_][A-Za-z_0-9]*', token) and re.search('msp04c', token, re.I)}
    for path in (source, Path('internal/eebusinteropsmoke/msp04c_report_red_test.go')):
        path.write_text(LEXEME.sub(lambda m: rewrite(m.group()) if m.group() in identifiers else m.group(), path.read_text()))
    for name in ('naming-inventory.json', 'scripts/behavior-name-baseline.json', 'scripts/go-lint-baseline.json', 'scripts/size-baseline.json'):
        path = Path(name)
        text = path.read_text()
        for old, new in MOVES.items():
            text = text.replace(old, new)
            text = text.replace('"' + Path(old).name + '"', '"' + Path(new).name + '"')
        for identifier in sorted(identifiers, key=len, reverse=True):
            text = re.sub(r'\b' + re.escape(identifier) + r'\b', rewrite(identifier), text)
        path.write_text(text)


if __name__ == '__main__':
    main()
