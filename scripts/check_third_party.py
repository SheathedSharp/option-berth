#!/usr/bin/env python3
"""Verify pinned third-party source data offline; never extract or execute it."""
import argparse
import json
import os
from pathlib import Path, PurePosixPath
import posixpath
import re
import hashlib
import stat
import subprocess
import tarfile
import zipfile

from collect_third_party import GO, ROOT, module_h1, records, sha, render_inventory

from source_archives import audit_archive, safe_name


def local_path(root, name):
    safe_name(name)
    path = root / name
    if not path.resolve().is_relative_to(root.resolve()):
        raise ValueError("path leaves repository: " + name)
    return path


def verify_manifest(root, manifest, check_graph=True):
    errors, registered, audited = [], set(), {}
    for name, expected in manifest["inputs"].items():
        path = local_path(root,name)
        if not path.is_file() or sha(path) != expected:
            errors.append("dependency input changed: " + name)
    sums = {}
    for line in (root/"engine/go.sum").read_text().splitlines():
        module,version,value = line.split(); sums[module,version] = value
    by_id = {c["id"]:c for c in manifest["components"]}
    for component in manifest["components"]:
        label = component["id"] + "@" + component["version"]
        if not any(a["kind"] == "full-upstream-source-archive" for a in component["archives"]):
            errors.append("missing full upstream source (module ZIP alone is not a whole project): " + label)
        if not component["archives"] or component.get("gaps"):
            errors.append("missing complete source: " + label)
        for entry in component["archives"]:
            name = entry["path"]; registered.add(name)
            path = local_path(root,name)
            if not path.is_file():
                errors.append("missing archive: " + name); continue
            if path.stat().st_size != entry["bytes"] or sha(path) != entry["sha256"]:
                errors.append(name + ": sha256/bytes mismatch before decompression"); continue
            if name not in audited:
                try: audited[name] = audit_archive(path)
                except (ValueError, tarfile.TarError, zipfile.BadZipFile) as error:
                    errors.append(name + ": " + str(error)); continue
            actual = audited[name]
            for key in ("sha256","bytes","files","source_bytes","license_files"):
                if actual[key] != entry[key]: errors.append(name + ": " + key + " mismatch")
            if not actual["source_files"]: errors.append("archive contains no source files: " + name)
            if actual["unsafe_symlinks"] != entry.get("unsafe_symlinks",[]):
                errors.append("unacknowledged unsafe source-fixture symlinks: " + name)
            if actual["submodule_manifests"]:
                dependencies = component.get("submodule_dependencies",[])
                if not dependencies:
                    errors.append("unresolved submodules require explicit source archives: " + name)
                for dependency in dependencies:
                    child = by_id.get(dependency["component"],{})
                    if not any(a.get("commit") == dependency["commit"] and a.get("repository") == dependency["repository"] for a in child.get("archives",[])):
                        errors.append("missing pinned submodule source: " + dependency["component"])
            if "go_sum_h1" in entry:
                value = module_h1(path)
                if value != entry["go_sum_h1"] or value != sums.get((component["id"], component["version"])):
                    errors.append("module h1/go.sum mismatch: " + label)
    for path in (root/"third_party/sources").rglob("*"):
        if path.is_file() and path.relative_to(root).as_posix() not in registered:
            errors.append("unregistered source archive: " + path.relative_to(root).as_posix())
    for entry in manifest.get("license_documents",[]):
        path = local_path(root, entry["path"])
        if not path.is_file() or sha(path) != entry["sha256"] or path.stat().st_size != entry["bytes"]:
            errors.append("license document mismatch: " + entry["path"])
    if check_graph:
        env = dict(os.environ, GOPROXY="off", GOWORK="off", GOTOOLCHAIN="local")
        graph = subprocess.check_output([GO,"mod","graph"],cwd=root/"engine",env=env,text=True)
        canonical = ("\n".join(sorted(graph.splitlines()))+"\n").encode()
        if hashlib.sha256(canonical).hexdigest() != manifest["module_graph"]["sha256"]:
            errors.append("strict offline Go-pruned graph differs from archived evidence")
        errors.extend(verify_source_bindings(root,manifest))
        text = subprocess.check_output([GO,"list","-mod=readonly","-m","-e","-json","all"],cwd=root/"engine",env=env,text=True)
        selected = {(r["Path"],r["Version"]) for r in records(text) if not r.get("Main")}
        archived = {(r["id"],r["version"]) for r in manifest["components"] if r["scope"] in ("production-package-closure","selected-test-or-generation-graph")}
        if selected != archived: errors.append("selected Go dependency graph differs from source inventory")
        production = set()
        for platform in manifest["platforms"]:
            goos,goarch = platform.split("/")
            text = subprocess.check_output([GO,"list","-mod=readonly","-deps","-json","./..."],cwd=root/"engine",env=dict(env,GOOS=goos,GOARCH=goarch,CGO_ENABLED="0"),text=True)
            production |= {r["Module"]["Path"] for r in records(text) if r.get("Module") and not r["Module"].get("Main")}
        expected = {r["id"] for r in manifest["components"] if r["scope"] == "production-package-closure"}
        if production != expected: errors.append("six-platform production package closure differs from inventory")
    return errors


def verify_source_bindings(root, manifest):
    errors = []
    by_id = {c["id"]:c for c in manifest["components"]}
    if (root/"third_party/README.md").read_text() != render_inventory(manifest):
        errors.append("generated source index is stale")
    legal = {d["path"] for d in manifest["license_documents"]}
    for component in manifest["components"]:
        if not component.get("license_documents") or not set(component["license_documents"]) <= legal:
            errors.append("missing bound original license document: " + component["id"])
    for key in ("module_graph", "source_trees"):
        record = manifest[key]; path = local_path(root,record["path"])
        if not path.is_file() or sha(path) != record["sha256"]:
            errors.append("source evidence mismatch: " + key)
    trees = json.loads((root / manifest["source_trees"]["path"]).read_text())
    for c in manifest["components"]:
        for a in c["archives"]:
            if not a.get("repository", "").startswith("https://github.com/"): continue
            tree = trees.get(a["path"])
            if not tree or tree["commit"] != a["commit"] or tree["omitted_tracked_files"] or tree["unexpected_archive_files"]:
                errors.append("full upstream Git tree is not covered: " + a["path"]); continue
            actual = {(x["path"],x["commit"]) for x in tree["gitlinks"]}
            included = {(x["path"],x["commit"]) for x in c.get("submodule_dependencies",[])}
            if actual != included: errors.append("Gitlink source closure mismatch: " + c["id"])
    declared = re.search(r"^go (\S+)", (root/"engine/go.mod").read_text(), re.M).group(1)
    if by_id["Go"]["version"] != "go" + declared: errors.append("declared Go source version mismatch")
    workflows = "\n".join((root/p).read_text() for p in manifest["inputs"] if p.startswith(".github/workflows/"))
    for ident, command in [("Mage","github.com/magefile/mage"),("nfpm","github.com/goreleaser/nfpm/v2/cmd/nfpm"),("actionlint","github.com/rhysd/actionlint/cmd/actionlint")]:
        versions = set(re.findall(re.escape(command) + r"@(v[^\s]+)", workflows))
        if versions != {by_id[ident]["version"]}: errors.append("pinned tool source version mismatch: " + ident)
    packages = json.loads((root/"site/package-lock.json").read_text())["packages"]
    expected = {"node_modules/@playwright/test","node_modules/playwright","node_modules/playwright-core"}
    if set(packages)-{""} != expected or any(packages[p]["version"] != by_id["Playwright"]["version"] for p in expected):
        errors.append("site lockfile source closure/version mismatch")
    with tarfile.open(root/by_id["GSAP"]["archives"][0]["path"]) as archive:
        name = next(n for n in archive.getnames() if n.endswith("/dist/gsap.min.js"))
        if archive.extractfile(name).read() != (root/"site/assets/gsap.min.js").read_bytes():
            errors.append("copied GSAP asset differs from pinned original")
    return errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root",type=Path,default=ROOT)
    args = parser.parse_args()
    manifest = json.loads((args.root/"third_party/sources.json").read_text())
    errors = verify_manifest(args.root,manifest)
    if errors:
        print("Third-party source verification failed:\n" + "\n".join(errors)); return 1
    archives = {a["path"]:a for c in manifest["components"] for a in c["archives"]}
    print(f"Third-party source verified: {len(manifest['components'])} components, {len(archives)} archives, {sum(a['bytes'] for a in archives.values())} compressed bytes; no archived code executed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
