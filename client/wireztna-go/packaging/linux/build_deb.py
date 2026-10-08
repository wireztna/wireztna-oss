#!/usr/bin/env python3
"""Build and atomically publish a canonical build-only WireZTNA DEB candidate."""

from __future__ import annotations

import argparse
import json
import os
import sys
import tempfile
from pathlib import Path
from typing import Sequence

from deb_archive import (
    BrandAssets,
    DebContractError,
    ICON_SPECS,
    MAX_ARTIFACT_SIZE,
    MAX_DATA_TAR_SIZE,
    MAX_PREBUILT_SIZE,
    PACKAGE_NAME,
    PublishedFile,
    atomic_publish_files,
    make_control_tar_gz,
    make_data_tar_gz,
    prepare_output_directory,
    read_snapshot_bytes,
    require_git_sha,
    require_release,
    require_source_date_epoch,
    require_version,
    sha256_bytes,
    sha256_file,
    snapshot_regular_file,
    validate_brand_assets,
    validate_elf,
    write_deb,
    write_exclusive,
)
from verify_deb import (
    MAX_BUILD_LOG_SIZE,
    encode_build_log,
    expected_artifact_filename,
    expected_build_log,
    expected_build_log_filename,
    verify,
)

MAX_AUXILIARY_SIZE = 1024 * 1024
PACKAGING_DIR = Path(__file__).resolve().parent
DESKTOP_ENTRY_SOURCE = PACKAGING_DIR / "wireztna.desktop"
TAURI_ICON_DIR = PACKAGING_DIR.parents[2] / "wireztna-desktop" / "src-tauri" / "icons"
ICON_SOURCE_NAMES = ("32x32.png", "128x128.png", "128x128@2x.png")


def _snapshot_brand_assets(temporary: str) -> BrandAssets:
    staged_entry = os.path.join(temporary, "wireztna.desktop")
    snapshot_regular_file(
        str(DESKTOP_ENTRY_SOURCE),
        staged_entry,
        "Linux desktop entry",
        max_size=MAX_AUXILIARY_SIZE,
        destination_mode=0o644,
    )
    staged_icons: list[str] = []
    for source_name, (_path, size, _sha256) in zip(ICON_SOURCE_NAMES, ICON_SPECS):
        staged_icon = os.path.join(temporary, f"wireztna-{size}.png")
        snapshot_regular_file(
            str(TAURI_ICON_DIR / source_name),
            staged_icon,
            f"WireZTNA {size}x{size} app icon",
            max_size=MAX_AUXILIARY_SIZE,
            destination_mode=0o644,
        )
        staged_icons.append(staged_icon)
    brand = BrandAssets(
        desktop_entry=read_snapshot_bytes(staged_entry, MAX_AUXILIARY_SIZE),
        icons=tuple(
            read_snapshot_bytes(path, MAX_AUXILIARY_SIZE) for path in staged_icons
        ),
    )
    validate_brand_assets(brand)
    return brand


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=(
            "Assemble a reproducible DL-5.1a/E0 build-only DEB from exactly one "
            "prebuilt Linux desktop ELF. SOURCE_DATE_EPOCH is required."
        )
    )
    parser.add_argument("--arch", required=True, choices=("amd64", "arm64"))
    parser.add_argument("--version", required=True)
    parser.add_argument("--release", default="1")
    parser.add_argument("--git-sha", required=True)
    parser.add_argument("--producer-git-sha", required=True)
    parser.add_argument("--desktop-binary", required=True)
    parser.add_argument(
        "--output-dir",
        default=str(Path(__file__).resolve().parent / "dist"),
    )
    return parser


def _reject_duplicate_options(parser: argparse.ArgumentParser, arguments: Sequence[str]) -> None:
    for option in (
        "--arch",
        "--version",
        "--release",
        "--git-sha",
        "--producer-git-sha",
        "--desktop-binary",
        "--output-dir",
    ):
        count = sum(
            argument == option or argument.startswith(option + "=")
            for argument in arguments
        )
        if count > 1:
            parser.error(f"argument {option} may be supplied only once")


def build(arguments: Sequence[str]) -> tuple[str, str, str, str]:
    parser = _parser()
    _reject_duplicate_options(parser, arguments)
    options = parser.parse_args(arguments)

    epoch = require_source_date_epoch(os.environ.get("SOURCE_DATE_EPOCH"))
    version = require_version(options.version)
    release = require_release(options.release)
    git_sha = require_git_sha(options.git_sha)
    producer_git_sha = require_git_sha(options.producer_git_sha, "producer git SHA")
    if not os.path.isabs(options.desktop_binary):
        raise DebContractError("desktop prebuilt path must be absolute")
    desktop_binary = os.path.abspath(options.desktop_binary)

    output_dir = prepare_output_directory(options.output_dir)
    artifact_name = expected_artifact_filename(version, options.arch)
    stem = artifact_name[:-4]
    log_name = expected_build_log_filename(version, options.arch)
    digest_name = stem + ".sha256"
    marker_name = stem + ".e0.json"

    with tempfile.TemporaryDirectory(prefix="wireztna-deb-build-") as temporary:
        staged_binary = os.path.join(temporary, "wireztna-desktop")
        snapshot_regular_file(
            desktop_binary,
            staged_binary,
            "desktop prebuilt",
            executable=True,
            max_size=MAX_PREBUILT_SIZE,
            destination_mode=0o755,
        )
        payload = read_snapshot_bytes(staged_binary, MAX_PREBUILT_SIZE)
        validate_elf(payload, options.arch)
        payload_sha256 = sha256_bytes(payload)
        brand = _snapshot_brand_assets(temporary)

        staged_artifact = os.path.join(temporary, artifact_name)
        staged_log = os.path.join(temporary, log_name)
        staged_digest = os.path.join(temporary, digest_name)
        staged_evidence = os.path.join(temporary, marker_name)
        control_tar_gz = make_control_tar_gz(
            version, release, options.arch, payload, brand, epoch
        )
        data_tar_gz = make_data_tar_gz(payload, brand, epoch)
        if len(data_tar_gz) > MAX_DATA_TAR_SIZE:
            raise DebContractError("data.tar.gz exceeds the 128 MiB safety limit")
        write_deb(staged_artifact, control_tar_gz, data_tar_gz, epoch)
        if os.stat(staged_artifact, follow_symlinks=False).st_size > MAX_ARTIFACT_SIZE:
            raise DebContractError("DEB exceeds the 128 MiB artifact safety limit")
        artifact_sha256 = sha256_file(staged_artifact)
        write_exclusive(
            staged_log,
            encode_build_log(
                expected_build_log(
                    arch=options.arch,
                    version=version,
                    release=release,
                    git_sha=git_sha,
                    producer_git_sha=producer_git_sha,
                    epoch=epoch,
                    payload_sha256=payload_sha256,
                    artifact_filename=artifact_name,
                    artifact_sha256=artifact_sha256,
                )
            ),
        )

        # The integrated verifier snapshots both files, reparses the complete DEB,
        # and parses/compares every build-log field before evidence is materialized.
        verified, evidence = verify(
            artifact=staged_artifact,
            expected_arch=options.arch,
            expected_version=version,
            expected_release=release,
            expected_package=PACKAGE_NAME,
            expected_payload_sha256=payload_sha256,
            git_sha=git_sha,
            producer_git_sha=producer_git_sha,
            build_log=staged_log,
            source_date_epoch=epoch,
        )
        digest_content = f"{verified.artifact_sha256}  {artifact_name}\n".encode("ascii")
        evidence_content = (
            json.dumps(evidence, ensure_ascii=True, indent=2, sort_keys=True) + "\n"
        ).encode("utf-8")
        write_exclusive(staged_digest, digest_content)
        write_exclusive(staged_evidence, evidence_content)
        reparsed = json.loads(
            read_snapshot_bytes(staged_evidence, MAX_AUXILIARY_SIZE).decode("utf-8")
        )
        if reparsed != evidence:
            raise DebContractError("staged PlatformEvidence changed before publication")

        # All four sources are copied into stable destination-filesystem snapshots.
        # The PlatformEvidence marker is linked last and commits the complete set.
        atomic_publish_files(
            output_dir,
            (
                PublishedFile(
                    staged_artifact,
                    artifact_name,
                    max_size=MAX_ARTIFACT_SIZE,
                ),
                PublishedFile(staged_log, log_name, max_size=MAX_BUILD_LOG_SIZE),
                PublishedFile(staged_digest, digest_name, max_size=MAX_AUXILIARY_SIZE),
                PublishedFile(staged_evidence, marker_name, max_size=MAX_AUXILIARY_SIZE),
            ),
            marker_filename=marker_name,
        )

    return (
        os.path.join(output_dir, artifact_name),
        os.path.join(output_dir, log_name),
        os.path.join(output_dir, digest_name),
        os.path.join(output_dir, marker_name),
    )


def main(arguments: Sequence[str] | None = None) -> int:
    raw_arguments = list(sys.argv[1:] if arguments is None else arguments)
    try:
        artifact, build_log, digest, evidence = build(raw_arguments)
    except (DebContractError, OSError, ValueError, json.JSONDecodeError) as error:
        print(f"build-deb.py: {error}", file=sys.stderr)
        return 1
    print(f"Built (build-only): {artifact}")
    print(f"Evidence commit marker: {evidence}")
    print(f"Digest: {digest}")
    print(f"Build log: {build_log}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
