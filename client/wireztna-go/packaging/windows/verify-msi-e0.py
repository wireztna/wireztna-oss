#!/usr/bin/env python3
"""Bounded source-contract inspector for the canonical WireZTNA MSI.

The legacy filename does not name a release stage. Successful output is always
build-only PlatformEvidence bound to exact candidate bytes and is not eligible
for promotion. The current IPC v2 peer-auth failure stops this program before
artifact snapshot, MSI extraction, or evidence emission.
"""

from __future__ import annotations

import argparse
import csv
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time
from typing import Iterable

try:
    import resource
except ImportError:  # pragma: no cover - this inspector currently runs with msitools on Unix.
    resource = None

from windows_readiness import blocked_report, sandbox_blocked_report, write_report

MAX_MSI_BYTES = 1 << 30
MAX_EXPORT_BYTES = 4 << 20
MAX_EXTRACTED_BYTES = 512 << 20
MAX_EXTRACTED_ENTRIES = 64
MAX_TABLES = 128
MAX_ROWS_PER_TABLE = 2048
MAX_CELL_BYTES = 4096
MAX_BOUNDARY_FILE_BYTES = 1 << 20
COMMAND_TIMEOUT_SECONDS = 30
UPGRADE_CODE = "D8E9F0A1-B2C3-4D5E-6F7A-8B9C0D1E2F3A"
EXPECTED_FILES = {
    "wireztna.exe",
    "wireztna-desktop.exe",
    "wireztna-auth.exe",
    "wireztna.ico",
    "wireguard.dll",
}
REQUIRED_TABLES = {
    "Component",
    "Directory",
    "Environment",
    "Feature",
    "FeatureComponents",
    "File",
    "Icon",
    "InstallExecuteSequence",
    "LaunchCondition",
    "Media",
    "Property",
    "Registry",
    "RemoveFile",
    "ServiceControl",
    "ServiceInstall",
    "Shortcut",
    "Upgrade",
}
FORBIDDEN_TABLES = {"CustomAction", "LockPermissions", "MsiLockPermissionsEx"}
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
VERSION_RE = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
GUID_RE = re.compile(r"^\{?[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\}?$")
LIMITATIONS = [
    "Build-only source-contract inspection; install, upgrade, repair, and uninstall were not executed.",
    "SCM behavior, service runtime, PATH propagation, shortcuts, and autostart were not runtime-qualified.",
    "Authenticode signing and SmartScreen reputation were not asserted.",
    "Absence of MSI permission/custom-action tables does not assert that runtime ACLs are narrow.",
    "This evidence does not authorize promotion or publication.",
]


class VerificationError(Exception):
    pass


def fail(message: str) -> None:
    raise VerificationError(message)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("artifact", type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--git-sha", required=True)
    parser.add_argument("--producer-git-sha", required=True)
    parser.add_argument("--generated-at", required=True, help="Explicit UTC RFC3339 timestamp")
    parser.add_argument("--msiinfo", default="msiinfo", help="msiinfo executable (default: PATH lookup)")
    parser.add_argument("--msiextract", default="msiextract", help="msiextract executable (default: PATH lookup)")
    parser.add_argument("--sandbox-runner", type=Path, help="Externally provisioned extraction boundary")
    parser.add_argument("--sandbox-policy", type=Path, help="Authenticated sandbox policy")
    parser.add_argument("--sandbox-auth-receipt", type=Path, help="Authentication receipt consumed by the sandbox runner")
    parser.add_argument("--report-out", type=Path)
    parser.add_argument("--evidence-out", type=Path, help="Evidence path, or '-' for stdout (default)")
    return parser.parse_args()


def canonical_timestamp(value: str) -> str:
    if not value.endswith("Z"):
        fail("--generated-at must use an explicit UTC Z suffix")
    try:
        parsed = dt.datetime.fromisoformat(value[:-1] + "+00:00")
    except ValueError as error:
        fail(f"invalid --generated-at: {error}")
    if parsed.utcoffset() != dt.timedelta(0):
        fail("--generated-at must be UTC")
    return parsed.isoformat(timespec="seconds").replace("+00:00", "Z")


def ensure_distinct_paths(artifact: Path, outputs: list[Path]) -> None:
    paths = [("artifact", artifact)] + [("output", path) for path in outputs]
    for index, (left_kind, left) in enumerate(paths):
        for right_kind, right in paths[index + 1 :]:
            if left.resolve(strict=False) == right.resolve(strict=False):
                fail(f"{left_kind} and {right_kind} paths must be distinct")
            if left.exists() and right.exists() and os.path.samefile(left, right):
                fail(f"{left_kind} and {right_kind} paths must not alias the same file")


def snapshot_artifact(source: Path, destination: Path) -> tuple[int, str]:
    flags = os.O_RDONLY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    try:
        descriptor = os.open(source, flags)
    except OSError as error:
        fail(f"cannot open artifact: {error}")

    digest = hashlib.sha256()
    observed = 0
    with os.fdopen(descriptor, "rb") as artifact, destination.open("xb") as snapshot:
        before = os.fstat(artifact.fileno())
        if not stat.S_ISREG(before.st_mode):
            fail("artifact must be a regular non-symlink file")
        if before.st_size <= 0 or before.st_size > MAX_MSI_BYTES:
            fail(f"artifact size must be between 1 and {MAX_MSI_BYTES} bytes")
        while chunk := artifact.read(1 << 20):
            observed += len(chunk)
            if observed > MAX_MSI_BYTES:
                fail("artifact grew beyond the size bound while snapshotting")
            snapshot.write(chunk)
            digest.update(chunk)
        after = os.fstat(artifact.fileno())
    before_identity = (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_ctime_ns)
    after_identity = (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns)
    if observed != before.st_size or after_identity != before_identity:
        fail("artifact changed while creating the inspection snapshot")
    destination.chmod(0o400)
    return observed, digest.hexdigest()


def bounded_extracted_files(root: Path) -> list[tuple[Path, int]]:
    pending = [root]
    entries = 0
    total = 0
    files: list[tuple[Path, int]] = []
    while pending:
        directory = pending.pop()
        try:
            children = list(os.scandir(directory))
        except OSError as error:
            fail(f"cannot inspect extracted payload: {error}")
        entries += len(children)
        if entries > MAX_EXTRACTED_ENTRIES:
            fail(f"extracted payload exceeds {MAX_EXTRACTED_ENTRIES} entries")
        for child in children:
            if child.is_symlink():
                fail("extracted payload must not contain symbolic links")
            if child.is_dir(follow_symlinks=False):
                pending.append(Path(child.path))
                continue
            if not child.is_file(follow_symlinks=False):
                fail("extracted payload contains a non-regular entry")
            size = child.stat(follow_symlinks=False).st_size
            total += size
            if total > MAX_EXTRACTED_BYTES:
                fail(f"extracted payload exceeds {MAX_EXTRACTED_BYTES} bytes")
            files.append((Path(child.path), size))
    return files


def apply_resource_limits() -> None:
    if resource is None:
        return
    limits = (
        (resource.RLIMIT_CORE, 0),
        (resource.RLIMIT_CPU, COMMAND_TIMEOUT_SECONDS),
        (resource.RLIMIT_FSIZE, MAX_EXTRACTED_BYTES),
        (resource.RLIMIT_NOFILE, 64),
    )
    for limit, value in limits:
        resource.setrlimit(limit, (value, value))
    if hasattr(resource, "RLIMIT_AS"):
        resource.setrlimit(resource.RLIMIT_AS, (MAX_MSI_BYTES + MAX_EXTRACTED_BYTES, MAX_MSI_BYTES + MAX_EXTRACTED_BYTES))
    if hasattr(resource, "RLIMIT_NPROC"):
        resource.setrlimit(resource.RLIMIT_NPROC, (32, 32))


def terminate_group(process: subprocess.Popen[bytes]) -> None:
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait()


def run_bounded(command: list[str], working_directory: Path, monitored_directory: Path | None = None) -> bytes:
    environment = {
        "LC_ALL": "C",
        "LANG": "C",
        "PATH": "/usr/bin:/bin",
        "HOME": str(working_directory),
        "TMPDIR": str(working_directory),
    }
    with tempfile.TemporaryFile() as output:
        process = subprocess.Popen(
            command,
            stdin=subprocess.DEVNULL,
            stdout=output,
            stderr=output,
            env=environment,
            cwd=working_directory,
            close_fds=True,
            start_new_session=True,
            preexec_fn=apply_resource_limits,
        )
        deadline = time.monotonic() + COMMAND_TIMEOUT_SECONDS
        while process.poll() is None:
            if time.monotonic() >= deadline:
                terminate_group(process)
                fail(f"command timed out: {command[0]}")
            if output.tell() > MAX_EXPORT_BYTES:
                terminate_group(process)
                fail(f"command output exceeded {MAX_EXPORT_BYTES} bytes: {command[0]}")
            if monitored_directory is not None:
                try:
                    bounded_extracted_files(monitored_directory)
                except VerificationError:
                    terminate_group(process)
                    raise
            time.sleep(0.01)
        if output.tell() > MAX_EXPORT_BYTES:
            fail(f"command output exceeded {MAX_EXPORT_BYTES} bytes: {command[0]}")
        output.seek(0)
        data = output.read(MAX_EXPORT_BYTES + 1)
    if process.returncode != 0:
        detail = data.decode("utf-8", "replace").strip()
        fail(f"command failed ({process.returncode}): {command[0]}: {detail[:512]}")
    return data


def parse_idt(table: str, data: bytes) -> list[dict[str, str]]:
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError as error:
        fail(f"{table} export is not UTF-8: {error}")
    rows = list(csv.reader(text.splitlines(), delimiter="\t"))
    if len(rows) < 3 or not rows[0]:
        fail(f"{table} export has no IDT header")
    headers = rows[0]
    if len(set(headers)) != len(headers):
        fail(f"{table} export has duplicate columns")
    parsed: list[dict[str, str]] = []
    for index, row in enumerate(rows[3:], start=4):
        if not row or (len(row) == 1 and row[0] == ""):
            continue
        if len(parsed) >= MAX_ROWS_PER_TABLE:
            fail(f"{table} contains more than {MAX_ROWS_PER_TABLE} rows")
        if len(row) != len(headers):
            fail(f"{table} row {index} has {len(row)} cells; expected {len(headers)}")
        if any(len(cell.encode("utf-8")) > MAX_CELL_BYTES for cell in row):
            fail(f"{table} row {index} contains an oversized cell")
        parsed.append(dict(zip(headers, row)))
    return parsed


def export_table(msiinfo: str, artifact: Path, table: str, working_directory: Path) -> list[dict[str, str]]:
    return parse_idt(table, run_bounded([msiinfo, "export", str(artifact), table], working_directory))


def exactly_one(rows: Iterable[dict[str, str]], description: str) -> dict[str, str]:
    matches = list(rows)
    if len(matches) != 1:
        fail(f"expected exactly one {description}; found {len(matches)}")
    return matches[0]


def require_count(tables: dict[str, list[dict[str, str]]], table: str, expected: int) -> None:
    observed = len(tables[table])
    if observed != expected:
        fail(f"{table} must contain exactly {expected} rows; found {observed}")


def unique_index(rows: list[dict[str, str]], key: str, table: str) -> dict[str, dict[str, str]]:
    indexed: dict[str, dict[str, str]] = {}
    for row in rows:
        value = row.get(key, "")
        if not value:
            fail(f"{table}.{key} must not be empty")
        if value in indexed:
            fail(f"{table}.{key} is duplicated: {value}")
        indexed[value] = row
    return indexed


def integer(row: dict[str, str], column: str, description: str) -> int:
    try:
        return int(row.get(column, ""))
    except ValueError:
        fail(f"{description}.{column} is not an integer")


def long_filename(value: str) -> str:
    return value.split("|", 1)[-1]


def canonical_guid(value: str) -> str:
    return value.strip("{}").upper()


def verify_extracted_payload(root: Path, file_rows: list[dict[str, str]]) -> None:
    extracted = bounded_extracted_files(root)
    by_name: dict[str, int] = {}
    for path, size in extracted:
        if path.name in by_name:
            fail(f"extracted payload contains duplicate basename: {path.name}")
        by_name[path.name] = size
    if set(by_name) != EXPECTED_FILES:
        fail(f"embedded cabinet payload mismatch: expected {sorted(EXPECTED_FILES)}, got {sorted(by_name)}")
    table_sizes = {long_filename(row["FileName"]): integer(row, "FileSize", "File") for row in file_rows}
    if by_name != table_sizes:
        fail("extracted payload sizes do not match the MSI File table")


def verify_tables(tables: dict[str, list[dict[str, str]]], version: str) -> dict[str, str]:
    exact_cardinalities = {
        "Component": 8,
        "Directory": 5,
        "Environment": 1,
        "Feature": 1,
        "FeatureComponents": 8,
        "File": 5,
        "Icon": 1,
        "LaunchCondition": 1,
        "Media": 1,
        "Registry": 3,
        "RemoveFile": 1,
        "ServiceControl": 1,
        "ServiceInstall": 1,
        "Shortcut": 1,
        "Upgrade": 2,
    }
    for table, count in exact_cardinalities.items():
        require_count(tables, table, count)

    properties = unique_index(tables["Property"], "Property", "Property")
    if properties.get("ProductName", {}).get("Value") != "WireZTNA":
        fail("Property.ProductName must be WireZTNA")
    if properties.get("ProductVersion", {}).get("Value") != version:
        fail("Property.ProductVersion does not match candidate version")
    if canonical_guid(properties.get("UpgradeCode", {}).get("Value", "")) != UPGRADE_CODE:
        fail("Property.UpgradeCode does not match the canonical product family")
    product_code = properties.get("ProductCode", {}).get("Value", "")
    if not GUID_RE.fullmatch(product_code):
        fail("Property.ProductCode must be a non-empty GUID")

    files_by_id = unique_index(tables["File"], "File", "File")
    files_by_name: dict[str, dict[str, str]] = {}
    for row in files_by_id.values():
        name = long_filename(row.get("FileName", ""))
        if name in files_by_name:
            fail(f"File.FileName is duplicated: {name}")
        files_by_name[name] = row
    if set(files_by_name) != EXPECTED_FILES:
        fail(f"File payload mismatch: expected {sorted(EXPECTED_FILES)}, got {sorted(files_by_name)}")
    expected_file_components = {
        "wireztna.exe": ("WireZTNACLI", "WireZTNAExe"),
        "wireztna-desktop.exe": ("WireZTNADesktop", "WireZTNADesktopExe"),
        "wireztna-auth.exe": ("WireZTNAAuth", "WireZTNAAuthExe"),
        "wireztna.ico": ("AppIcon", "AppIconFile"),
        "wireguard.dll": ("WireGuardNTDLL", "WireGuardNTDll"),
    }
    for name, (component, file_id) in expected_file_components.items():
        row = files_by_name[name]
        if row.get("Component_") != component or row.get("File") != file_id:
            fail(f"File mapping is not canonical for {name}")
        if name.endswith(".exe") and row.get("Version") != version:
            fail(f"{name} PE file version does not match the candidate version")

    expected_directories = {
        "TARGETDIR": ("", "SourceDir"),
        "ProgramFiles64Folder": ("TARGETDIR", "."),
        "INSTALLFOLDER": ("ProgramFiles64Folder", "WireZTNA"),
        "ProgramMenuFolder": ("TARGETDIR", "."),
        "ApplicationProgramsFolder": ("ProgramMenuFolder", "WireZTNA"),
    }
    directories = unique_index(tables["Directory"], "Directory", "Directory")
    if set(directories) != set(expected_directories):
        fail("Directory table does not contain the exact canonical graph")
    for directory, (parent, default_dir) in expected_directories.items():
        row = directories[directory]
        if row.get("Directory_Parent", "") != parent or row.get("DefaultDir", "") != default_dir:
            fail(f"Directory graph mismatch for {directory}")

    expected_components = {
        "WireZTNACLI": ("B1C2D3E4-F5A6-4B7C-8D9E-0F1A2B3C4D5E", "INSTALLFOLDER", "WireZTNAExe"),
        "WireZTNADesktop": ("C2D3E4F5-A6B7-4C8D-9E0F-1A2B3C4D5E6F", "INSTALLFOLDER", "WireZTNADesktopExe"),
        "WireZTNAAuth": ("6A1D9E1F-1A3F-4B57-A242-8EE6AB89D102", "INSTALLFOLDER", "WireZTNAAuthExe"),
        "AppIcon": ("2B3C4D5E-6F7A-4B8C-9D0E-1F2A3B4C5D6E", "INSTALLFOLDER", "AppIconFile"),
        "WireGuardNTDLL": ("7B018908-1D33-4462-9997-80493166A27A", "INSTALLFOLDER", "WireGuardNTDll"),
        "PathEntry": ("F5A6B7C8-D9E0-4F1A-2B3C-4D5E6F7A8B9C", "INSTALLFOLDER", "WireZTNAInstallDirRegistry"),
        "StartMenuShortcut": ("A1B2C3D4-E5F6-4A7B-8C9D-0E1F2A3B4C5D", "ApplicationProgramsFolder", "StartMenuShortcutRegistry"),
        "TrayAutostart": ("E4F5A6B7-C8D9-4E0F-1A2B-3C4D5E6F7A8B", "INSTALLFOLDER", "WireZTNATrayAutostartRegistry"),
    }
    components = unique_index(tables["Component"], "Component", "Component")
    if set(components) != set(expected_components):
        fail("Component table does not contain the exact canonical component set")
    for component, (guid, directory, key_path) in expected_components.items():
        row = components[component]
        if canonical_guid(row.get("ComponentId", "")) != guid or row.get("Directory_") != directory:
            fail(f"Component identity/directory mismatch for {component}")
        if integer(row, "Attributes", f"Component[{component}]") != 256:
            fail(f"Component[{component}] must be exactly 64-bit")
        if row.get("Condition", "") or row.get("KeyPath") != key_path:
            fail(f"Component condition/key path mismatch for {component}")

    feature = tables["Feature"][0]
    if feature.get("Feature") != "MainFeature" or feature.get("Feature_Parent", "") or integer(feature, "Level", "Feature") != 1:
        fail("Feature table must contain only canonical MainFeature")
    feature_components = unique_index(tables["FeatureComponents"], "Component_", "FeatureComponents")
    if set(feature_components) != set(expected_components):
        fail("FeatureComponents does not contain the exact canonical component set")
    if any(row.get("Feature_") != "MainFeature" for row in feature_components.values()):
        fail("every FeatureComponents row must reference MainFeature")

    upgrades = tables["Upgrade"]
    if any(canonical_guid(row.get("UpgradeCode", "")) != UPGRADE_CODE for row in upgrades):
        fail("every Upgrade row must use the canonical UpgradeCode")
    upgrades_by_property = unique_index(upgrades, "ActionProperty", "Upgrade")
    if set(upgrades_by_property) != {"PREVIOUSFOUND", "SAMEORNEWERFOUND"}:
        fail("Upgrade must contain exactly PREVIOUSFOUND and SAMEORNEWERFOUND")
    previous = upgrades_by_property["PREVIOUSFOUND"]
    if previous.get("VersionMin") != "0.0.0" or previous.get("VersionMax") != version or integer(previous, "Attributes", "Upgrade[PREVIOUSFOUND]") != 256:
        fail("PREVIOUSFOUND must include 0.0.0 and exclude the candidate version")
    same_or_newer = upgrades_by_property["SAMEORNEWERFOUND"]
    if same_or_newer.get("VersionMin") != version or same_or_newer.get("VersionMax", "") or integer(same_or_newer, "Attributes", "Upgrade[SAMEORNEWERFOUND]") != 258:
        fail("SAMEORNEWERFOUND must detect the candidate version and all newer versions")
    launch = tables["LaunchCondition"][0]
    if launch.get("Condition") != "Installed OR NOT SAMEORNEWERFOUND" or launch.get("Description") != "A same or newer version of WireZTNA is already installed.":
        fail("LaunchCondition must block same-version replacement and downgrade")
    execute_sequence = unique_index(tables["InstallExecuteSequence"], "Action", "InstallExecuteSequence")
    remove = execute_sequence.get("RemoveExistingProducts")
    initialize = execute_sequence.get("InstallInitialize")
    if remove is None or initialize is None:
        fail("InstallExecuteSequence is missing canonical upgrade actions")
    if remove.get("Condition") != "PREVIOUSFOUND" or integer(remove, "Sequence", "RemoveExistingProducts") != integer(initialize, "Sequence", "InstallInitialize") + 1:
        fail("RemoveExistingProducts must be conditioned on PREVIOUSFOUND immediately after InstallInitialize")

    service = tables["ServiceInstall"][0]
    if service.get("Name") != "WireZTNA" or service.get("Component_") != "WireZTNACLI":
        fail("ServiceInstall must bind WireZTNA to WireZTNACLI")
    if integer(service, "ServiceType", "ServiceInstall") & 16 == 0 or integer(service, "StartType", "ServiceInstall") != 2:
        fail("ServiceInstall must define an automatic own-process service")
    if integer(service, "ErrorControl", "ServiceInstall") & 3 != 1 or service.get("StartName") not in ("", "LocalSystem") or service.get("Arguments") != "service":
        fail("ServiceInstall account, error control, or arguments are not canonical")

    control = tables["ServiceControl"][0]
    if control.get("Name") != "WireZTNA" or control.get("Component_") != "WireZTNACLI" or integer(control, "Event", "ServiceControl") != 163 or integer(control, "Wait", "ServiceControl") != 1:
        fail("ServiceControl row is not canonical")

    environment = tables["Environment"][0]
    if environment.get("Environment") != "PATH" or not environment.get("Name", "").startswith("*") or "PATH" not in environment.get("Name", ""):
        fail("Environment PATH must be system-scoped")
    if environment.get("Value") != "[INSTALLFOLDER]" or environment.get("Component_") != "PathEntry":
        fail("Environment PATH must bind INSTALLFOLDER to PathEntry")

    shortcut = tables["Shortcut"][0]
    if shortcut.get("Directory_") != "ApplicationProgramsFolder" or shortcut.get("Target") != "[INSTALLFOLDER]wireztna-desktop.exe" or shortcut.get("WkDir") != "INSTALLFOLDER":
        fail("Shortcut target/directory is not canonical")
    if shortcut.get("Component_") != "StartMenuShortcut" or shortcut.get("Icon_") != "WireZTNAIcon":
        fail("Shortcut component/icon mapping is not canonical")

    remove_file = tables["RemoveFile"][0]
    if remove_file.get("FileKey") != "CleanupStartMenu" or remove_file.get("Component_") != "StartMenuShortcut":
        fail("RemoveFile must bind CleanupStartMenu to StartMenuShortcut")
    if remove_file.get("FileName", "") or remove_file.get("DirProperty") != "ApplicationProgramsFolder" or integer(remove_file, "InstallMode", "RemoveFile") != 2:
        fail("RemoveFile must remove only the application Start Menu folder on uninstall")

    registry = unique_index(tables["Registry"], "Registry", "Registry")
    if set(registry) != {"WireZTNAInstallDirRegistry", "StartMenuShortcutRegistry", "WireZTNATrayAutostartRegistry"}:
        fail("Registry table does not contain the exact canonical row identities")
    install_dir = registry["WireZTNAInstallDirRegistry"]
    if install_dir.get("Root") != "2" or install_dir.get("Key") != r"Software\WireZTNA" or install_dir.get("Name") != "InstallDir" or install_dir.get("Value") != "[INSTALLFOLDER]" or install_dir.get("Component_") != "PathEntry":
        fail("HKLM InstallDir registry row is not canonical")
    start_menu = registry["StartMenuShortcutRegistry"]
    if start_menu.get("Root") != "2" or start_menu.get("Key") != r"Software\WireZTNA" or start_menu.get("Name") != "StartMenuShortcut" or start_menu.get("Component_") != "StartMenuShortcut":
        fail("HKLM StartMenuShortcut registry row is not canonical")
    autostart = registry["WireZTNATrayAutostartRegistry"]
    if autostart.get("Root") != "2" or autostart.get("Key") != r"Software\Microsoft\Windows\CurrentVersion\Run" or autostart.get("Name") != "WireZTNA" or autostart.get("Value") != '"[INSTALLFOLDER]wireztna-desktop.exe"' or autostart.get("Component_") != "TrayAutostart":
        fail("HKLM Run autostart registry row is not canonical")

    media = tables["Media"][0]
    if media.get("Cabinet") != "#wireztna.cab":
        fail("Media cabinet must be embedded as wireztna.cab")
    icon = tables["Icon"][0]
    if icon.get("Name") != "WireZTNAIcon":
        fail("Icon table must contain only WireZTNAIcon")
    return {"product_code": product_code, "upgrade_code": UPGRADE_CODE}


def secure_boundary_file(path: Path, description: str, executable: bool = False) -> Path:
    if not path.is_absolute():
        fail(f"{description} must be an absolute externally configured path")
    try:
        info = path.lstat()
    except OSError as error:
        fail(f"cannot inspect {description}: {error}")
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        fail(f"{description} must be a regular non-symlink file")
    if info.st_size <= 0 or info.st_size > MAX_BOUNDARY_FILE_BYTES:
        fail(f"{description} exceeds its bounded size")
    if info.st_mode & 0o022:
        fail(f"{description} must not be group/world writable")
    if executable and not os.access(path, os.X_OK):
        fail(f"{description} must be executable")
    return path.resolve(strict=True)


def sandbox_command(args: argparse.Namespace, msiextract: str, snapshot: Path, destination: Path) -> list[str]:
    if args.sandbox_runner is None or args.sandbox_policy is None or args.sandbox_auth_receipt is None:
        fail("authenticated external sandbox runner, policy, and receipt are required before MSI extraction")
    runner = secure_boundary_file(args.sandbox_runner, "sandbox runner", executable=True)
    policy = secure_boundary_file(args.sandbox_policy, "sandbox policy")
    receipt = secure_boundary_file(args.sandbox_auth_receipt, "sandbox authentication receipt")
    return [
        str(runner),
        "extract-msi",
        "--policy",
        str(policy),
        "--authentication-receipt",
        str(receipt),
        "--max-seconds",
        str(COMMAND_TIMEOUT_SECONDS),
        "--max-bytes",
        str(MAX_EXTRACTED_BYTES),
        "--max-entries",
        str(MAX_EXTRACTED_ENTRIES),
        "--",
        msiextract,
        "-C",
        str(destination),
        str(snapshot),
    ]


def write_json(path: Path, document: object) -> None:
    if path.is_symlink():
        fail(f"refusing to replace symbolic link: {path}")
    if not path.parent.is_dir():
        fail(f"output parent is not a directory: {path.parent}")
    encoded = (json.dumps(document, indent=2, sort_keys=True) + "\n").encode("utf-8")
    descriptor, name = tempfile.mkstemp(dir=path.parent, prefix=path.name + ".")
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
    args = parse_args()
    if not VERSION_RE.fullmatch(args.version):
        fail("--version must be canonical x.y.z")
    if not SHA_RE.fullmatch(args.git_sha) or not SHA_RE.fullmatch(args.producer_git_sha):
        fail("Git SHAs must be full 40-character lowercase hexadecimal values")
    generated_at = canonical_timestamp(args.generated_at)
    expected_name = f"wireztna-{args.version}-windows-amd64.msi"
    if args.artifact.name != expected_name:
        fail(f"artifact basename must be {expected_name}")
    report_path = args.report_out or args.artifact.with_name(args.artifact.name + ".host-blocked.json")
    output_paths = [path for path in (report_path, args.evidence_out) if path is not None and str(path) != "-"]
    ensure_distinct_paths(args.artifact, output_paths)

    # This is deliberately a separate host report, not PlatformEvidence. There
    # is no self-asserted boolean override and no evidence output on this path.
    write_report(report_path, blocked_report(args.version, args.git_sha, generated_at))
    if args.evidence_out is not None and str(args.evidence_out) != "-" and (args.evidence_out.exists() or args.evidence_out.is_symlink()):
        fail(f"blocked inspection refuses to leave a pre-existing evidence output: {args.evidence_out}")
    fail(f"windows.ipc-v2.peer-auth is failed and blocking; see {report_path}")

    # Future authenticated peer-readiness integration resumes here. All table
    # validation remains before extraction, and extraction remains sandbox-only.
    msiinfo = shutil.which(args.msiinfo)
    if msiinfo is None:
        fail(f"msiinfo executable not found: {args.msiinfo}")
    msiextract = shutil.which(args.msiextract)
    if msiextract is None:
        fail(f"msiextract executable not found: {args.msiextract}")

    with tempfile.TemporaryDirectory(prefix="wireztna-msi-inspect-") as temporary_directory:
        private_root = Path(temporary_directory)
        snapshot = private_root / expected_name
        size, sha256 = snapshot_artifact(args.artifact, snapshot)
        table_rows = export_table(msiinfo, snapshot, "_Tables", private_root)
        available_tables = {row.get("Name", "") for row in table_rows}
        if len(available_tables) != len(table_rows):
            fail("_Tables contains duplicate or empty table names")
        if len(available_tables) > MAX_TABLES:
            fail(f"MSI contains more than {MAX_TABLES} tables")
        missing = REQUIRED_TABLES - available_tables
        if missing:
            fail(f"MSI is missing required tables: {sorted(missing)}")
        forbidden = FORBIDDEN_TABLES & available_tables
        if forbidden:
            fail(f"MSI contains prohibited permission/custom-action tables: {sorted(forbidden)}")

        tables = {"_Tables": table_rows}
        for table in sorted(REQUIRED_TABLES):
            tables[table] = export_table(msiinfo, snapshot, table, private_root)
        identity = verify_tables(tables, args.version)

        extracted_directory = private_root / "payload"
        try:
            command = sandbox_command(args, msiextract, snapshot, extracted_directory)
        except VerificationError as error:
            write_report(
                report_path,
                sandbox_blocked_report(args.version, args.git_sha, generated_at, str(error)),
            )
            raise
        extracted_directory.mkdir(mode=0o700)
        run_bounded(command, private_root, extracted_directory)
        verify_extracted_payload(extracted_directory, tables["File"])

    artifact = {"filename": expected_name, "size": size, "sha256": sha256}
    candidate = {
        "version": args.version,
        "git_sha": args.git_sha,
        "os": "windows",
        "arch": "amd64",
        "type": "msi",
        "artifact": artifact,
    }
    checks = [
        {"id": "windows.msi.identity", "status": "passed"},
        {"id": "windows.msi.payload", "status": "passed"},
        {"id": "windows.msi.scm", "status": "passed"},
        {"id": "windows.msi.integration-tables", "status": "passed"},
        {"id": "windows.msi.no-permission-or-custom-action-tables", "status": "passed"},
    ]
    report = {
        "contract": "wireztna-windows-msi-source-contract-inspection",
        "schema_version": 1,
        "generated_at": generated_at,
        "candidate": candidate,
        "msi_identity": identity,
        "checks": checks,
        "limitations": LIMITATIONS,
    }
    evidence = {
        "identity": {
            "contract": "wireztna-platform-evidence",
            "schema_version": 1,
            "evidence_id": f"windows.msi.build-only:{sha256}",
            "generated_at": generated_at,
        },
        "source": {
            "producer": "windows-msi-source-contract-inspector",
            "producer_git_sha": args.producer_git_sha,
            "test_fixture": False,
        },
        "tuple": candidate,
        "claim": {
            "level": "build-only",
            "artifact": True,
            "runtime": False,
            "public_eligibility": False,
        },
        "gates": [{"id": "windows.msi.source-contract-inspected", "status": "passed"}],
        "signature": {"status": "not-asserted"},
        "provenance": {"status": "not-asserted"},
        "limitations": LIMITATIONS,
    }

    if args.report_out is not None:
        write_json(args.report_out, report)
    if args.evidence_out is None or str(args.evidence_out) == "-":
        json.dump(evidence, sys.stdout, indent=2, sort_keys=True)
        sys.stdout.write("\n")
    else:
        write_json(args.evidence_out, evidence)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, VerificationError, ValueError) as error:
        print(f"verify-msi: {error}", file=sys.stderr)
        raise SystemExit(1)
