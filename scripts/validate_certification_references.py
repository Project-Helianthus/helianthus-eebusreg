#!/usr/bin/env python3
"""Validate a certification-references document and run its mutation check.

``python3 -I scripts/validate_certification_references.py FILE`` checks the
document against the shared schema and the record rules and prints one stable
failure reason. ``--mutation-check FILE`` accepts the document, then applies
mutations that each must be rejected with an exact reason, entry and member.
"""
import copy
import datetime
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = ROOT / "driverdata/certification/references.schema.json"
CONTRACT = "helianthus.semreg.certification-references/v1"
SOURCE = "certification"
COMMON = ("source", "category", "driver", "vendor", "flavor", "model", "firmware")
LOCATION = ("issuer", "url", "retrieved_on")
CATEGORY_MEMBERS = {
    "reference": COMMON + ("issuer", "certificate_number", "issue_date", "stated_value", "url", "retrieved_on"),
    "gap": COMMON + LOCATION,
    "unavailable": COMMON + LOCATION + ("refusal",),
}
TEXT_MEMBERS = ("driver", "vendor", "flavor", "model", "issuer", "certificate_number")
DATE_MEMBERS = ("issue_date", "retrieved_on")
IDENTITY = ("driver", "vendor", "flavor", "model", "firmware")
STATED_MEMBERS = (
    "measurement_type", "measured", "ratio_maximum", "measured_minimum",
    "measured_maximum", "range_minimum", "range_maximum",
)
DECIMAL_MEMBERS = ("ratio_maximum", "measured_minimum", "measured_maximum", "range_minimum", "range_maximum")
UPPER_BOUND_SOURCES = ("certificate", "vendor_document")
FIRMWARE_FORMS = {"not_stated": (), "release": ("release",), "range": ("from", "to", "upper_bound_stated_by")}
RELEASE = re.compile(r"[0-9]+(\.[0-9]+)*")
DATE = re.compile(r"[0-9]{4}-[0-9]{2}-[0-9]{2}")
COEFFICIENT = re.compile(r"0|-?[1-9]([0-9]*[1-9])?")
SCHEMA_KEYWORDS = frozenset({
    "$schema", "$id", "$defs", "$ref", "title", "description", "type", "const", "enum",
    "pattern", "minLength", "minimum", "maximum", "minItems", "items", "required",
    "properties", "additionalProperties", "oneOf", "anyOf",
})


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"repeated object key {key!r}")
        result[key] = value
    return result


def refuse_constant(name):
    raise ValueError(f"non-JSON constant {name}")


def load_json(path):
    with open(path, encoding="utf-8") as handle:
        return json.load(handle, object_pairs_hook=unique_object, parse_constant=refuse_constant)


def json_equal(left, right):
    if isinstance(left, bool) or isinstance(right, bool):
        return isinstance(left, bool) and isinstance(right, bool) and left == right
    if isinstance(left, (list, tuple)) and isinstance(right, (list, tuple)):
        return len(left) == len(right) and all(map(json_equal, left, right))
    if isinstance(left, dict) and isinstance(right, dict):
        return left.keys() == right.keys() and all(json_equal(left[key], right[key]) for key in left)
    return type(left) is type(right) and left == right


def has_type(value, name):
    if name == "object":
        return isinstance(value, dict)
    if name == "array":
        return isinstance(value, list)
    if name == "string":
        return isinstance(value, str)
    if name == "boolean":
        return isinstance(value, bool)
    if name == "integer":
        return isinstance(value, int) and not isinstance(value, bool)
    raise ValueError(f"unsupported type {name}")


def schema_errors(instance, node, root):
    """Return the errors of an instance against the schema keywords this contract uses."""
    unknown = set(node) - SCHEMA_KEYWORDS
    if unknown:
        raise ValueError(f"unsupported schema keyword {sorted(unknown)[0]}")
    if "$ref" in node:
        match = re.fullmatch(r"#/\$defs/([A-Za-z0-9_.-]+)", node["$ref"])
        if match is None or match.group(1) not in root.get("$defs", {}):
            raise ValueError(f"unresolved reference {node['$ref']}")
        return schema_errors(instance, root["$defs"][match.group(1)], root)
    found = []
    if "type" in node and not has_type(instance, node["type"]):
        return ["type"]
    if "const" in node and not json_equal(instance, node["const"]):
        found.append("const")
    if "enum" in node and not any(json_equal(instance, option) for option in node["enum"]):
        found.append("enum")
    if isinstance(instance, str):
        if "pattern" in node and re.search(node["pattern"].replace("$", r"\Z"), instance) is None:
            found.append("pattern")
        if len(instance) < node.get("minLength", 0):
            found.append("minLength")
    if isinstance(instance, int) and not isinstance(instance, bool):
        if "minimum" in node and instance < node["minimum"]:
            found.append("minimum")
        if "maximum" in node and instance > node["maximum"]:
            found.append("maximum")
    if isinstance(instance, list):
        if len(instance) < node.get("minItems", 0):
            found.append("minItems")
        for item in instance if "items" in node else []:
            found += schema_errors(item, node["items"], root)
    if isinstance(instance, dict):
        found += [f"required {name}" for name in node.get("required", []) if name not in instance]
        properties = node.get("properties", {})
        for name in sorted(set(instance) & set(properties)):
            found += schema_errors(instance[name], properties[name], root)
        if node.get("additionalProperties") is False:
            found += [f"member {name}" for name in sorted(set(instance) - set(properties))]
    if "anyOf" in node and not any(not schema_errors(instance, branch, root) for branch in node["anyOf"]):
        found.append("anyOf")
    if "oneOf" in node:
        matched = sum(not schema_errors(instance, branch, root) for branch in node["oneOf"])
        if matched != 1:
            found.append("oneOf")
    return found


def release_key(release):
    return tuple(int(part) for part in release.split("."))


def compare_releases(left, right):
    a, b = release_key(left), release_key(right)
    size = max(len(a), len(b))
    a, b = a + (0,) * (size - len(a)), b + (0,) * (size - len(b))
    return (a > b) - (a < b)


def is_text(value):
    return isinstance(value, str) and bool(value)


def is_date(value):
    if not isinstance(value, str) or not DATE.fullmatch(value):
        return False
    try:
        datetime.date.fromisoformat(value)
    except ValueError:
        return False
    return True


def is_url(value):
    return isinstance(value, str) and re.fullmatch(r"https://[^ ]+", value) is not None


def is_decimal(value):
    if not isinstance(value, dict) or set(value) != {"coefficient", "exponent10"}:
        return False
    coefficient, exponent = value["coefficient"], value["exponent10"]
    if not isinstance(coefficient, str) or not COEFFICIENT.fullmatch(coefficient):
        return False
    if type(exponent) is not int or not -18 <= exponent <= 18:
        return False
    return coefficient != "0" or exponent == 0


def first_member(entry, names, test):
    return next((name for name in names if name in entry and test(entry[name])), None)


def finding(reason, entry=None, member=None):
    return {"reason": reason, "entry": entry, "member": member}


def check_source(entry):
    if "source" in entry and entry["source"] != SOURCE:
        return finding("source_not_certification", member="source")
    return None


def check_category(entry):
    if "category" not in entry:
        return finding("missing_member", member="category")
    if entry["category"] not in CATEGORY_MEMBERS:
        return finding("invalid_value", member="category")
    return None


def check_members(entry):
    allowed = CATEGORY_MEMBERS[entry["category"]]
    if entry["category"] != "reference" and "stated_value" in entry:
        return finding("stated_value_not_allowed", member="stated_value")
    extra = sorted(set(entry) - set(allowed))
    if extra:
        return finding("unknown_member", member=extra[0])
    if entry["category"] == "unavailable" and not is_text(entry.get("refusal")):
        return finding("refusal_missing", member="refusal")
    missing = [name for name in allowed if name not in entry]
    return finding("missing_member", member=missing[0]) if missing else None


def check_values(entry):
    bad = first_member(entry, TEXT_MEMBERS, lambda value: not is_text(value))
    bad = bad or first_member(entry, DATE_MEMBERS, lambda value: not is_date(value))
    bad = bad or first_member(entry, ("url",), lambda value: not is_url(value))
    return finding("invalid_value", member=bad) if bad else None


def check_firmware(entry):
    firmware = entry["firmware"]
    if not isinstance(firmware, dict) or firmware.get("form") not in FIRMWARE_FORMS:
        return finding("firmware_invalid", member="firmware")
    if firmware["form"] == "range" and firmware.get("upper_bound_stated_by") not in UPPER_BOUND_SOURCES:
        return finding("firmware_upper_bound_unstated", member="firmware")
    if set(firmware) != {"form", *FIRMWARE_FORMS[firmware["form"]]}:
        return finding("firmware_invalid", member="firmware")
    releases = [firmware[name] for name in ("release", "from", "to") if name in firmware]
    if not all(isinstance(item, str) and RELEASE.fullmatch(item) for item in releases):
        return finding("firmware_invalid", member="firmware")
    if firmware["form"] == "range" and compare_releases(firmware["from"], firmware["to"]) > 0:
        return finding("firmware_invalid", member="firmware")
    return None


def check_stated_value(entry):
    if "stated_value" not in entry:
        return None
    stated = entry["stated_value"]
    problem = finding("stated_value_invalid", member="stated_value")
    if not isinstance(stated, dict) or not set(stated) <= set(STATED_MEMBERS):
        return problem
    measurement = stated.get("measurement_type")
    if not isinstance(measurement, str) or not re.fullmatch(r"[a-z][a-z0-9_]*", measurement):
        return problem
    values = set(stated) - {"measurement_type"}
    if not values or ("measured" in values and type(stated["measured"]) is not bool):
        return problem
    if not all(is_decimal(stated[name]) for name in values & set(DECIMAL_MEMBERS)):
        return problem
    return None


ENTRY_CHECKS = (check_source, check_category, check_members, check_values, check_firmware, check_stated_value)


def check_entry(entry, index):
    if not isinstance(entry, dict):
        return finding("schema", index)
    for check in ENTRY_CHECKS:
        found = check(entry)
        if found:
            return {**found, "entry": index}
    return None


def check_categories(entries):
    seen = {}
    for index, entry in enumerate(entries):
        key = json.dumps([entry[name] for name in IDENTITY], sort_keys=True)
        if key in seen and seen[key] != entry["category"]:
            return finding("category_conflict", index)
        seen.setdefault(key, entry["category"])
    return None


def check_document(document, schema):
    """Return the first finding of a document, or None when it is valid."""
    entries = document.get("entries") if isinstance(document, dict) else None
    if not isinstance(entries, list):
        return finding("schema")
    for index, entry in enumerate(entries):
        found = check_entry(entry, index)
        if found:
            return found
    if schema_errors(document, schema, schema):
        return finding("schema")
    return check_categories(entries)


def outcome(found):
    if found is None:
        return {"accepted": True}
    result = {"accepted": False, "reason": found["reason"]}
    for name in ("entry", "member"):
        if found[name] is not None:
            result[name] = found[name]
    return result


def other_category(entry):
    clone = copy.deepcopy(entry)
    if entry["category"] == "gap":
        clone.update(category="unavailable", refusal="The registry refused automated access.")
    elif entry["category"] == "unavailable":
        clone.update(category="gap")
        del clone["refusal"]
    else:
        clone["category"] = "gap"
        for name in ("certificate_number", "issue_date", "stated_value"):
            del clone[name]
    return clone


def entry_mutations(entry, index):
    """Yield (name, entry mutation, expected outcome) for one valid entry."""
    category = entry["category"]

    def at(reason, member=None):
        return outcome(finding(reason, index, member))

    def edited(change):
        clone = copy.deepcopy(entry)
        change(clone)
        return clone

    for name in CATEGORY_MEMBERS[category]:
        reason = "refusal_missing" if name == "refusal" else "missing_member"
        yield f"drop {name}", edited(lambda e, n=name: e.pop(n)), at(reason, name)
    yield "other source", edited(lambda e: e.update(source="device")), at("source_not_certification", "source")
    yield "unknown category", edited(lambda e: e.update(category="other")), at("invalid_value", "category")
    yield "device value member", edited(lambda e: e.update(device_published_value="1")), at("unknown_member", "device_published_value")
    for name in TEXT_MEMBERS:
        if name in entry:
            yield f"empty {name}", edited(lambda e, n=name: e.update({n: ""})), at("invalid_value", name)
    for name in DATE_MEMBERS:
        if name in entry:
            yield f"impossible {name}", edited(lambda e, n=name: e.update({n: "2000-02-30"})), at("invalid_value", name)
    yield "plain url", edited(lambda e: e.update(url="http://example.invalid/a")), at("invalid_value", "url")
    yield "range without bound", edited(lambda e: e.update(firmware={"form": "range", "from": "1", "to": "2"})), at("firmware_upper_bound_unstated", "firmware")
    yield "range reversed", edited(lambda e: e.update(firmware={"form": "range", "from": "2", "to": "1", "upper_bound_stated_by": "certificate"})), at("firmware_invalid", "firmware")
    yield "release not numeric", edited(lambda e: e.update(firmware={"form": "release", "release": "v1"})), at("firmware_invalid", "firmware")
    yield "unknown firmware form", edited(lambda e: e.update(firmware={"form": "ceiling"})), at("firmware_invalid", "firmware")
    if category == "reference":
        yield "null stated member", edited(lambda e: e["stated_value"].update(measured=None)), at("stated_value_invalid", "stated_value")
        yield "empty stated value", edited(lambda e: e.update(stated_value={"measurement_type": "active_power"})), at("stated_value_invalid", "stated_value")
    else:
        yield "stated value on non-reference", edited(lambda e: e.update(stated_value={"measurement_type": "active_power", "measured": True})), at("stated_value_not_allowed", "stated_value")
    if category == "unavailable":
        yield "empty refusal", edited(lambda e: e.update(refusal="")), at("refusal_missing", "refusal")
    if category == "gap":
        yield "refusal on gap", edited(lambda e: e.update(refusal="refused")), at("unknown_member", "refusal")


def mutations(document):
    """Yield (name, mutated document, expected outcome) for a valid document."""
    entries = document["entries"]
    for index, entry in enumerate(entries):
        for name, mutated, expected in entry_mutations(entry, index):
            clone = copy.deepcopy(document)
            clone["entries"][index] = mutated
            yield f"entry {index}: {name}", clone, expected
        clone = copy.deepcopy(document)
        clone["entries"].append(other_category(entry))
        yield f"entry {index}: other category for one identity", clone, outcome(finding("category_conflict", len(entries)))
    for name, change in (
        ("no entries", lambda d: d.update(entries=[])),
        ("other contract", lambda d: d.update(contract="other")),
        ("extra document member", lambda d: d.update(extra=1)),
        ("entries not an array", lambda d: d.update(entries={})),
    ):
        clone = copy.deepcopy(document)
        change(clone)
        yield name, clone, outcome(finding("schema"))


def mutation_check(document, schema):
    """Return the failures of the mutation check; empty when every mutation is rejected as expected."""
    actual = outcome(check_document(document, schema))
    if actual != {"accepted": True}:
        return [f"the document itself is rejected: {actual}"]
    failures = []
    for name, mutated, expected in mutations(document):
        try:
            actual = outcome(check_document(mutated, schema))
        except (KeyError, TypeError) as error:
            actual = f"validator raised {type(error).__name__}"
        if actual != expected:
            failures.append(f"{name}: outcome {actual}, expected {expected}")
    return failures


def main(argv=None):
    args = sys.argv[1:] if argv is None else list(argv)
    check_mutations = args[:1] == ["--mutation-check"]
    paths = args[1:] if check_mutations else args
    if len(paths) != 1:
        print("usage: validate_certification_references.py [--mutation-check] FILE", file=sys.stderr)
        return 2
    try:
        document = load_json(paths[0])
        schema = load_json(SCHEMA)
        if check_mutations:
            failures = mutation_check(document, schema)
            for failure in failures:
                print(f"FAIL: {failure}", file=sys.stderr)
            if failures:
                return 1
            print(f"PASS: {sum(1 for _ in mutations(document))} mutations of {paths[0]} are rejected")
            return 0
        actual = outcome(check_document(document, schema))
    except (OSError, ValueError, KeyError, TypeError, RecursionError) as error:
        print(f"FAIL: {error}", file=sys.stderr)
        return 1
    if not actual["accepted"]:
        print(f"FAIL: {json.dumps(actual, sort_keys=True)}", file=sys.stderr)
        return 1
    print(f"PASS: {paths[0]} holds {len(document['entries'])} certification reference entries")
    return 0


if __name__ == "__main__":
    sys.exit(main())
