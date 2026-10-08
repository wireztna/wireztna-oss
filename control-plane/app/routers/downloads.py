"""Client binary downloads — serves prebuilt binaries for all platforms."""

import hashlib
import os
from pathlib import Path

from fastapi import APIRouter, HTTPException
from fastapi.responses import FileResponse

router = APIRouter()

# Directory where client binaries are stored on the broker
DOWNLOADS_DIR = Path(os.environ.get("DOWNLOADS_DIR", "/opt/wireztna/downloads"))

# Client version — update when releasing new binaries
CLIENT_VERSION = os.environ.get("CLIENT_VERSION", "0.9.31")

# Cache checksums to avoid re-hashing on every request
_checksum_cache: dict[str, str] = {}


def _compute_sha256(filepath: Path) -> str:
    """Compute SHA256 checksum of a file (cached by path+mtime)."""
    cache_key = f"{filepath}:{filepath.stat().st_mtime}"
    if cache_key in _checksum_cache:
        return _checksum_cache[cache_key]
    h = hashlib.sha256()
    with open(filepath, "rb") as f:
        while chunk := f.read(65536):
            h.update(chunk)
    digest = h.hexdigest()
    _checksum_cache[cache_key] = digest
    return digest


def _classify_file(name: str) -> dict:
    """Classify a download file by platform, arch, and type."""
    lower_name = name.lower()
    platform = "unknown"
    arch = "unknown"
    file_type = "cli"  # default: CLI binary

    # Platform detection
    if "android" in lower_name or lower_name.endswith(".apk"):
        platform = "android"
    elif "windows" in lower_name:
        platform = "windows"
    elif "linux" in lower_name:
        platform = "linux"
    elif "darwin" in lower_name or "macos" in lower_name or lower_name.endswith(".pkg"):
        platform = "macos"

    # Architecture detection
    if "amd64" in lower_name or "x64" in lower_name:
        arch = "amd64"
    elif "arm64" in lower_name:
        arch = "arm64"
    elif "universal" in lower_name or lower_name.endswith(".apk"):
        arch = "universal"

    # Type detection — order matters (more specific first)
    if lower_name.endswith((".apk", ".msi", ".dmg", ".pkg")):
        file_type = "installer"
    elif lower_name.endswith((".deb", ".rpm")):
        file_type = "package"
    elif "desktop" in lower_name:
        file_type = "desktop"

    # Human-readable description
    if lower_name.endswith(".pkg"):
        description = "macOS Installer (PKG) — includes app, privileged helper, and bundled WireGuard runtime"
    else:
        descriptions = {
            ("installer", "android"): "Android App (APK) — install directly on device",
            ("installer", "windows"): "Windows Installer (MSI) — includes service + tray app",
            ("installer", "macos"): "macOS Installer (DMG) — includes app + CLI",
            ("package", "linux"): "Linux Package — includes service + tray app",
            ("desktop", "windows"): "Windows Tray App (standalone)",
            ("desktop", "macos"): "macOS Tray App (standalone)",
            ("desktop", "linux"): "Linux Tray App (standalone)",
            ("cli", "windows"): "Windows CLI + TUI (portable binary)",
            ("cli", "macos"): "macOS CLI + TUI (portable binary)",
            ("cli", "linux"): "Linux CLI + TUI (portable binary)",
        }
        description = descriptions.get((file_type, platform), f"{platform} {file_type}")

    return {
        "platform": platform,
        "arch": arch,
        "type": file_type,
        "description": description,
    }


@router.get("")
async def list_downloads():
    """List available client binaries for download.

    No authentication required — binaries are useless without enrollment.
    Returns files grouped by type: installers first, then packages, then CLI.
    """
    if not DOWNLOADS_DIR.exists():
        return {"version": CLIENT_VERSION, "files": []}

    # Extensions to exclude from listing — raw .exe are superseded by the MSI installer
    EXCLUDED_EXTENSIONS = {".exe"}

    files = []
    for f in sorted(DOWNLOADS_DIR.iterdir()):
        if f.is_file() and not f.name.startswith(".") and f.suffix.lower() not in EXCLUDED_EXTENSIONS:
            # Sigstore .bundle files are listed as signature metadata, not standalone downloads
            if f.name.endswith(".bundle"):
                continue
            info = _classify_file(f.name)
            # Check if a corresponding Sigstore signature bundle exists
            bundle_path = DOWNLOADS_DIR / f"{f.name}.bundle"
            signature_url = f"/api/v1/downloads/{f.name}.bundle" if bundle_path.exists() else None
            entry = {
                "filename": f.name,
                "platform": info["platform"],
                "arch": info["arch"],
                "type": info["type"],
                "description": info["description"],
                "size_bytes": f.stat().st_size,
                "sha256": _compute_sha256(f),
                "download_url": f"/api/v1/downloads/{f.name}",
            }
            if signature_url:
                entry["sigstore_bundle_url"] = signature_url
            files.append(entry)

    # Sort: installers first, with the self-contained macOS PKG ahead of other
    # formats for the same platform/architecture.
    type_order = {"installer": 0, "package": 1, "desktop": 2, "cli": 3}
    files.sort(
        key=lambda x: (
            type_order.get(x["type"], 9),
            x["platform"],
            x["arch"],
            0 if x["filename"].lower().endswith(".pkg") else 1,
        )
    )

    return {"version": CLIENT_VERSION, "files": files}


@router.get("/{filename}")
async def download_file(filename: str):
    """Download a specific client binary.

    No authentication required — binaries are useless without enrollment.
    """
    # Prevent path traversal
    if ".." in filename or "/" in filename or "\\" in filename:
        raise HTTPException(status_code=400, detail="Invalid filename")

    filepath = DOWNLOADS_DIR / filename
    if not filepath.exists() or not filepath.is_file():
        raise HTTPException(status_code=404, detail="File not found")

    # Determine media type
    lower_filename = filename.lower()
    media_type = "application/octet-stream"
    if lower_filename.endswith(".apk"):
        media_type = "application/vnd.android.package-archive"
    elif lower_filename.endswith(".exe"):
        media_type = "application/vnd.microsoft.portable-executable"
    elif lower_filename.endswith(".msi"):
        media_type = "application/x-msi"
    elif lower_filename.endswith(".dmg"):
        media_type = "application/x-apple-diskimage"
    elif lower_filename.endswith(".pkg"):
        media_type = "application/vnd.apple.installer+xml"
    elif lower_filename.endswith(".deb"):
        media_type = "application/vnd.debian.binary-package"
    elif lower_filename.endswith(".rpm"):
        media_type = "application/x-rpm"
    elif lower_filename.endswith(".bundle"):
        media_type = "application/json"

    return FileResponse(
        path=str(filepath),
        filename=filename,
        media_type=media_type,
    )
