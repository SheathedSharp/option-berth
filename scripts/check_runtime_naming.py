#!/usr/bin/env python3
"""Audit retired product identifiers with explicit, reasoned exceptions.

Does not read credentials, local settings, third-party material or licensing.
The migration table and rejection fixtures document old inputs, not aliases.
"""
import json
from pathlib import Path
import re
import subprocess

RETIRED = re.compile(r"daemon|BERTH_SOCKET|BERTH_NO_AUTOSTART|BERTH_ALLOW_TEST_DAEMON|\bserve\s+--detach|internal/runtime service|oberth serve", re.I)
REJECTION = {
 "engine/internal/paths/runtime_service_env.go", "engine/internal/paths/runtime_service_env_test.go",
 "engine/internal/cmd/runtime_service_rejection.go", "engine/internal/cmd/runtime_service_rejection_test.go",
 "engine/internal/config/runtime_service_names.go", "engine/internal/config/runtime_service_names_test.go",
 "engine/internal/runtimesvc/rpc/runtime_service_names.go", "engine/internal/runtimesvc/rpc/runtime_service_names_test.go",
 "scripts/runtime_naming_test.py", "scripts/check_runtime_naming_cli.py",
}
MIGRATIONS = {
 "engine/internal/store/migrations/002_indexes_roots.sql", "engine/internal/store/migrations/008_run_exits.sql",
 "engine/internal/store/migrations/010_worktree_identities.sql", "engine/internal/sessions/migrations/005_sessions.sql",
 "engine/internal/claims/006_claims.sql",
}
GUARDS = {"engine/internal/runtimesvc/storage_lease.go", "engine/internal/runtimesvc/storage_lease_test.go",
          "engine/internal/runtimesvc/storage_lease_legacy_test.go"}
DOCKER = {"scripts/agent_eval.py", "examples/stack-workshop/README.md", "docs/agent/validation.md", "engine/internal/docker/watch_test.go",
          "engine/internal/doctor/probes.go", "engine/internal/doctor/doctor.go", "engine/internal/doctor/checks_test.go", "engine/internal/doctor/probes_test.go"}
THREADS = {"scripts/check_storage_ownership.py","scripts/example_api_test.py", "scripts/verify-e2e.py", "scripts/install_test.py", "scripts/verify_stats_drain_test.py"}
SUFFIXES = {".go", ".py", ".md", ".sh", ".ps1", ".yml", ".yaml", ".json", ".sql", ".tmpl", ".html"}


def audit(root):
    names = subprocess.check_output(["git", "-C", str(root), "ls-files", "-z", "--", "engine", "scripts", "docs", "site", "examples", ".github", ".agents", "AGENTS.md", "CLAUDE.md", "README.md", "README.zh.md", "install.sh", "uninstall.sh", "install.ps1", "uninstall.ps1"], text=True).split("\0")
    checked, allowed, violations = 0, [], []
    for name in filter(None, names):
        path = Path(name)
        if ".claude" in path.parts or "third_party" in path.parts or path.name == "settings.local.json" or "LICENSE" in path.name.upper() or "THIRD_PARTY" in path.name.upper():
            continue
        if path.suffix not in SUFFIXES:
            continue
        checked += 1
        migration = False
        for number, line in enumerate((root / path).read_text().splitlines(), 1):
            if "<!-- retired-runtime-v2-begin" in line:
                migration = True
            reason = None
            if RETIRED.search(line):
                if name == "scripts/check_runtime_naming.py": reason = "the audit's explicit retired-input rules"
                elif name in REJECTION: reason = "explicit rejection/input-negative contract fixture"
                elif name == "scripts/check_runtime_availability.py" and ('"daemon.lock"' in line or 'c.call("daemon.status"' in line): reason = "real released-Store interlock fixture or explicitly rejected old RPC"
                elif name in GUARDS and '\"daemon.lock\"' in line: reason = "legacy exclusion interlock only; never a socket fallback"
                elif name in ("docs/cli.md", "docs/cli.zh.md") and migration: reason = "bounded manual migration/rejection table"
                elif name in MIGRATIONS and line.lstrip().startswith("--"): reason = "historical immutable SQL migration comment"
                elif name in DOCKER and "docker" in line.lower(): reason = "third-party Docker terminology"
                elif name in ("site/guide/quickstart.en.html", "site/guide/quickstart.zh.html") and "daemon" in line: reason = "unrelated user background processes, not product host identity"
                elif name in THREADS and re.search(r"daemon\s*=\s*True", line): reason = "standard-library thread lifetime flag"
                elif name == "scripts/measure_runtime.py" and ("engine/internal/daemon" in line): reason = "historical baseline import adapter"
                elif name == "engine/internal/testenv/runtime_service_process_test.go" and "false" in line: reason = "old argv explicitly not recognized as a new host"
                item = {"path": name, "line": number, "reason": reason}
                (allowed if reason else violations).append(item)
            if "<!-- retired-runtime-v2-end" in line:
                migration = False
        if RETIRED.search(path.name) and name not in REJECTION:
            violations.append({"path":name,"line":0,"reason":"retired source filename"})
    return {"checked_files":checked,"allowed":allowed,"violations":violations}


if __name__ == "__main__":
    result = audit(Path(__file__).resolve().parents[1])
    print(json.dumps(result, ensure_ascii=False, indent=2))
    raise SystemExit(bool(result["violations"]))
