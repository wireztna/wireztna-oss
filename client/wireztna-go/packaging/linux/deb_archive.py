#!/usr/bin/env python3
"""Deterministic, fail-closed primitives for the WireZTNA E0 DEB contract."""

from __future__ import annotations

import binascii
import gzip
import hashlib
import io
import os
import re
import secrets
import stat
import struct
import tarfile
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable, Sequence

AR_MAGIC = b"!<arch>\n"
AR_MEMBERS = ("debian-binary", "control.tar.gz", "data.tar.gz")
AR_MODE = 0o100644
PACKAGE_NAME = "wireztna-desktop"
PACKAGE_OS = "linux"
PACKAGE_TYPE = "deb"
PAYLOAD_PATH = "usr/bin/wireztna-desktop"
PAYLOAD_INSTALL_PATH = "/" + PAYLOAD_PATH
DESKTOP_ENTRY_PATH = "usr/share/applications/wireztna.desktop"
DESKTOP_ENTRY_BYTES = b"""[Desktop Entry]
Type=Application
Name=WireZTNA
Comment=Zero Trust Network Access Client
Exec=/usr/bin/wireztna-desktop
TryExec=/usr/bin/wireztna-desktop
Icon=wireztna
Categories=Network;Security;
StartupNotify=false
Terminal=false
"""
ICON_SPECS = (
    (
        "usr/share/icons/hicolor/32x32/apps/wireztna.png",
        32,
        "3c581b6175842a56e9c68a271922a068a4e64908b0cf29575a0227b24c5e800f",
    ),
    (
        "usr/share/icons/hicolor/128x128/apps/wireztna.png",
        128,
        "d8ff0a1b72a4c0e203f6976f365f8b5a41347db7b44b4ff31a59280a1140db95",
    ),
    (
        "usr/share/icons/hicolor/256x256/apps/wireztna.png",
        256,
        "291155c91f79acbf2214e8e7e865e5e051ca5208077ab20f54535af5ece94930",
    ),
)
SUPPORTED_ARCHES = {"amd64": 62, "arm64": 183}
VERSION_RE = re.compile(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\Z")
GIT_SHA_RE = re.compile(r"[0-9a-f]{40}\Z")
MAX_SOURCE_DATE_EPOCH = (1 << 32) - 1
MAX_PREBUILT_SIZE = 64 * 1024 * 1024
MAX_ARTIFACT_SIZE = 128 * 1024 * 1024
MAX_CONTROL_TAR_SIZE = 1024 * 1024
MAX_DATA_TAR_SIZE = 128 * 1024 * 1024
COPY_CHUNK = 1024 * 1024


class DebContractError(RuntimeError):
    """Raised when an input or archive violates the E0 package contract."""


@dataclass(frozen=True)
class BrandAssets:
    desktop_entry: bytes
    icons: tuple[bytes, bytes, bytes]


@dataclass(frozen=True)
class TarPayload:
    control: bytes
    md5sums: bytes
    payload: bytes
    brand: BrandAssets


@dataclass(frozen=True)
class PublishedFile:
    source: str
    filename: str
    mode: int = 0o644
    max_size: int = MAX_ARTIFACT_SIZE


@dataclass(frozen=True)
class FileIdentity:
    device: int
    inode: int


def require_source_date_epoch(value: str | None) -> int:
    if value is None or not re.fullmatch(r"[0-9]+", value):
        raise DebContractError("SOURCE_DATE_EPOCH must be set to a decimal Unix timestamp")
    epoch = int(value, 10)
    if not 0 <= epoch <= MAX_SOURCE_DATE_EPOCH:
        raise DebContractError(
            f"SOURCE_DATE_EPOCH must be between 0 and {MAX_SOURCE_DATE_EPOCH}"
        )
    return epoch


def require_version(value: str, label: str = "version") -> str:
    if not VERSION_RE.fullmatch(value):
        raise DebContractError(f"{label} must be canonical semver x.y.z")
    return value


def require_release(value: str) -> str:
    if value != "1":
        raise DebContractError("release must be exactly 1")
    return value


def require_git_sha(value: str, label: str = "git SHA") -> str:
    if not GIT_SHA_RE.fullmatch(value):
        raise DebContractError(
            f"{label} must contain exactly 40 lowercase hexadecimal characters"
        )
    return value


def sha256_bytes(content: bytes) -> str:
    return hashlib.sha256(content).hexdigest()


def sha256_file(path: str) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as source:
        for chunk in iter(lambda: source.read(COPY_CHUNK), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _md5_bytes(content: bytes) -> str:
    digest = hashlib.md5(usedforsecurity=False)
    digest.update(content)
    return digest.hexdigest()


def snapshot_regular_file(
    source: str,
    destination: str,
    label: str,
    *,
    executable: bool = False,
    max_size: int = MAX_ARTIFACT_SIZE,
    destination_mode: int = 0o600,
) -> int:
    """Copy one opened no-follow regular-file snapshot into an exclusive file."""
    if not hasattr(os, "O_NOFOLLOW"):
        raise DebContractError("this platform lacks required no-follow file opens")
    flags = os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK
    try:
        source_fd = os.open(source, flags)
    except OSError as error:
        raise DebContractError(
            f"could not open {label} without following symlinks: {error}"
        ) from error

    destination_fd: int | None = None
    try:
        before = os.fstat(source_fd)
        if not stat.S_ISREG(before.st_mode):
            raise DebContractError(f"{label} is not a regular file")
        if before.st_size <= 0:
            raise DebContractError(f"{label} is empty")
        if before.st_size > max_size:
            raise DebContractError(f"{label} exceeds the {max_size}-byte safety limit")
        if executable and before.st_mode & 0o111 == 0:
            raise DebContractError(f"{label} is not executable")

        create_flags = (
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_CLOEXEC | os.O_NOFOLLOW
        )
        destination_fd = os.open(destination, create_flags, destination_mode)
        copied = 0
        while True:
            chunk = os.read(source_fd, COPY_CHUNK)
            if not chunk:
                break
            copied += len(chunk)
            if copied > max_size:
                raise DebContractError(f"{label} changed beyond its safety limit")
            view = memoryview(chunk)
            while view:
                written = os.write(destination_fd, view)
                if written <= 0:
                    raise DebContractError(f"could not snapshot {label}")
                view = view[written:]

        after = os.fstat(source_fd)
        stable_fields = (
            "st_dev",
            "st_ino",
            "st_size",
            "st_mtime_ns",
            "st_ctime_ns",
        )
        if copied != before.st_size or any(
            getattr(before, field) != getattr(after, field) for field in stable_fields
        ):
            raise DebContractError(f"{label} changed while its snapshot was captured")
        os.fchmod(destination_fd, destination_mode)
        os.fsync(destination_fd)
        return copied
    finally:
        if destination_fd is not None:
            os.close(destination_fd)
        os.close(source_fd)


def validate_elf(payload: bytes, expected_arch: str) -> None:
    """Validate an actual 64-bit little-endian Linux executable image."""
    expected_machine = SUPPORTED_ARCHES.get(expected_arch)
    if expected_machine is None:
        raise DebContractError(f"unsupported architecture: {expected_arch}")
    if len(payload) < 64 or payload[:4] != b"\x7fELF":
        raise DebContractError("desktop prebuilt is not an ELF file")
    if payload[4] != 2:
        raise DebContractError("desktop prebuilt is not a 64-bit ELF")
    if payload[5] != 1:
        raise DebContractError("desktop prebuilt is not a little-endian ELF")
    if payload[6] != 1:
        raise DebContractError("desktop prebuilt has an unsupported ELF identifier version")
    if payload[7] not in (0, 3):
        raise DebContractError("desktop prebuilt does not use the Linux/System V ELF ABI")

    try:
        (
            elf_type,
            machine,
            elf_version,
            entry,
            program_offset,
            _section_offset,
            _flags,
            header_size,
            program_entry_size,
            program_count,
            _section_entry_size,
            _section_count,
            _section_names,
        ) = struct.unpack_from("<HHIQQQIHHHHHH", payload, 16)
    except struct.error as error:
        raise DebContractError("desktop prebuilt has a truncated ELF header") from error

    if elf_type not in (2, 3):
        raise DebContractError("desktop prebuilt is neither ET_EXEC nor position-independent ET_DYN")
    if machine != expected_machine:
        raise DebContractError(
            f"desktop prebuilt ELF machine {machine} does not match {expected_arch}"
        )
    if elf_version != 1 or header_size != 64:
        raise DebContractError("desktop prebuilt has malformed ELF header metadata")
    if program_entry_size != 56 or program_count == 0 or program_count > 4096:
        raise DebContractError("desktop prebuilt has an invalid ELF program-header table")
    table_end = program_offset + program_entry_size * program_count
    if program_offset < header_size or table_end > len(payload):
        raise DebContractError("desktop prebuilt has an out-of-bounds ELF program-header table")

    executable_load = False
    entry_in_file_backed_executable_load = False
    max_u64 = (1 << 64) - 1
    for index in range(program_count):
        offset = program_offset + index * program_entry_size
        (
            segment_type,
            segment_flags,
            file_offset,
            virtual_address,
            _physical_address,
            file_size,
            memory_size,
            alignment,
        ) = struct.unpack_from("<IIQQQQQQ", payload, offset)
        if file_offset > len(payload) or file_size > len(payload) - file_offset:
            raise DebContractError("desktop prebuilt has an out-of-bounds ELF segment")
        if segment_type != 1:  # PT_LOAD
            continue
        if file_size > memory_size:
            raise DebContractError("desktop prebuilt PT_LOAD filesz exceeds memsz")
        if virtual_address > max_u64 - memory_size:
            raise DebContractError("desktop prebuilt PT_LOAD virtual range overflows")
        if alignment not in (0, 1):
            if alignment & (alignment - 1):
                raise DebContractError("desktop prebuilt PT_LOAD alignment is not a power of two")
            if virtual_address % alignment != file_offset % alignment:
                raise DebContractError("desktop prebuilt PT_LOAD alignment is inconsistent")
        if segment_flags & 0x1:
            executable_load = True
            if file_size and virtual_address <= entry < virtual_address + file_size:
                entry_in_file_backed_executable_load = True
    if (
        not executable_load
        or entry == 0
        or not entry_in_file_backed_executable_load
    ):
        raise DebContractError(
            "desktop prebuilt entry is not file-backed by an executable PT_LOAD"
        )
    if b"CmdQuit" in payload:
        raise DebContractError("desktop prebuilt contains forbidden legacy CmdQuit marker")


def read_snapshot_bytes(path: str, max_size: int) -> bytes:
    metadata = os.stat(path, follow_symlinks=False)
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > max_size:
        raise DebContractError("private snapshot is not a bounded regular file")
    with open(path, "rb") as source:
        content = source.read(max_size + 1)
    if len(content) > max_size or len(content) != metadata.st_size:
        raise DebContractError("private snapshot changed or exceeded its safety limit")
    return content


def _tar_info(name: str, mode: int, epoch: int, *, directory: bool, size: int = 0) -> tarfile.TarInfo:
    info = tarfile.TarInfo(name)
    info.type = tarfile.DIRTYPE if directory else tarfile.REGTYPE
    info.mode = mode
    info.uid = 0
    info.gid = 0
    info.uname = "root"
    info.gname = "root"
    info.mtime = epoch
    info.size = 0 if directory else size
    return info


def _make_tar(entries: Iterable[tuple[str, int, bool, bytes]], epoch: int) -> bytes:
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w", format=tarfile.USTAR_FORMAT) as archive:
        for name, mode, directory, content in entries:
            info = _tar_info(name, mode, epoch, directory=directory, size=len(content))
            archive.addfile(info, None if directory else io.BytesIO(content))
    return output.getvalue()


def gzip_bytes(content: bytes, epoch: int) -> bytes:
    """Return a canonical gzip stream using deterministic stored DEFLATE blocks."""
    output = io.BytesIO()
    output.write(b"\x1f\x8b\x08\x00")
    output.write(struct.pack("<I", epoch))
    output.write(b"\x00\xff")  # XFL=0, OS=unknown: independent of the build host.
    if not content:
        output.write(b"\x01\x00\x00\xff\xff")
    else:
        offset = 0
        while offset < len(content):
            chunk = content[offset : offset + 65535]
            offset += len(chunk)
            output.write(b"\x01" if offset == len(content) else b"\x00")
            output.write(struct.pack("<HH", len(chunk), len(chunk) ^ 0xFFFF))
            output.write(chunk)
    output.write(
        struct.pack(
            "<II",
            binascii.crc32(content) & 0xFFFFFFFF,
            len(content) & 0xFFFFFFFF,
        )
    )
    return output.getvalue()


def package_version(version: str, release: str) -> str:
    return f"{version}-{release}"


def validate_brand_assets(brand: BrandAssets) -> None:
    if brand.desktop_entry != DESKTOP_ENTRY_BYTES:
        raise DebContractError("Linux desktop entry does not match the canonical WireZTNA launcher")
    if len(brand.icons) != len(ICON_SPECS):
        raise DebContractError("Linux app icon set is incomplete")
    for content, (path, size, expected_sha256) in zip(brand.icons, ICON_SPECS):
        if len(content) < 24 or content[:8] != b"\x89PNG\r\n\x1a\n":
            raise DebContractError(f"Linux app icon is not a PNG: /{path}")
        if content[12:16] != b"IHDR":
            raise DebContractError(f"Linux app icon lacks a canonical IHDR: /{path}")
        width = int.from_bytes(content[16:20], "big")
        height = int.from_bytes(content[20:24], "big")
        if (width, height) != (size, size):
            raise DebContractError(f"Linux app icon has wrong dimensions: /{path}")
        if sha256_bytes(content) != expected_sha256:
            raise DebContractError(f"Linux app icon does not match canonical web branding: /{path}")


def _regular_payload_files(
    payload: bytes, brand: BrandAssets
) -> tuple[tuple[str, int, bytes], ...]:
    validate_brand_assets(brand)
    return (
        (PAYLOAD_PATH, 0o755, payload),
        (DESKTOP_ENTRY_PATH, 0o644, brand.desktop_entry),
        *tuple(
            (path, 0o644, content)
            for content, (path, _size, _sha256) in zip(brand.icons, ICON_SPECS)
        ),
    )


def _data_tar_entries(
    payload: bytes, brand: BrandAssets
) -> tuple[tuple[str, int, bool, bytes], ...]:
    regular = {path: (mode, content) for path, mode, content in _regular_payload_files(payload, brand)}
    return (
        (".", 0o755, True, b""),
        ("./usr", 0o755, True, b""),
        ("./usr/bin", 0o755, True, b""),
        ("./usr/bin/wireztna-desktop", *regular[PAYLOAD_PATH][0:1], False, regular[PAYLOAD_PATH][1]),
        ("./usr/share", 0o755, True, b""),
        ("./usr/share/applications", 0o755, True, b""),
        ("./usr/share/applications/wireztna.desktop", *regular[DESKTOP_ENTRY_PATH][0:1], False, regular[DESKTOP_ENTRY_PATH][1]),
        ("./usr/share/icons", 0o755, True, b""),
        ("./usr/share/icons/hicolor", 0o755, True, b""),
        ("./usr/share/icons/hicolor/32x32", 0o755, True, b""),
        ("./usr/share/icons/hicolor/32x32/apps", 0o755, True, b""),
        ("./usr/share/icons/hicolor/32x32/apps/wireztna.png", 0o644, False, brand.icons[0]),
        ("./usr/share/icons/hicolor/128x128", 0o755, True, b""),
        ("./usr/share/icons/hicolor/128x128/apps", 0o755, True, b""),
        ("./usr/share/icons/hicolor/128x128/apps/wireztna.png", 0o644, False, brand.icons[1]),
        ("./usr/share/icons/hicolor/256x256", 0o755, True, b""),
        ("./usr/share/icons/hicolor/256x256/apps", 0o755, True, b""),
        ("./usr/share/icons/hicolor/256x256/apps/wireztna.png", 0o644, False, brand.icons[2]),
    )


def control_bytes(version: str, release: str, arch: str, installed_content_size: int) -> bytes:
    installed_size = max(1, (installed_content_size + 1023) // 1024)
    fields = (
        ("Package", PACKAGE_NAME),
        ("Version", package_version(version, release)),
        ("Section", "net"),
        ("Priority", "optional"),
        ("Architecture", arch),
        ("Maintainer", "WireZTNA Team <team@wireztna.io>"),
        ("Installed-Size", str(installed_size)),
        ("Homepage", "https://wireztna.io"),
        ("Vendor", "WireZTNA"),
        ("License", "Proprietary"),
        ("Description", "WireZTNA desktop client (E0 build-only package)"),
    )
    return ("".join(f"{name}: {value}\n" for name, value in fields)).encode("utf-8")


def md5sums_bytes(payload: bytes, brand: BrandAssets) -> bytes:
    return "".join(
        f"{_md5_bytes(content)}  {path}\n"
        for path, _mode, content in _regular_payload_files(payload, brand)
    ).encode("ascii")


def make_control_tar_gz(
    version: str, release: str, arch: str, payload: bytes, brand: BrandAssets, epoch: int
) -> bytes:
    installed_content_size = sum(
        len(content) for _path, _mode, content in _regular_payload_files(payload, brand)
    )
    control = control_bytes(version, release, arch, installed_content_size)
    md5sums = md5sums_bytes(payload, brand)
    archive = _make_tar(
        (
            (".", 0o755, True, b""),
            ("./control", 0o644, False, control),
            ("./md5sums", 0o644, False, md5sums),
        ),
        epoch,
    )
    return gzip_bytes(archive, epoch)


def make_data_tar_gz(payload: bytes, brand: BrandAssets, epoch: int) -> bytes:
    return gzip_bytes(_make_tar(_data_tar_entries(payload, brand), epoch), epoch)


def _ar_header(name: str, size: int, epoch: int) -> bytes:
    if name not in AR_MEMBERS or len(name) + 1 > 16:
        raise DebContractError(f"unsupported ar member name: {name}")
    values = (
        f"{name}/".ljust(16),
        str(epoch).ljust(12),
        "0".ljust(6),
        "0".ljust(6),
        format(AR_MODE, "o").ljust(8),
        str(size).ljust(10),
        "`\n",
    )
    header = "".join(values).encode("ascii")
    if len(header) != 60:
        raise DebContractError("ar member metadata exceeds canonical field widths")
    return header


def write_deb(path: str, control_tar_gz: bytes, data_tar_gz: bytes, epoch: int) -> None:
    members = (
        ("debian-binary", b"2.0\n"),
        ("control.tar.gz", control_tar_gz),
        ("data.tar.gz", data_tar_gz),
    )
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_CLOEXEC
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    descriptor = os.open(path, flags, 0o600)
    try:
        with os.fdopen(descriptor, "wb", closefd=False) as output:
            output.write(AR_MAGIC)
            for name, content in members:
                output.write(_ar_header(name, len(content), epoch))
                output.write(content)
                if len(content) & 1:
                    output.write(b"\n")
            output.flush()
            os.fsync(output.fileno())
        os.fchmod(descriptor, 0o644)
    finally:
        os.close(descriptor)


def read_canonical_ar(path: str, epoch: int) -> dict[str, bytes]:
    total_size = os.stat(path, follow_symlinks=False).st_size
    if total_size > MAX_ARTIFACT_SIZE:
        raise DebContractError("DEB exceeds the artifact safety limit")
    members: dict[str, bytes] = {}
    with open(path, "rb") as source:
        if source.read(len(AR_MAGIC)) != AR_MAGIC:
            raise DebContractError("DEB does not begin with the ar global header")
        for expected_name in AR_MEMBERS:
            header = source.read(60)
            if len(header) != 60:
                raise DebContractError(f"DEB is missing canonical ar member {expected_name}")
            try:
                size_text = header[48:58].decode("ascii")
            except UnicodeDecodeError as error:
                raise DebContractError("DEB contains non-ASCII ar metadata") from error
            if not re.fullmatch(r"[0-9]+ *", size_text):
                raise DebContractError("DEB contains an invalid ar member size")
            size = int(size_text.strip(), 10)
            if size > MAX_ARTIFACT_SIZE:
                raise DebContractError("DEB ar member exceeds the safety limit")
            content = source.read(size)
            if len(content) != size:
                raise DebContractError(f"DEB ar member {expected_name} is truncated")
            if header != _ar_header(expected_name, size, epoch):
                raise DebContractError(
                    f"DEB ar member {expected_name} is not in canonical deterministic form"
                )
            if size & 1 and source.read(1) != b"\n":
                raise DebContractError(f"DEB ar member {expected_name} has invalid padding")
            members[expected_name] = content
        if source.read(1):
            raise DebContractError("DEB contains trailing or unexpected ar members")
    if members["debian-binary"] != b"2.0\n":
        raise DebContractError("debian-binary must contain exactly '2.0\\n'")
    return members


def _normalize_tar_name(raw: str, label: str) -> str:
    if not raw or raw.startswith("/") or "\\" in raw or "\x00" in raw:
        raise DebContractError(f"unsafe {label} path: {raw!r}")
    name = raw
    while name.startswith("./"):
        name = name[2:]
    name = name.rstrip("/")
    if name in ("", "."):
        return "."
    parts = name.split("/")
    if any(part in ("", ".", "..") for part in parts):
        raise DebContractError(f"unsafe {label} path: {raw!r}")
    return "/".join(parts)


def _gunzip_canonical(content: bytes, epoch: int, limit: int, label: str) -> bytes:
    try:
        with gzip.GzipFile(fileobj=io.BytesIO(content), mode="rb") as compressed:
            uncompressed = compressed.read(limit + 1)
    except (OSError, EOFError) as error:
        raise DebContractError(f"could not decompress {label}: {error}") from error
    if len(uncompressed) > limit:
        raise DebContractError(f"{label} exceeds its uncompressed safety limit")
    if gzip_bytes(uncompressed, epoch) != content:
        raise DebContractError(f"{label} is not canonical deterministic gzip")
    return uncompressed


def _read_exact_tar(
    raw_tar: bytes,
    expected: Sequence[tuple[str, int, bool]],
    epoch: int,
    label: str,
) -> dict[str, bytes]:
    files: dict[str, bytes] = {}
    try:
        with tarfile.open(fileobj=io.BytesIO(raw_tar), mode="r:") as archive:
            if archive.pax_headers:
                raise DebContractError(f"{label} contains forbidden global PAX metadata")
            members = archive.getmembers()
            if len(members) != len(expected):
                raise DebContractError(f"{label} does not contain the exact E0 member set")
            seen: set[str] = set()
            for member, (raw_name, mode, directory) in zip(members, expected):
                normalized = _normalize_tar_name(member.name, label)
                expected_normalized = _normalize_tar_name(raw_name, label)
                if normalized in seen:
                    raise DebContractError(f"{label} contains duplicate path /{normalized}")
                seen.add(normalized)
                if member.name != raw_name or normalized != expected_normalized:
                    raise DebContractError(
                        f"{label} contains unexpected or reordered path: {member.name!r}"
                    )
                if member.pax_headers:
                    raise DebContractError(f"{label} member {member.name} contains PAX metadata")
                expected_type = tarfile.DIRTYPE if directory else tarfile.REGTYPE
                if member.type != expected_type or member.issym() or member.islnk():
                    raise DebContractError(
                        f"{label} member {member.name} is not the required regular type"
                    )
                if member.linkname:
                    raise DebContractError(f"{label} member {member.name} has a forbidden link target")
                if member.uid != 0 or member.gid != 0 or member.uname != "root" or member.gname != "root":
                    raise DebContractError(f"{label} member {member.name} is not root:root")
                if member.mode != mode or member.mtime != epoch:
                    raise DebContractError(
                        f"{label} member {member.name} has noncanonical mode or timestamp"
                    )
                if directory:
                    if member.size != 0:
                        raise DebContractError(f"{label} directory {member.name} is not empty")
                    continue
                stream = archive.extractfile(member)
                if stream is None:
                    raise DebContractError(f"could not read {label} member {member.name}")
                files[normalized] = stream.read()
    except tarfile.TarError as error:
        raise DebContractError(f"could not parse {label}: {error}") from error
    return files


def _parse_control(content: bytes) -> dict[str, str]:
    try:
        text = content.decode("utf-8")
    except UnicodeDecodeError as error:
        raise DebContractError("DEB control metadata is not UTF-8") from error
    if not text.endswith("\n") or "\r" in text or "\x00" in text or "\n\n" in text:
        raise DebContractError("DEB control metadata is not one canonical paragraph")
    fields: dict[str, str] = {}
    for line in text[:-1].split("\n"):
        if not line or line[0].isspace() or ": " not in line:
            raise DebContractError(f"malformed DEB control metadata line: {line!r}")
        name, value = line.split(": ", 1)
        key = name.lower()
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9-]*", name) or key in fields:
            raise DebContractError(f"invalid or duplicate DEB control field: {name!r}")
        fields[key] = value
    return fields


def inspect_tar_payload(
    members: dict[str, bytes],
    version: str,
    release: str,
    arch: str,
    epoch: int,
) -> TarPayload:
    control_tar = _gunzip_canonical(
        members["control.tar.gz"], epoch, MAX_CONTROL_TAR_SIZE, "control.tar.gz"
    )
    control_files = _read_exact_tar(
        control_tar,
        ((".", 0o755, True), ("./control", 0o644, False), ("./md5sums", 0o644, False)),
        epoch,
        "control.tar",
    )
    data_tar = _gunzip_canonical(
        members["data.tar.gz"], epoch, MAX_DATA_TAR_SIZE, "data.tar.gz"
    )
    data_files = _read_exact_tar(
        data_tar,
        (
            (".", 0o755, True),
            ("./usr", 0o755, True),
            ("./usr/bin", 0o755, True),
            ("./usr/bin/wireztna-desktop", 0o755, False),
            ("./usr/share", 0o755, True),
            ("./usr/share/applications", 0o755, True),
            ("./usr/share/applications/wireztna.desktop", 0o644, False),
            ("./usr/share/icons", 0o755, True),
            ("./usr/share/icons/hicolor", 0o755, True),
            ("./usr/share/icons/hicolor/32x32", 0o755, True),
            ("./usr/share/icons/hicolor/32x32/apps", 0o755, True),
            ("./usr/share/icons/hicolor/32x32/apps/wireztna.png", 0o644, False),
            ("./usr/share/icons/hicolor/128x128", 0o755, True),
            ("./usr/share/icons/hicolor/128x128/apps", 0o755, True),
            ("./usr/share/icons/hicolor/128x128/apps/wireztna.png", 0o644, False),
            ("./usr/share/icons/hicolor/256x256", 0o755, True),
            ("./usr/share/icons/hicolor/256x256/apps", 0o755, True),
            ("./usr/share/icons/hicolor/256x256/apps/wireztna.png", 0o644, False),
        ),
        epoch,
        "data.tar",
    )
    payload = data_files[PAYLOAD_PATH]
    brand = BrandAssets(
        desktop_entry=data_files[DESKTOP_ENTRY_PATH],
        icons=tuple(data_files[path] for path, _size, _sha256 in ICON_SPECS),
    )
    validate_brand_assets(brand)
    installed_content_size = sum(
        len(content) for _path, _mode, content in _regular_payload_files(payload, brand)
    )
    expected_control = control_bytes(version, release, arch, installed_content_size)
    expected_md5sums = md5sums_bytes(payload, brand)
    if control_files["control"] != expected_control:
        fields = _parse_control(control_files["control"])
        expected_fields = _parse_control(expected_control)
        unexpected = sorted(set(fields) ^ set(expected_fields))
        if unexpected:
            raise DebContractError("unexpected DEB control fields: " + ", ".join(unexpected))
        for name, expected_value in expected_fields.items():
            if fields.get(name) != expected_value:
                raise DebContractError(f"unexpected DEB control value for {name}")
        raise DebContractError("DEB control metadata is not in canonical field order")
    if control_files["md5sums"] != expected_md5sums:
        raise DebContractError("md5sums does not match the canonical desktop payload set")
    if _make_tar(
        (
            (".", 0o755, True, b""),
            ("./control", 0o644, False, expected_control),
            ("./md5sums", 0o644, False, expected_md5sums),
        ),
        epoch,
    ) != control_tar:
        raise DebContractError("control.tar is not canonical deterministic ustar")
    if _make_tar(_data_tar_entries(payload, brand), epoch) != data_tar:
        raise DebContractError("data.tar is not canonical deterministic ustar")
    return TarPayload(expected_control, expected_md5sums, payload, brand)


def prepare_output_directory(path: str) -> str:
    absolute = os.path.abspath(path)
    if os.path.lexists(absolute) and os.path.islink(absolute):
        raise DebContractError(f"output directory must not be a symlink: {path}")
    os.makedirs(absolute, mode=0o755, exist_ok=True)
    metadata = os.stat(absolute, follow_symlinks=False)
    if not stat.S_ISDIR(metadata.st_mode):
        raise DebContractError(f"output path is not a directory: {path}")
    return os.path.realpath(absolute)


def write_exclusive(path: str, content: bytes, mode: int = 0o644) -> None:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_CLOEXEC
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    descriptor = os.open(path, flags, mode)
    try:
        view = memoryview(content)
        while view:
            written = os.write(descriptor, view)
            if written <= 0:
                raise DebContractError(f"could not write {path}")
            view = view[written:]
        os.fchmod(descriptor, mode)
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def _file_identity(metadata: os.stat_result) -> FileIdentity:
    return FileIdentity(metadata.st_dev, metadata.st_ino)


def _stat_identity_at(directory_fd: int, name: str) -> FileIdentity | None:
    try:
        return _file_identity(os.stat(name, dir_fd=directory_fd, follow_symlinks=False))
    except FileNotFoundError:
        return None


def _unlink_owned_name(
    directory_fd: int, name: str, expected: FileIdentity
) -> bool:
    """Unlink only when a dirfd-relative name still identifies our inode."""
    if _stat_identity_at(directory_fd, name) != expected:
        return False
    os.unlink(name, dir_fd=directory_fd)
    return True


def atomic_publish_files(
    output_dir: str,
    files: Sequence[PublishedFile],
    *,
    marker_filename: str,
) -> None:
    """Publish stable snapshots without overwrite; marker-last commits the set."""
    if not files or files[-1].filename != marker_filename:
        raise DebContractError("publication marker must be the final published file")
    filenames = tuple(item.filename for item in files)
    if len(set(filenames)) != len(filenames):
        raise DebContractError("publication filenames must be unique")
    for item in files:
        if Path(item.filename).name != item.filename:
            raise DebContractError(f"publication filename is not local: {item.filename}")
        if item.max_size <= 0 or item.max_size > MAX_ARTIFACT_SIZE:
            raise DebContractError("publication source limit is invalid")

    directory_flags = os.O_RDONLY | os.O_CLOEXEC
    if hasattr(os, "O_DIRECTORY"):
        directory_flags |= os.O_DIRECTORY
    if hasattr(os, "O_NOFOLLOW"):
        directory_flags |= os.O_NOFOLLOW
    directory_fd = os.open(output_dir, directory_flags)
    created: dict[str, FileIdentity] = {}
    temporaries: dict[str, FileIdentity] = {}
    try:
        existing = {
            name: identity
            for name in filenames
            if (identity := _stat_identity_at(directory_fd, name)) is not None
        }
        if existing:
            if marker_filename not in existing:
                raise DebContractError(
                    "orphaned publication without marker; refusing cleanup or overwrite"
                )
            if len(existing) != len(filenames):
                raise DebContractError(
                    "incomplete marked publication; refusing cleanup or overwrite"
                )
            raise DebContractError("complete publication already exists; refusing overwrite")

        for item in files:
            temporary = f".wireztna-publish-{secrets.token_hex(16)}"
            source_flags = os.O_RDONLY | os.O_CLOEXEC | os.O_NONBLOCK
            if hasattr(os, "O_NOFOLLOW"):
                source_flags |= os.O_NOFOLLOW
            source_fd = os.open(item.source, source_flags)
            destination_fd: int | None = None
            try:
                before = os.fstat(source_fd)
                if not stat.S_ISREG(before.st_mode) or before.st_size <= 0:
                    raise DebContractError(f"publication source is not regular: {item.source}")
                if before.st_size > item.max_size:
                    raise DebContractError(
                        f"publication source exceeds {item.max_size} bytes: {item.source}"
                    )
                destination_flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_CLOEXEC
                if hasattr(os, "O_NOFOLLOW"):
                    destination_flags |= os.O_NOFOLLOW
                destination_fd = os.open(
                    temporary, destination_flags, item.mode, dir_fd=directory_fd
                )
                temporary_identity = _file_identity(os.fstat(destination_fd))
                temporaries[temporary] = temporary_identity
                copied = 0
                while True:
                    chunk = os.read(source_fd, COPY_CHUNK)
                    if not chunk:
                        break
                    copied += len(chunk)
                    if copied > item.max_size:
                        raise DebContractError(
                            f"publication source changed beyond its limit: {item.source}"
                        )
                    view = memoryview(chunk)
                    while view:
                        written = os.write(destination_fd, view)
                        if written <= 0:
                            raise DebContractError("could not stage local publication")
                        view = view[written:]
                after = os.fstat(source_fd)
                stable_fields = (
                    "st_dev",
                    "st_ino",
                    "st_size",
                    "st_mtime_ns",
                    "st_ctime_ns",
                )
                if copied != before.st_size or any(
                    getattr(before, field) != getattr(after, field)
                    for field in stable_fields
                ):
                    raise DebContractError(
                        f"publication source changed while snapshotting: {item.source}"
                    )
                os.fchmod(destination_fd, item.mode)
                os.fsync(destination_fd)
                if _file_identity(os.fstat(destination_fd)) != temporary_identity:
                    raise DebContractError("publication temporary inode changed while writing")
            finally:
                if destination_fd is not None:
                    os.close(destination_fd)
                os.close(source_fd)

            os.link(
                temporary,
                item.filename,
                src_dir_fd=directory_fd,
                dst_dir_fd=directory_fd,
                follow_symlinks=False,
            )
            final_identity = _stat_identity_at(directory_fd, item.filename)
            if final_identity != temporary_identity:
                raise DebContractError("published name does not identify the staged inode")
            created[item.filename] = temporary_identity
            if not _unlink_owned_name(directory_fd, temporary, temporary_identity):
                raise DebContractError("publication temporary was replaced before cleanup")
            temporaries.pop(temporary)
            os.fsync(directory_fd)
    except BaseException:
        cleanup_order = ([marker_filename] if marker_filename in created else []) + [
            name for name in reversed(tuple(created)) if name != marker_filename
        ]
        for name in cleanup_order:
            _unlink_owned_name(directory_fd, name, created[name])
        os.fsync(directory_fd)
        raise
    finally:
        for temporary, identity in tuple(temporaries.items()):
            _unlink_owned_name(directory_fd, temporary, identity)
        os.close(directory_fd)
