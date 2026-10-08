#!/usr/bin/env python3
"""Snapshot the five canonical MSI payloads without reopening live inputs."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import sys

PAYLOADS = (
    "wireztna.exe",
    "wireztna-desktop.exe",
    "wireztna-auth.exe",
    "wireztna.ico",
    "wireguard.dll",
)
MAX_PAYLOAD_BYTES = 1 << 30


class SnapshotError(Exception):
    pass


def open_directory(path: Path) -> int:
    flags = os.O_RDONLY
    if hasattr(os, "O_DIRECTORY"):
        flags |= os.O_DIRECTORY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    return os.open(path, flags)


def snapshot(source: Path, destination: Path) -> list[dict[str, object]]:
    source_fd = open_directory(source)
    destination_fd = open_directory(destination)
    records: list[dict[str, object]] = []
    try:
        try:
            os.stat("wintun.dll", dir_fd=source_fd, follow_symlinks=False)
        except FileNotFoundError:
            pass
        else:
            raise SnapshotError("wintun.dll is prohibited by the canonical MSI contract")

        for name in PAYLOADS:
            flags = os.O_RDONLY
            if hasattr(os, "O_NOFOLLOW"):
                flags |= os.O_NOFOLLOW
            try:
                source_file = os.open(name, flags, dir_fd=source_fd)
            except OSError as error:
                raise SnapshotError(f"cannot open canonical payload {name}: {error}") from error
            try:
                before = os.fstat(source_file)
                if not stat.S_ISREG(before.st_mode) or before.st_size <= 0:
                    raise SnapshotError(f"payload must be a non-empty regular file: {name}")
                if before.st_size > MAX_PAYLOAD_BYTES:
                    raise SnapshotError(f"payload exceeds {MAX_PAYLOAD_BYTES} bytes: {name}")
                destination_file = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o400, dir_fd=destination_fd)
                digest = hashlib.sha256()
                observed = 0
                try:
                    while True:
                        chunk = os.read(source_file, 1 << 20)
                        if not chunk:
                            break
                        observed += len(chunk)
                        if observed > MAX_PAYLOAD_BYTES:
                            raise SnapshotError(f"payload grew beyond the size limit: {name}")
                        digest.update(chunk)
                        view = memoryview(chunk)
                        while view:
                            written = os.write(destination_file, view)
                            view = view[written:]
                    os.fsync(destination_file)
                finally:
                    os.close(destination_file)
                after = os.fstat(source_file)
                identity_before = (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_ctime_ns)
                identity_after = (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns)
                if observed != before.st_size or identity_after != identity_before:
                    raise SnapshotError(f"payload changed while snapshotting: {name}")
                records.append({"name": name, "size": observed, "sha256": digest.hexdigest()})
            finally:
                os.close(source_file)
    finally:
        os.close(destination_fd)
        os.close(source_fd)
    return records


def write_manifest(path: Path, records: list[dict[str, object]]) -> None:
    if path.is_symlink() or path.exists():
        raise SnapshotError(f"manifest output must not already exist: {path}")
    encoded = (json.dumps({"schema_version": 1, "payloads": records}, indent=2, sort_keys=True) + "\n").encode("utf-8")
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o400)
    with os.fdopen(descriptor, "wb") as output:
        output.write(encoded)
        output.flush()
        os.fsync(output.fileno())


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("destination", type=Path)
    parser.add_argument("--manifest-out", required=True, type=Path)
    args = parser.parse_args()
    if not args.destination.is_dir():
        raise SnapshotError("destination must be an existing private directory")
    if any(args.destination.iterdir()):
        raise SnapshotError("destination must be empty")
    records = snapshot(args.source, args.destination)
    if tuple(record["name"] for record in records) != PAYLOADS:
        raise SnapshotError("snapshot does not contain the exact canonical five-payload sequence")
    write_manifest(args.manifest_out, records)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, SnapshotError) as error:
        print(f"snapshot-msi-payload: {error}", file=sys.stderr)
        raise SystemExit(1)
