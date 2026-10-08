#!/usr/bin/env python3
"""Emit the non-evidence Windows host-readiness blocking report.

This report is intentionally not PlatformEvidence.  It records why MSI assembly
and inspection must stop before producing candidate bytes or promotable evidence.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import tempfile

IPC_PEER_AUTH_GATE = "windows.ipc-v2.peer-auth"
KNOWN_IPC_LIMITATION = (
    "Known limitation: the named-pipe ACL grants BUILTIN\\Users (BU) "
    "GENERIC_READ|GENERIC_WRITE (GRGW), and the service still accepts legacy "
    "CmdQuit without authenticated IPC v2 peer authorization."
)


def blocked_report(version: str | None = None, git_sha: str | None = None, generated_at: str | None = None) -> dict[str, object]:
    report: dict[str, object] = {
        "contract": "wireztna-windows-host-readiness-blocked",
        "schema_version": 1,
        "status": "blocked",
        "promotion_eligible": False,
        "gates": [
            {
                "id": IPC_PEER_AUTH_GATE,
                "status": "failed",
                "blocking": True,
                "detail": KNOWN_IPC_LIMITATION,
            }
        ],
        "limitations": [
            KNOWN_IPC_LIMITATION,
            "No authenticated external readiness input is currently accepted by the MSI builder.",
            "No MSI candidate or PlatformEvidence may be emitted while this gate is failed.",
        ],
    }
    candidate: dict[str, str] = {"os": "windows", "arch": "amd64", "type": "msi"}
    if version is not None:
        candidate["version"] = version
    if git_sha is not None:
        candidate["git_sha"] = git_sha
    report["candidate_context"] = candidate
    if generated_at is not None:
        report["generated_at"] = generated_at
    return report


def sandbox_blocked_report(version: str, git_sha: str, generated_at: str, detail: str) -> dict[str, object]:
    return {
        "contract": "wireztna-windows-msi-inspection-blocked",
        "schema_version": 1,
        "generated_at": generated_at,
        "status": "blocked",
        "promotion_eligible": False,
        "candidate_context": {
            "version": version,
            "git_sha": git_sha,
            "os": "windows",
            "arch": "amd64",
            "type": "msi",
        },
        "gates": [
            {
                "id": "windows.msi.extraction-sandbox",
                "status": "failed",
                "blocking": True,
                "detail": detail,
            }
        ],
        "limitations": [
            "MSI extraction requires an externally configured sandbox runner, policy, and authentication receipt.",
            "The Directory/Component graph and operational tables are validated before any extraction attempt.",
            "No PlatformEvidence is emitted when the sandbox boundary is absent or invalid.",
        ],
    }


def write_report(path: Path, report: dict[str, object]) -> None:
    if path.is_symlink():
        raise ValueError(f"refusing to replace symbolic link: {path}")
    if not path.parent.is_dir():
        raise ValueError(f"report parent is not a directory: {path.parent}")
    encoded = (json.dumps(report, indent=2, sort_keys=True) + "\n").encode("utf-8")
    descriptor, name = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    temporary = Path(name)
    try:
        with os.fdopen(descriptor, "wb") as output:
            output.write(encoded)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version")
    parser.add_argument("--git-sha")
    parser.add_argument("--generated-at")
    parser.add_argument("--report-out", required=True, type=Path)
    args = parser.parse_args()
    write_report(args.report_out, blocked_report(args.version, args.git_sha, args.generated_at))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
