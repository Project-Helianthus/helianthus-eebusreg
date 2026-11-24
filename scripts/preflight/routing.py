#!/usr/bin/env python3
"""Select one manifest and one head-bound route for additive-rows.

The other workflows only read the saved result. They do not resolve
docs-semantic main and they do not classify a pull request themselves.
"""

from __future__ import annotations

import argparse
import io
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request
import zipfile
from pathlib import Path

DOCS_SEMANTIC_URL = "https://github.com/Project-Helianthus/helianthus-docs-semantic.git"
ARTIFACT_NAME = "additive-routing-result"
RESULT_NAME = "routing-result.json"
THIS_REPOSITORY = frozenset({"eebusreg", "helianthus-eebusreg"})
FULL_SUITE_IF = "github.event_name != 'pull_request' || steps.route.outputs.suppress != 'true'"
READ_RESULT_IF = "github.event_name == 'pull_request'"
SUPPRESSING = frozenset({"proven additive-only", "resolved lower member"})
REGISTRATION_FILE = "internal/apiboundary/export_inventory.go"
REGISTRATION_DIR = "internal/apiboundary/testdata/exports/"
# No recorded nightly cron was found in the approved cadence sources.
# The schedule trigger is not invented here.
NIGHTLY_CRON = None

PUBLIC_REPOSITORY_NAMES = {
    "gateway": "helianthus-gateway",
    "helianthus-ebusgateway": "helianthus-gateway",
    "semreg": "helianthus-semreg",
    "ebusreg": "helianthus-ebusreg",
    "ebusgo": "helianthus-ebusgo",
    "modbusreg": "helianthus-modbusreg",
    "modbus": "helianthus-modbus",
    "canbusreg": "helianthus-canbusreg",
    "canbus": "helianthus-canbus",
    "eebusreg": "helianthus-eebusreg",
    "eebus-go": "helianthus-eebus-go",
    "matterreg": "helianthus-matterreg",
    "helianthus-matter-binding": "helianthus-matterreg",
    "ha-integration": "helianthus-ha-integration",
    "vrc-explorer": "helianthus-vrc-explorer",
    "helianthus-vrc-explorer": "helianthus-vrc-explorer",
    "docs-semantic": "helianthus-docs-semantic",
    "addon": "helianthus-ha-addon",
    "helianthus-ha-addon": "helianthus-ha-addon",
}


def public_repository_name(repository):
    if repository not in PUBLIC_REPOSITORY_NAMES:
        raise KeyError(repository)
    return PUBLIC_REPOSITORY_NAMES[repository]


def public_repository_url(repository):
    return "https://github.com/Project-Helianthus/" + public_repository_name(repository) + ".git"


def is_registration_path(path):
    return path == REGISTRATION_FILE or path.startswith(REGISTRATION_DIR)


def is_driver_row(path):
    return path.startswith("driver/rows/") and path != "driver/rows/"


def proven_additive(changes, sealed):
    if not changes:
        return False
    for status, path in changes:
        if status != "A" or not is_driver_row(path) or path not in sealed:
            return False
    return True


def destination_sealed(manifest, repository_names):
    donor = (manifest.get("donor") or {}).get("repository")
    sealed = set()
    for entry in manifest.get("paths") or []:
        if entry.get("repository") == donor:
            continue
        if entry.get("repository") not in repository_names:
            continue
        sealed.update(entry.get("paths") or [])
    return sealed


def select_manifest(loaded, changed_paths, repository_names=THIS_REPOSITORY):
    """Return (path, manifest), ('ambiguous', matches), or (None, None).

    Coverage is the changed paths contained by one recorded row's sealed
    destination paths. Donor paths are not destination coverage. An equal
    tie is not treated as no match.
    """
    changed = [path for path in changed_paths if path]
    if not changed:
        return None, None
    covers = []
    for path, manifest in loaded:
        if not isinstance(manifest, dict):
            continue
        sealed = destination_sealed(manifest, repository_names)
        if sealed and set(changed) <= sealed:
            covers.append((path, manifest, sealed))
    if not covers:
        return None, None
    covers.sort(key=lambda item: (len(item[2]), str(item[0])))
    best_len = len(covers[0][2])
    best = [item for item in covers if len(item[2]) == best_len]
    if len(best) > 1:
        return "ambiguous", best
    return best[0][0], best[0][1]


def load_manifests(tool_root):
    directory = Path(tool_root) / "acceptance" / "manifests"
    loaded = []
    if not directory.is_dir():
        return loaded
    for path in sorted(directory.glob("*.json")):
        try:
            manifest = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            loaded.append((path, None))
            continue
        loaded.append((path, manifest))
    return loaded


def stack_position(current, pulls, complete):
    """Parent head matches child base by repository id and ref.

    A member with a child is lower. A member with a parent and no child is
    the tip. Neither relationship is unstacked. Incomplete data is unresolved.
    """
    if not complete or not isinstance(current, dict):
        return "unresolved"
    head_repo = current.get("head_repo_id")
    base_repo = current.get("base_repo_id")
    head_ref = current.get("head_ref")
    base_ref = current.get("base_ref")
    if head_repo is None or base_repo is None or not head_ref or not base_ref:
        return "unresolved"
    number = current.get("number")
    child = False
    parent = False
    for pull in pulls:
        if not isinstance(pull, dict):
            return "unresolved"
        if pull.get("number") == number:
            continue
        head = pull.get("head") or {}
        base = pull.get("base") or {}
        head_repository = (head.get("repo") or {}).get("id") if isinstance(head.get("repo"), dict) else None
        base_repository = (base.get("repo") or {}).get("id") if isinstance(base.get("repo"), dict) else None
        pull_head_ref = head.get("ref")
        pull_base_ref = base.get("ref")
        ref_related = pull_base_ref == head_ref or pull_head_ref == base_ref
        ids_missing = (
            head_repository is None
            or base_repository is None
            or not pull_head_ref
            or not pull_base_ref
        )
        if ref_related and ids_missing:
            return "unresolved"
        if pull_base_ref == head_ref and base_repository == head_repo:
            child = True
        if pull_head_ref == base_ref and head_repository == base_repo:
            parent = True
    if child:
        return "lower"
    if parent:
        return "tip"
    return "unstacked"


def decide(position, manifest_selected, proven, registration_edit, validation_ok, ambiguous=False):
    blank = {
        "classification": "no applicable manifest",
        "manifest_applicable": False,
        "suppress_full_suite": False,
        "run_package_tests": False,
        "run_compare": False,
        "run_preflight": False,
        "fail": False,
        "edits_export_inventory_registration": bool(registration_edit),
    }
    if ambiguous:
        failed = dict(blank)
        failed.update({
            "classification": "ambiguous manifest",
            "manifest_applicable": True,
            "fail": True,
        })
        return failed
    if not manifest_selected:
        return blank
    if not validation_ok:
        failed = dict(blank)
        failed.update({
            "classification": "selected manifest failed validation",
            "manifest_applicable": True,
            "fail": True,
        })
        return failed
    qualified = bool(proven) and not registration_edit
    if position == "tip":
        classification = "tip"
        compare = False
        preflight = False
        suppress = False
    elif position == "unresolved":
        classification = "unresolved"
        compare = True
        preflight = False
        suppress = False
    elif position == "lower" and qualified:
        classification = "proven additive-only"
        compare = True
        preflight = True
        suppress = True
    elif position == "lower":
        classification = "resolved lower member"
        compare = True
        preflight = False
        suppress = not registration_edit
    elif position == "unstacked" and qualified:
        classification = "proven additive-only"
        compare = True
        preflight = True
        suppress = True
    elif position == "unstacked":
        classification = "unstacked"
        compare = True
        preflight = False
        suppress = False
    else:
        classification = "unresolved"
        compare = True
        preflight = False
        suppress = False
    return {
        "classification": classification,
        "manifest_applicable": True,
        "suppress_full_suite": suppress,
        "run_package_tests": compare,
        "run_compare": compare,
        "run_preflight": preflight,
        "fail": False,
        "edits_export_inventory_registration": bool(registration_edit),
    }


def routing_result(head_sha, base_sha, docs_semantic_sha, manifest, decision):
    return {
        "head_sha": head_sha,
        "base_sha": base_sha,
        "docs_semantic_sha": docs_semantic_sha,
        "manifest": manifest,
        "classification": decision["classification"],
        "suppress_full_suite": decision["suppress_full_suite"],
        "edits_export_inventory_registration": decision["edits_export_inventory_registration"],
        "run_package_tests": decision["run_package_tests"],
        "run_compare": decision["run_compare"],
        "run_preflight": decision["run_preflight"],
    }


def consumer_suppresses(result, head_sha, base_sha):
    if not isinstance(result, dict):
        return False
    if result.get("head_sha") != head_sha or result.get("base_sha") != base_sha:
        return False
    if not result.get("manifest"):
        return False
    if not result.get("docs_semantic_sha"):
        return False
    if result.get("classification") not in SUPPRESSING:
        return False
    if result.get("edits_export_inventory_registration"):
        return False
    return bool(result.get("suppress_full_suite"))


def full_suite_runs(event_name, suppress):
    if event_name != "pull_request":
        return True
    return suppress != "true"


def reads_routing_result(event_name):
    return event_name == "pull_request"


def test_routes(result, event_name, head_sha, base_sha):
    routes = []
    if (
        event_name == "pull_request"
        and isinstance(result, dict)
        and (result.get("run_package_tests") or result.get("run_compare") or result.get("run_preflight"))
    ):
        routes.append("fast-path" if result.get("run_preflight") else "package-and-compare")
    if full_suite_runs(
        event_name,
        "true" if consumer_suppresses(result, head_sha, base_sha) else "false",
    ):
        routes.append("full-suite")
    return routes


def packages_of(paths):
    packages = []
    for path in paths:
        if not str(path).endswith(".go"):
            continue
        parent = str(Path(path).parent).replace("\\", "/")
        if parent in ("", "."):
            package = "."
        else:
            package = "./" + parent
        if package not in packages:
            packages.append(package)
    return packages


def parse_name_status(text):
    changes = []
    for line in text.splitlines():
        if not line.strip():
            continue
        parts = line.split("\t")
        status = parts[0]
        if status[:1] in {"R", "C"} and len(parts) >= 3:
            changes.append((status[:1], parts[-1]))
        elif len(parts) >= 2:
            changes.append((status[:1], parts[-1]))
    return changes


def git(repo, *args, check=True):
    command = ["git", "-C", str(repo), *args] if repo is not None else ["git", *args]
    completed = subprocess.run(command, text=True, capture_output=True, check=False)
    if check and completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "").strip()
        raise RuntimeError(detail or "git failed")
    return completed


def resolve_ref_sha(url, ref="refs/heads/main"):
    completed = git(None, "ls-remote", url, ref)
    lines = [line for line in completed.stdout.splitlines() if line.strip()]
    if len(lines) != 1:
        raise RuntimeError("docs-semantic ref did not resolve to one sha")
    sha = lines[0].split()[0]
    if len(sha) != 40 or any(character not in "0123456789abcdef" for character in sha):
        raise RuntimeError("docs-semantic ref did not resolve to one full sha")
    return sha


def checkout_commit(dest, url, revision):
    destination = Path(dest)
    if destination.exists():
        raise RuntimeError("checkout destination already exists")
    git(None, "clone", url, str(destination))
    git(destination, "fetch", "--depth", "1", "origin", revision)
    git(destination, "checkout", "--detach", revision)


def fetch_revision(checkout, repository, revision, url=None):
    remote = url if url is not None else public_repository_url(repository)
    git(checkout, "fetch", "--depth", "1", remote, revision)


def exemplar_revisions(manifest):
    found = []
    for pattern in manifest.get("patterns") or []:
        exemplar = pattern.get("exemplar") or {}
        item = (exemplar.get("repository"), exemplar.get("revision"))
        if all(item) and item not in found:
            found.append(item)
    return found


def reference_revisions(manifest):
    found = []

    def add(repository, revision):
        item = (repository, revision)
        if repository and revision and item not in found:
            found.append(item)

    for pattern in manifest.get("patterns") or []:
        candidate = pattern.get("candidate") or {}
        if candidate.get("form") == "reference":
            add(candidate.get("repository"), candidate.get("revision"))
            recorded = candidate.get("reference") or {}
            add(recorded.get("repository"), recorded.get("revision"))
    for expectation in manifest.get("expectations") or []:
        for reference in expectation.get("references") or []:
            add(reference.get("repository"), reference.get("revision"))
    for difference in manifest.get("expected_differences") or []:
        recorded = difference.get("reference") or {}
        add(recorded.get("repository"), recorded.get("revision"))
        add(difference.get("repository"), difference.get("revision"))
    return found


def fetch_exemplars(donor, manifest, url_for=None):
    for repository, revision in exemplar_revisions(manifest):
        url = None if url_for is None else url_for(repository, revision)
        fetch_revision(donor, repository, revision, url=url)


def fetch_references(tool, manifest, url_for=None):
    for repository, revision in reference_revisions(manifest):
        url = None if url_for is None else url_for(repository, revision)
        fetch_revision(tool, repository, revision, url=url)


def check_manifest(tool_root, manifest_path, donor):
    main = Path(tool_root) / "acceptance" / "tools" / "manifest" / "main.py"
    completed = subprocess.run(
        [sys.executable, str(main), "check", "--manifest", str(manifest_path), "--donor", str(donor)],
        text=True,
        capture_output=True,
        check=False,
    )
    return completed


def write_github_output(values):
    path = os.environ.get("GITHUB_OUTPUT")
    if not path:
        return
    with open(path, "a", encoding="utf-8") as handle:
        for key, value in values.items():
            text = "" if value is None else str(value)
            handle.write(key + "=" + text + "\n")


def github_json(url, token):
    request = urllib.request.Request(
        url,
        headers={
            "Authorization": "Bearer " + token,
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
            "User-Agent": "eebusreg-additive-rows",
        },
    )
    with urllib.request.urlopen(request) as response:
        payload = json.loads(response.read().decode("utf-8"))
        return payload, response.headers.get("Link", "")


def next_link(header):
    for part in header.split(","):
        if 'rel="next"' not in part:
            continue
        start = part.find("<")
        end = part.find(">")
        if start != -1 and end != -1:
            return part[start + 1:end]
    return None


def list_open_pulls(repository, token):
    url = "https://api.github.com/repos/" + repository + "/pulls?state=open&per_page=100"
    pulls = []
    pages = 0
    while url:
        pages += 1
        if pages > 50:
            return pulls, False
        try:
            payload, link = github_json(url, token)
        except (OSError, ValueError, urllib.error.URLError):
            return pulls, False
        if not isinstance(payload, list):
            return pulls, False
        pulls.extend(payload)
        url = next_link(link)
    return pulls, True


def current_pull(event):
    pull = event.get("pull_request") or {}
    head = pull.get("head") or {}
    base = pull.get("base") or {}
    head_repo = head.get("repo") if isinstance(head.get("repo"), dict) else None
    base_repo = base.get("repo") if isinstance(base.get("repo"), dict) else None
    return {
        "number": pull.get("number"),
        "head_repo_id": None if head_repo is None else head_repo.get("id"),
        "head_ref": head.get("ref"),
        "head_sha": head.get("sha"),
        "base_repo_id": None if base_repo is None else base_repo.get("id"),
        "base_ref": base.get("ref"),
        "base_sha": base.get("sha"),
    }


def changed_against(repo, base_sha, head_sha):
    git(repo, "fetch", "--no-tags", "--depth", "1", "origin", base_sha)
    fetched = git(repo, "fetch", "--no-tags", "--depth", "1", "origin", head_sha, check=False)
    right = head_sha if fetched.returncode == 0 else "HEAD"
    completed = git(repo, "diff", "--name-status", "--find-renames", base_sha, right)
    return parse_name_status(completed.stdout)


def relative_manifest(tool_root, manifest_path):
    if manifest_path is None:
        return None
    try:
        return Path(manifest_path).resolve().relative_to(Path(tool_root).resolve()).as_posix()
    except ValueError:
        return Path(manifest_path).as_posix()


def prepare(event, repo, result_path, tool_dest, donor_dest, token, repository):
    current = current_pull(event)
    docs_sha = resolve_ref_sha(DOCS_SEMANTIC_URL)
    checkout_commit(tool_dest, DOCS_SEMANTIC_URL, docs_sha)
    changes = changed_against(repo, current["base_sha"], current["head_sha"])
    changed_paths = [path for _status, path in changes]
    selected_path, selected = select_manifest(load_manifests(tool_dest), changed_paths)
    ambiguous = selected_path == "ambiguous"
    pulls, complete = list_open_pulls(repository, token) if token else ([], False)
    position = stack_position(current, pulls, complete and bool(token))
    manifest_rel = None
    validation_ok = False
    proven = False
    registration = any(is_registration_path(path) for path in changed_paths)
    donor_checkout = ""
    if ambiguous:
        decision = decide(position, True, False, registration, False, ambiguous=True)
    elif selected is None:
        decision = decide(position, False, False, registration, False)
    else:
        manifest_rel = relative_manifest(tool_dest, selected_path)
        sealed = destination_sealed(selected, THIS_REPOSITORY)
        proven = proven_additive(changes, sealed)
        donor = selected.get("donor") or {}
        checkout_commit(donor_dest, public_repository_url(donor["repository"]), donor["sha"])
        fetch_exemplars(donor_dest, selected)
        fetch_references(tool_dest, selected)
        checked = check_manifest(tool_dest, selected_path, donor_dest)
        validation_ok = checked.returncode == 0 and "check: ok" in checked.stdout
        if checked.stdout:
            sys.stderr.write(checked.stdout)
        if checked.stderr:
            sys.stderr.write(checked.stderr)
        donor_checkout = str(donor_dest)
        decision = decide(position, True, proven, registration, validation_ok)
    result = routing_result(
        current["head_sha"],
        current["base_sha"],
        docs_sha,
        manifest_rel,
        decision,
    )
    Path(result_path).write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    packages = " ".join(packages_of(changed_paths))
    outputs = {
        "suppress": "true" if decision["suppress_full_suite"] else "false",
        "run_package_tests": "true" if decision["run_package_tests"] else "false",
        "run_compare": "true" if decision["run_compare"] else "false",
        "run_preflight": "true" if decision["run_preflight"] else "false",
        "fail": "true" if decision["fail"] else "false",
        "packages": packages,
        "manifest": "" if selected_path in (None, "ambiguous") else str(selected_path),
        "donor": donor_checkout,
        "tool": str(tool_dest),
        "classification": decision["classification"],
    }
    write_github_output(outputs)
    return result


def download_bytes(url, token):
    """Download one artifact zip. The redirected object URL does not receive the token."""

    class StopRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None

    request = urllib.request.Request(
        url,
        headers={
            "Authorization": "Bearer " + token,
            "Accept": "application/vnd.github+json",
            "User-Agent": "eebusreg-additive-rows",
        },
    )
    opener = urllib.request.build_opener(StopRedirect)
    try:
        with opener.open(request) as response:
            return response.read()
    except urllib.error.HTTPError as error:
        if error.code not in (301, 302, 303, 307, 308):
            raise
        location = error.headers.get("Location")
        if not location:
            raise
        with urllib.request.urlopen(location) as response:
            return response.read()


def load_saved_result(repository, head_sha, token):
    if not token or not repository or not head_sha:
        return None
    query = (
        "https://api.github.com/repos/"
        + repository
        + "/actions/workflows/additive-rows.yml/runs?head_sha="
        + head_sha
        + "&event=pull_request&per_page=20"
    )
    try:
        payload, _link = github_json(query, token)
    except (OSError, ValueError, urllib.error.URLError):
        return None
    runs = payload.get("workflow_runs") if isinstance(payload, dict) else None
    if not isinstance(runs, list):
        return None
    for run in runs:
        run_id = run.get("id")
        if not run_id:
            continue
        try:
            artifacts, _link = github_json(
                "https://api.github.com/repos/" + repository + "/actions/runs/" + str(run_id) + "/artifacts",
                token,
            )
        except (OSError, ValueError, urllib.error.URLError):
            continue
        for artifact in artifacts.get("artifacts") or []:
            if artifact.get("name") != ARTIFACT_NAME or artifact.get("expired"):
                continue
            try:
                blob = download_bytes(artifact["archive_download_url"], token)
                with zipfile.ZipFile(io.BytesIO(blob)) as archive:
                    text = archive.read(RESULT_NAME).decode("utf-8")
                return json.loads(text)
            except (OSError, KeyError, ValueError, zipfile.BadZipFile):
                return None
    return None


def read_result(head_sha, base_sha, repository, token, write_path):
    result = load_saved_result(repository, head_sha, token)
    suppress = consumer_suppresses(result, head_sha, base_sha)
    if write_path and isinstance(result, dict):
        Path(write_path).write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    write_github_output({"suppress": "true" if suppress else "false"})
    return suppress


def checkout_tool(dest, head_sha, base_sha, result_path, url=None):
    recorded = None
    if result_path and Path(result_path).is_file() and head_sha and base_sha:
        try:
            result = json.loads(Path(result_path).read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            result = None
        if (
            isinstance(result, dict)
            and result.get("head_sha") == head_sha
            and result.get("base_sha") == base_sha
            and result.get("docs_semantic_sha")
        ):
            recorded = result["docs_semantic_sha"]
    remote = url or DOCS_SEMANTIC_URL
    sha = recorded or resolve_ref_sha(remote)
    checkout_commit(dest, remote, sha)
    return sha


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)

    prepare_command = commands.add_parser("prepare")
    prepare_command.add_argument("--result", required=True)
    prepare_command.add_argument("--tool", required=True)
    prepare_command.add_argument("--donor", required=True)

    read_command = commands.add_parser("read-result")
    read_command.add_argument("--head", required=True)
    read_command.add_argument("--base", required=True)
    read_command.add_argument("--write")

    tool_command = commands.add_parser("checkout-tool")
    tool_command.add_argument("--dest", required=True)
    tool_command.add_argument("--result")
    tool_command.add_argument("--head", default="")
    tool_command.add_argument("--base", default="")

    args = parser.parse_args(argv)
    if args.command == "prepare":
        event_path = os.environ.get("GITHUB_EVENT_PATH")
        if not event_path:
            print("GITHUB_EVENT_PATH is required", file=sys.stderr)
            return 1
        event = json.loads(Path(event_path).read_text(encoding="utf-8"))
        prepare(
            event,
            Path.cwd(),
            args.result,
            args.tool,
            args.donor,
            os.environ.get("GITHUB_TOKEN", ""),
            os.environ.get("GITHUB_REPOSITORY", ""),
        )
        return 0
    if args.command == "read-result":
        read_result(
            args.head,
            args.base,
            os.environ.get("GITHUB_REPOSITORY", ""),
            os.environ.get("GITHUB_TOKEN", ""),
            args.write,
        )
        return 0
    if args.command == "checkout-tool":
        sha = checkout_tool(args.dest, args.head, args.base, args.result)
        print(sha)
        return 0
    return 2


if __name__ == "__main__":
    sys.exit(main())
