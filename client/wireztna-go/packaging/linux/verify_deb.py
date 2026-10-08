#!/usr/bin/env python3
"""Standalone, build-only PlatformEvidence verifier for canonical WireZTNA DEBs."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import sys
import tempfile
from dataclasses import dataclass
from typing import NoReturn, Sequence

from deb_archive import (
    DebContractError,
    MAX_ARTIFACT_SIZE,
    PACKAGE_NAME,
    PACKAGE_OS,
    PACKAGE_TYPE,
    PAYLOAD_INSTALL_PATH,
    inspect_tar_payload,
    read_canonical_ar,
    read_snapshot_bytes,
    require_git_sha,
    require_release,
    require_source_date_epoch,
    require_version,
    sha256_bytes,
    sha256_file,
    snapshot_regular_file,
    validate_elf,
    write_exclusive,
)

MAX_BUILD_LOG_SIZE = 1024 * 1024
PRODUCER = "linux-deb-source-contract-inspector"
LIMITATIONS = [
    "Build-only source-contract inspection; installation and removal were not executed.",
    "Desktop-session integration, lifecycle hooks, services, sockets, and runtime behavior were not qualified.",
    "Artifact signing and repository trust were not asserted.",
    "This evidence does not authorize promotion or publication.",
]
DIGEST_RE = re.compile(r"[0-9a-f]{64}\Z")
BUILD_LOG_FIELDS = (
    "contract",
    "status",
    "source_date_epoch",
    "os",
    "architecture",
    "version",
    "release",
    "git_sha",
    "producer",
    "producer_git_sha",
    "artifact_filename",
    "desktop_binary_sha256",
    "artifact_sha256",
    "builder",
    "action",
    "static_verification",
)


@dataclass(frozen=True)
class VerifiedDeb:
    artifact_filename: str
    artifact_size: int
    artifact_sha256: str
    package_version: str
    payload_size: int
    payload_sha256: str


def _fail(message: str) -> NoReturn:
    raise DebContractError(message)


def _validate_digest(value: str) -> str:
    if not DIGEST_RE.fullmatch(value):
        _fail("expected payload SHA-256 must contain exactly 64 lowercase hexadecimal characters")
    return value


def expected_artifact_filename(version: str, arch: str) -> str:
    return f"wireztna-{version}-linux-{arch}.deb"


def expected_build_log_filename(version: str, arch: str) -> str:
    return f"wireztna-{version}-linux-{arch}.build.log"


def inspect_deb_snapshot(
    snapshot: str,
    *,
    artifact_filename: str,
    expected_arch: str,
    expected_version: str,
    expected_release: str,
    expected_package: str,
    expected_payload_sha256: str,
    source_date_epoch: int,
) -> VerifiedDeb:
    if expected_package != PACKAGE_NAME:
        _fail(f"E0 package name must be exactly {PACKAGE_NAME}")
    expected_name = expected_artifact_filename(expected_version, expected_arch)
    if artifact_filename != expected_name:
        _fail(f"artifact basename must be {expected_name}")
    members = read_canonical_ar(snapshot, source_date_epoch)
    payload_set = inspect_tar_payload(
        members,
        expected_version,
        expected_release,
        expected_arch,
        source_date_epoch,
    )
    validate_elf(payload_set.payload, expected_arch)
    payload_sha256 = sha256_bytes(payload_set.payload)
    if payload_sha256 != expected_payload_sha256:
        _fail("packaged desktop payload does not match the authoritative prebuilt SHA-256")
    return VerifiedDeb(
        artifact_filename=artifact_filename,
        artifact_size=os.stat(snapshot, follow_symlinks=False).st_size,
        artifact_sha256=sha256_file(snapshot),
        package_version=f"{expected_version}-{expected_release}",
        payload_size=len(payload_set.payload),
        payload_sha256=payload_sha256,
    )


def _timestamp(epoch: int) -> str:
    origin = dt.datetime(1970, 1, 1, tzinfo=dt.timezone.utc)
    return (origin + dt.timedelta(seconds=epoch)).strftime("%Y-%m-%dT%H:%M:%SZ")


def expected_build_log(
    *,
    arch: str,
    version: str,
    release: str,
    git_sha: str,
    producer_git_sha: str,
    epoch: int,
    payload_sha256: str,
    artifact_filename: str,
    artifact_sha256: str,
) -> tuple[tuple[str, str], ...]:
    return (
        ("contract", "DL-5.1a/E0"),
        ("status", "build-only"),
        ("source_date_epoch", str(epoch)),
        ("os", PACKAGE_OS),
        ("architecture", arch),
        ("version", version),
        ("release", release),
        ("git_sha", git_sha),
        ("producer", PRODUCER),
        ("producer_git_sha", producer_git_sha),
        ("artifact_filename", artifact_filename),
        ("desktop_binary_sha256", payload_sha256),
        ("artifact_sha256", artifact_sha256),
        ("builder", "python3-stdlib-canonical-deb-v1"),
        ("action", "assemble-authoritative-prebuilt-snapshot-only"),
        ("static_verification", "required-before-publication"),
    )


def encode_build_log(values: tuple[tuple[str, str], ...]) -> bytes:
    if tuple(name for name, _value in values) != BUILD_LOG_FIELDS:
        _fail("internal build log field order is not canonical")
    return "".join(f"{name}={value}\n" for name, value in values).encode("utf-8")


def verify_build_log(content: bytes, expected: tuple[tuple[str, str], ...]) -> str:
    try:
        text = content.decode("utf-8")
    except UnicodeDecodeError as error:
        _fail(f"build log is not UTF-8: {error}")
    if not text.endswith("\n") or "\r" in text or "\x00" in text:
        _fail("build log is not canonical newline-delimited UTF-8")
    parsed: list[tuple[str, str]] = []
    seen: set[str] = set()
    for line in text[:-1].split("\n"):
        if not line or "=" not in line:
            _fail("build log contains a malformed line")
        name, value = line.split("=", 1)
        if name in seen:
            _fail(f"build log contains duplicate field: {name}")
        seen.add(name)
        parsed.append((name, value))
    if tuple(parsed) != expected:
        _fail("build log does not exactly match the verified candidate")
    return sha256_bytes(content)


def platform_evidence(
    verified: VerifiedDeb,
    *,
    expected_version: str,
    expected_arch: str,
    git_sha: str,
    producer_git_sha: str,
    source_date_epoch: int,
) -> dict[str, object]:
    artifact = {
        "filename": verified.artifact_filename,
        "size": verified.artifact_size,
        "sha256": verified.artifact_sha256,
    }
    candidate = {
        "version": expected_version,
        "git_sha": git_sha,
        "os": PACKAGE_OS,
        "arch": expected_arch,
        "type": PACKAGE_TYPE,
        "artifact": artifact,
    }
    return {
        "identity": {
            "contract": "wireztna-platform-evidence",
            "schema_version": 1,
            "evidence_id": f"linux.deb.build-only:{verified.artifact_sha256}",
            "generated_at": _timestamp(source_date_epoch),
        },
        "source": {
            "producer": PRODUCER,
            "producer_git_sha": producer_git_sha,
            "test_fixture": False,
        },
        "tuple": candidate,
        "claim": {
            "level": "build-only",
            "artifact": True,
            "runtime": False,
            "public_eligibility": False,
        },
        "gates": [
            {"id": "linux.deb.source-contract-inspected", "status": "passed"}
        ],
        "signature": {"status": "not-asserted"},
        "provenance": {"status": "not-asserted"},
        "limitations": LIMITATIONS,
    }


def verify(
    *,
    artifact: str,
    expected_arch: str,
    expected_version: str,
    expected_release: str,
    expected_package: str,
    expected_payload_sha256: str,
    git_sha: str,
    producer_git_sha: str,
    build_log: str,
    source_date_epoch: int,
) -> tuple[VerifiedDeb, dict[str, object]]:
    expected_version = require_version(expected_version)
    expected_release = require_release(expected_release)
    git_sha = require_git_sha(git_sha)
    producer_git_sha = require_git_sha(producer_git_sha, "producer git SHA")
    expected_payload_sha256 = _validate_digest(expected_payload_sha256)
    if expected_arch not in ("amd64", "arm64"):
        _fail(f"unsupported expected architecture: {expected_arch}")

    artifact_name = os.path.basename(artifact)
    expected_name = expected_artifact_filename(expected_version, expected_arch)
    if artifact_name != expected_name:
        _fail(f"artifact basename must be {expected_name}")
    build_log_name = os.path.basename(build_log)
    expected_log_name = expected_build_log_filename(expected_version, expected_arch)
    if build_log_name != expected_log_name:
        _fail(f"build log basename must be {expected_log_name}")

    with tempfile.TemporaryDirectory(prefix="wireztna-deb-verify-") as temporary:
        artifact_snapshot = os.path.join(temporary, expected_name)
        log_snapshot = os.path.join(temporary, expected_log_name)
        snapshot_regular_file(
            artifact,
            artifact_snapshot,
            "DEB artifact",
            max_size=MAX_ARTIFACT_SIZE,
        )
        snapshot_regular_file(
            build_log,
            log_snapshot,
            "build log",
            max_size=MAX_BUILD_LOG_SIZE,
        )
        verified = inspect_deb_snapshot(
            artifact_snapshot,
            artifact_filename=artifact_name,
            expected_arch=expected_arch,
            expected_version=expected_version,
            expected_release=expected_release,
            expected_package=expected_package,
            expected_payload_sha256=expected_payload_sha256,
            source_date_epoch=source_date_epoch,
        )
        log_content = read_snapshot_bytes(log_snapshot, MAX_BUILD_LOG_SIZE)
        verify_build_log(
            log_content,
            expected_build_log(
                arch=expected_arch,
                version=expected_version,
                release=expected_release,
                git_sha=git_sha,
                producer_git_sha=producer_git_sha,
                epoch=source_date_epoch,
                payload_sha256=verified.payload_sha256,
                artifact_filename=verified.artifact_filename,
                artifact_sha256=verified.artifact_sha256,
            ),
        )
        return verified, platform_evidence(
            verified,
            expected_version=expected_version,
            expected_arch=expected_arch,
            git_sha=git_sha,
            producer_git_sha=producer_git_sha,
            source_date_epoch=source_date_epoch,
        )


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=(
            "Statically verify a canonical WireZTNA E0 DEB and emit only build-only "
            "release.PlatformEvidence v1 JSON. SOURCE_DATE_EPOCH is required."
        )
    )
    parser.add_argument("--artifact", required=True)
    parser.add_argument("--expected-arch", required=True, choices=("amd64", "arm64"))
    parser.add_argument("--expected-version", required=True)
    parser.add_argument("--expected-release", default="1")
    parser.add_argument("--expected-package", required=True)
    parser.add_argument("--expected-payload-sha256", required=True)
    parser.add_argument("--git-sha", required=True)
    parser.add_argument("--producer-git-sha", required=True)
    parser.add_argument("--build-log", required=True)
    parser.add_argument("--report-out")
    return parser


def _reject_duplicate_options(parser: argparse.ArgumentParser, arguments: Sequence[str]) -> None:
    for option in (
        "--artifact",
        "--expected-arch",
        "--expected-version",
        "--expected-release",
        "--expected-package",
        "--expected-payload-sha256",
        "--git-sha",
        "--producer-git-sha",
        "--build-log",
        "--report-out",
    ):
        count = sum(
            argument == option or argument.startswith(option + "=")
            for argument in arguments
        )
        if count > 1:
            parser.error(f"argument {option} may be supplied only once")


def _ensure_distinct_output(report_out: str, artifact: str, build_log: str) -> str:
    output = os.path.abspath(report_out)
    for label, source in (("artifact", artifact), ("build log", build_log)):
        if output == os.path.abspath(source):
            _fail(f"report output must be distinct from the {label}")
        if os.path.exists(output) and os.path.exists(source) and os.path.samefile(output, source):
            _fail(f"report output must not alias the {label}")
    if os.path.lexists(output):
        _fail(f"refusing to overwrite report output: {output}")
    parent = os.path.dirname(output)
    if not os.path.isdir(parent) or os.path.islink(parent):
        _fail("report output parent must be an existing non-symlink directory")
    return output


def main(arguments: Sequence[str] | None = None) -> int:
    raw_arguments = list(sys.argv[1:] if arguments is None else arguments)
    parser = _parser()
    _reject_duplicate_options(parser, raw_arguments)
    options = parser.parse_args(raw_arguments)
    try:
        epoch = require_source_date_epoch(os.environ.get("SOURCE_DATE_EPOCH"))
        _verified, evidence = verify(
            artifact=options.artifact,
            expected_arch=options.expected_arch,
            expected_version=options.expected_version,
            expected_release=options.expected_release,
            expected_package=options.expected_package,
            expected_payload_sha256=options.expected_payload_sha256,
            git_sha=options.git_sha,
            producer_git_sha=options.producer_git_sha,
            build_log=options.build_log,
            source_date_epoch=epoch,
        )
        encoded = (
            json.dumps(evidence, ensure_ascii=True, indent=2, sort_keys=True) + "\n"
        ).encode("utf-8")
        if options.report_out:
            write_exclusive(
                _ensure_distinct_output(
                    options.report_out, options.artifact, options.build_log
                ),
                encoded,
            )
        else:
            sys.stdout.buffer.write(encoded)
    except (DebContractError, OSError, ValueError, json.JSONDecodeError) as error:
        print(f"verify-deb.py: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
