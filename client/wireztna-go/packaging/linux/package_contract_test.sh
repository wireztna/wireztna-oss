#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
command -v python3 >/dev/null 2>&1 || {
  printf 'FAIL: required command not found: python3\n' >&2
  exit 1
}
exec python3 - "$script_dir" <<'PY'
import hashlib
import json
import os
import pathlib
import shutil
import stat
import struct
import subprocess
import sys
import tempfile

root = pathlib.Path(sys.argv[1])
sys.path.insert(0, str(root))
import deb_archive
import verify_deb

build_wrapper = root / "build-deb.sh"
verify_wrapper = root / "verify-deb.sh"
build_module = root / "build_deb.py"
verify_module = root / "verify_deb.py"
archive_module = root / "deb_archive.py"


def fail(message):
    raise SystemExit(f"FAIL: {message}")


def require_text(path, text):
    if text not in path.read_text(encoding="utf-8"):
        fail(f"{path.name} does not contain required contract text: {text}")


def expect_failure(name, command, environment):
    completed = subprocess.run(
        command,
        env=environment,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        check=False,
    )
    if completed.returncode == 0:
        fail(f"{name} unexpectedly succeeded")


for required in (
    build_wrapper,
    verify_wrapper,
    build_module,
    verify_module,
    archive_module,
):
    if not required.is_file():
        fail(f"missing contract file: {required}")

for active in (build_wrapper, verify_wrapper, build_module, verify_module, archive_module):
    lowered = active.read_text(encoding="utf-8").lower()
    for forbidden in ("nfpm", "dpkg-deb"):
        if forbidden in lowered:
            fail(f"{active.name} retains forbidden external packaging dependency: {forbidden}")

require_text(archive_module, 'AR_MEMBERS = ("debian-binary", "control.tar.gz", "data.tar.gz")')
require_text(archive_module, 'MAX_PREBUILT_SIZE = 64 * 1024 * 1024')
require_text(archive_module, 'MAX_ARTIFACT_SIZE = 128 * 1024 * 1024')
require_text(archive_module, 'MAX_DATA_TAR_SIZE = 128 * 1024 * 1024')
require_text(archive_module, 'tarfile.USTAR_FORMAT')
require_text(archive_module, 'deterministic stored DEFLATE blocks')
require_text(archive_module, 'os.O_NOFOLLOW')
require_text(archive_module, 'member.issym() or member.islnk()')
require_text(archive_module, 'PAYLOAD_PATH = "usr/bin/wireztna-desktop"')
require_text(archive_module, 'DESKTOP_ENTRY_PATH = "usr/share/applications/wireztna.desktop"')
require_text(archive_module, 'usr/share/icons/hicolor/256x256/apps/wireztna.png')
require_text(archive_module, 'Linux app icon does not match canonical web branding')
require_text(archive_module, 'if b"CmdQuit" in payload:')
require_text(archive_module, 'if elf_type not in (2, 3):')
require_text(archive_module, 'if segment_type != 1:  # PT_LOAD')
require_text(archive_module, 'virtual_address <= entry < virtual_address + file_size')
require_text(archive_module, 'def _unlink_owned_name(')
require_text(archive_module, 'dir_fd=directory_fd, follow_symlinks=False')
require_text(archive_module, 'orphaned publication without marker')
require_text(build_module, 'require_source_date_epoch(os.environ.get("SOURCE_DATE_EPOCH"))')
require_text(build_module, 'parser.add_argument("--producer-git-sha", required=True)')
require_text(build_module, 'artifact_name = expected_artifact_filename(version, options.arch)')
require_text(build_module, 'verified, evidence = verify(')
require_text(build_module, 'PublishedFile(staged_evidence, marker_name')
require_text(verify_module, '"contract": "wireztna-platform-evidence"')
require_text(verify_module, '"level": "build-only"')
require_text(verify_module, '"producer": PRODUCER')
require_text(verify_module, '"producer_git_sha": producer_git_sha')
require_text(verify_module, '"test_fixture": False')
require_text(verify_module, '"signature": {"status": "not-asserted"}')
require_text(verify_module, '"provenance": {"status": "not-asserted"}')
require_text(verify_module, 'parser.add_argument("--report-out")')
if "evidence-prefix" in verify_module.read_text(encoding="utf-8"):
    fail("standalone verifier retains transactional evidence publication")

if (root / "nfpm.yaml").exists():
    fail("obsolete nfpm.yaml remains present")

if deb_archive.MAX_PREBUILT_SIZE != 64 * 1024 * 1024:
    fail("prebuilt limit is not exactly 64 MiB")
if not 0 < deb_archive.MAX_ARTIFACT_SIZE <= 128 * 1024 * 1024:
    fail("artifact limit exceeds 128 MiB")
if not 0 < deb_archive.MAX_DATA_TAR_SIZE <= 128 * 1024 * 1024:
    fail("data limit exceeds 128 MiB")
for invalid_version in ("1", "1.2", "01.2.3", "1.02.3", "1.2.03", "1.2.3-rc1", "1.2.3+meta"):
    try:
        deb_archive.require_version(invalid_version)
    except deb_archive.DebContractError:
        pass
    else:
        fail(f"noncanonical version accepted: {invalid_version}")
for invalid_sha in ("A" * 40, "0" * 39, "0" * 64):
    try:
        deb_archive.require_git_sha(invalid_sha)
    except deb_archive.DebContractError:
        pass
    else:
        fail("noncanonical Git SHA accepted")
try:
    deb_archive.require_release("2")
except deb_archive.DebContractError:
    pass
else:
    fail("release other than 1 was accepted")

canonical_log_fields = verify_deb.expected_build_log(
    arch="amd64",
    version="1.2.3",
    release="1",
    git_sha="0" * 40,
    producer_git_sha="1" * 40,
    epoch=1700000000,
    payload_sha256="2" * 64,
    artifact_filename="wireztna-1.2.3-linux-amd64.deb",
    artifact_sha256="3" * 64,
)
canonical_log = verify_deb.encode_build_log(canonical_log_fields)
verify_deb.verify_build_log(canonical_log, canonical_log_fields)
for malformed_log in (
    canonical_log.replace(b"release=1\n", b"release=2\n"),
    canonical_log + b"release=1\n",
    canonical_log.replace(b"status=build-only\n", b""),
):
    try:
        verify_deb.verify_build_log(malformed_log, canonical_log_fields)
    except deb_archive.DebContractError:
        pass
    else:
        fail("noncanonical or mismatched build log was accepted")


def minimal_elf(entry_in_file=True):
    payload = bytearray(0x180)
    payload[:16] = b"\x7fELF\x02\x01\x01\x00" + b"\x00" * 8
    entry = 0x400100 if entry_in_file else 0x400180
    struct.pack_into(
        "<HHIQQQIHHHHHH",
        payload,
        16,
        2,
        62,
        1,
        entry,
        64,
        0,
        0,
        64,
        56,
        1,
        0,
        0,
        0,
    )
    struct.pack_into("<IIQQQQQQ", payload, 64, 1, 5, 0, 0x400000, 0, 0x180, 0x200, 0x1000)
    return bytes(payload)


deb_archive.validate_elf(minimal_elf(), "amd64")
try:
    deb_archive.validate_elf(minimal_elf(entry_in_file=False), "amd64")
except deb_archive.DebContractError:
    pass
else:
    fail("ELF entry in PT_LOAD memory tail but outside file-backed bytes was accepted")

icon_dir = root.parents[2] / "wireztna-desktop" / "src-tauri" / "icons"
brand = deb_archive.BrandAssets(
    desktop_entry=(root / "wireztna.desktop").read_bytes(),
    icons=tuple(
        (icon_dir / name).read_bytes()
        for name in ("32x32.png", "128x128.png", "128x128@2x.png")
    ),
)
deb_archive.validate_brand_assets(brand)
tampered_icons = list(brand.icons)
tampered_icons[0] = tampered_icons[0] + b"tampered"
try:
    deb_archive.validate_brand_assets(
        deb_archive.BrandAssets(brand.desktop_entry, tuple(tampered_icons))
    )
except deb_archive.DebContractError:
    pass
else:
    fail("modified Linux app icon was accepted")

with tempfile.TemporaryDirectory(prefix="wireztna-branded-deb-contract-") as brand_temporary:
    epoch = 1700000000
    payload = minimal_elf()
    control_tar = deb_archive.make_control_tar_gz("1.2.3", "1", "amd64", payload, brand, epoch)
    data_tar = deb_archive.make_data_tar_gz(payload, brand, epoch)
    artifact = pathlib.Path(brand_temporary) / "wireztna-1.2.3-linux-amd64.deb"
    deb_archive.write_deb(str(artifact), control_tar, data_tar, epoch)
    inspected = deb_archive.inspect_tar_payload(
        deb_archive.read_canonical_ar(str(artifact), epoch), "1.2.3", "1", "amd64", epoch
    )
    if inspected.payload != payload or inspected.brand != brand:
        fail("branded Linux payload did not survive canonical build and inspection")

with tempfile.TemporaryDirectory(prefix="wireztna-package-contract-") as temporary_name:
    temporary = pathlib.Path(temporary_name)
    fake_binary = temporary / "not-an-elf"
    fake_binary.write_bytes(b"#!/bin/sh\nexit 0\n")
    fake_binary.chmod(0o755)
    symlink = temporary / "symlink-prebuilt"
    symlink.symlink_to(fake_binary)
    build_log = temporary / "wireztna-1.0.0-linux-amd64.build.log"
    build_log.write_text("closed build log\n", encoding="utf-8")
    environment = os.environ.copy()
    environment["SOURCE_DATE_EPOCH"] = "1700000000"
    git_sha = "0" * 40
    producer_git_sha = "1" * 40
    digest = "0" * 64
    common = ["--git-sha", git_sha, "--producer-git-sha", producer_git_sha]

    expect_failure(
        "unsupported_arch",
        [str(build_wrapper), "--arch", "i386", "--version", "1.0.0", *common,
         "--desktop-binary", str(fake_binary), "--output-dir", str(temporary / "out")],
        environment,
    )
    for name, version in (("short_version", "1.0"), ("prerelease_version", "1.0.0-rc1")):
        expect_failure(
            name,
            [str(build_wrapper), "--arch", "amd64", "--version", version, *common,
             "--desktop-binary", str(fake_binary), "--output-dir", str(temporary / "out")],
            environment,
        )
    expect_failure(
        "release_not_one",
        [str(build_wrapper), "--arch", "amd64", "--version", "1.0.0", "--release", "2", *common,
         "--desktop-binary", str(fake_binary), "--output-dir", str(temporary / "out")],
        environment,
    )
    expect_failure(
        "uppercase_git_sha",
        [str(build_wrapper), "--arch", "amd64", "--version", "1.0.0",
         "--git-sha", "A" * 40, "--producer-git-sha", producer_git_sha,
         "--desktop-binary", str(fake_binary), "--output-dir", str(temporary / "out")],
        environment,
    )
    expect_failure(
        "missing_prebuilt",
        [str(build_wrapper), "--arch", "amd64", "--version", "1.0.0", *common,
         "--desktop-binary", str(temporary / "missing"), "--output-dir", str(temporary / "out")],
        environment,
    )
    expect_failure(
        "symlink_prebuilt",
        [str(build_wrapper), "--arch", "amd64", "--version", "1.0.0", *common,
         "--desktop-binary", str(symlink), "--output-dir", str(temporary / "out")],
        environment,
    )
    expect_failure(
        "non_elf_prebuilt",
        [str(build_wrapper), "--arch", "amd64", "--version", "1.0.0", *common,
         "--desktop-binary", str(fake_binary), "--output-dir", str(temporary / "out")],
        environment,
    )
    expect_failure("unknown_argument", [str(build_wrapper), "--source-tree", str(temporary)], environment)
    expect_failure(
        "duplicate_argument",
        [str(build_wrapper), "--arch", "amd64", "--arch", "arm64", "--version", "1.0.0", *common,
         "--desktop-binary", str(fake_binary)],
        environment,
    )
    no_epoch = environment.copy()
    no_epoch.pop("SOURCE_DATE_EPOCH", None)
    expect_failure(
        "missing_epoch",
        [str(build_wrapper), "--arch", "amd64", "--version", "1.0.0", *common,
         "--desktop-binary", str(fake_binary), "--output-dir", str(temporary / "out")],
        no_epoch,
    )
    expect_failure(
        "wrong_basename",
        [str(verify_wrapper), "--artifact", str(temporary / "wrong.deb"),
         "--expected-arch", "amd64", "--expected-version", "1.0.0",
         "--expected-release", "1", "--expected-package", "wireztna-desktop",
         "--expected-payload-sha256", digest, *common, "--build-log", str(build_log)],
        environment,
    )

    source_files = []
    for name in ("a", "b", "c", "marker"):
        path = temporary / f"source-{name}"
        path.write_text(name, encoding="ascii")
        source_files.append(deb_archive.PublishedFile(str(path), name, max_size=16))
    orphan_dir = temporary / "orphan-output"
    orphan_dir.mkdir()
    (orphan_dir / "a").write_text("orphan", encoding="ascii")
    try:
        deb_archive.atomic_publish_files(str(orphan_dir), source_files, marker_filename="marker")
    except deb_archive.DebContractError:
        pass
    else:
        fail("orphan publication without marker was accepted")
    if (orphan_dir / "a").read_text(encoding="ascii") != "orphan":
        fail("orphan publication was modified during fail-closed preflight")

    race_dir = temporary / "race-output"
    race_dir.mkdir()
    real_link = deb_archive.os.link

    def replacing_link(source, destination, **kwargs):
        if destination == "b":
            os.unlink("a", dir_fd=kwargs["dst_dir_fd"])
            fd = os.open("a", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600, dir_fd=kwargs["dst_dir_fd"])
            os.write(fd, b"replacement")
            os.close(fd)
            raise OSError("injected publication failure")
        real_link(source, destination, **kwargs)

    deb_archive.os.link = replacing_link
    try:
        try:
            deb_archive.atomic_publish_files(str(race_dir), source_files, marker_filename="marker")
        except OSError:
            pass
        else:
            fail("injected publication failure did not fail")
    finally:
        deb_archive.os.link = real_link
    if (race_dir / "a").read_bytes() != b"replacement":
        fail("cleanup deleted a substituted publication inode")

    print("PASS: DL-5.1a/E0 source contract and fail-closed preflight checks")

    def file_sha256(path):
        digest_value = hashlib.sha256()
        with path.open("rb") as source:
            for chunk in iter(lambda: source.read(1024 * 1024), b""):
                digest_value.update(chunk)
        return digest_value.hexdigest()

    def positive_contract(arch, prebuilt):
        prebuilt_path = pathlib.Path(prebuilt)
        metadata = prebuilt_path.lstat()
        if not prebuilt_path.is_absolute() or not stat.S_ISREG(metadata.st_mode) or metadata.st_mode & 0o111 == 0:
            fail(f"real executable prebuilt for {arch} is unavailable: {prebuilt}")
        outputs = []
        for index in (1, 2):
            output_dir = temporary / f'output-quote-"-colon-:-hash-#-{arch}-{index}'
            command = [
                str(build_wrapper), "--arch", arch, "--version", "0.0.0",
                "--release", "1", *common,
                "--desktop-binary", str(prebuilt_path), "--output-dir", str(output_dir),
            ]
            completed = subprocess.run(command, env=environment, check=False)
            if completed.returncode != 0:
                fail(f"positive E0 build failed for supplied real {arch} prebuilt")
            stem = output_dir / f"wireztna-0.0.0-linux-{arch}"
            files = {suffix: pathlib.Path(str(stem) + suffix) for suffix in (".deb", ".build.log", ".sha256", ".e0.json")}
            if any(not path.is_file() or path.stat().st_size == 0 for path in files.values()):
                fail(f"positive E0 build did not publish all four files for {arch}")
            evidence = json.loads(files[".e0.json"].read_text(encoding="utf-8"))
            deb_sha = file_sha256(files[".deb"])
            expected_tuple = {
                "version": "0.0.0",
                "git_sha": git_sha,
                "os": "linux",
                "arch": arch,
                "type": "deb",
                "artifact": {
                    "filename": files[".deb"].name,
                    "size": files[".deb"].stat().st_size,
                    "sha256": deb_sha,
                },
            }
            if set(evidence) != {"identity", "source", "tuple", "claim", "gates", "signature", "provenance", "limitations"}:
                fail("PlatformEvidence does not have the exact v1 envelope")
            if evidence["tuple"] != expected_tuple:
                fail("PlatformEvidence tuple is not the exact candidate")
            if evidence["identity"] != {
                "contract": "wireztna-platform-evidence",
                "schema_version": 1,
                "evidence_id": f"linux.deb.build-only:{deb_sha}",
                "generated_at": "2023-11-14T22:13:20Z",
            }:
                fail("PlatformEvidence identity is not exact")
            if evidence["source"] != {
                "producer": "linux-deb-source-contract-inspector",
                "producer_git_sha": producer_git_sha,
                "test_fixture": False,
            }:
                fail("PlatformEvidence source is not exact")
            if evidence["claim"] != {
                "level": "build-only", "artifact": True, "runtime": False, "public_eligibility": False
            }:
                fail("PlatformEvidence claim is not exact build-only")
            if evidence["gates"] != [{"id": "linux.deb.source-contract-inspected", "status": "passed"}]:
                fail("PlatformEvidence gate is not exact")
            if evidence["signature"] != {"status": "not-asserted"} or evidence["provenance"] != {"status": "not-asserted"}:
                fail("PlatformEvidence signature/provenance overclaim")
            if evidence["limitations"] != verify_deb.LIMITATIONS:
                fail("PlatformEvidence limitations are not exact")
            if files[".sha256"].read_text(encoding="ascii") != f"{deb_sha}  {files['.deb'].name}\n":
                fail("published digest does not match the DEB")

            standalone = temporary / f"standalone-{arch}-{index}"
            standalone.mkdir()
            copied_deb = standalone / files[".deb"].name
            copied_log = standalone / files[".build.log"].name
            shutil.copyfile(files[".deb"], copied_deb)
            shutil.copyfile(files[".build.log"], copied_log)
            verify_command = [
                str(verify_wrapper), "--artifact", str(copied_deb), "--expected-arch", arch,
                "--expected-version", "0.0.0", "--expected-release", "1",
                "--expected-package", "wireztna-desktop", "--expected-payload-sha256", file_sha256(prebuilt_path),
                *common, "--build-log", str(copied_log),
            ]
            standalone_result = subprocess.run(verify_command, env=environment, stdout=subprocess.PIPE, check=False)
            if standalone_result.returncode or json.loads(standalone_result.stdout) != evidence:
                fail("standalone verifier did not emit the exact PlatformEvidence JSON")
            report_out = standalone / "report.json"
            report_result = subprocess.run(
                [*verify_command, "--report-out", str(report_out)],
                env=environment,
                stdout=subprocess.PIPE,
                check=False,
            )
            if report_result.returncode or report_result.stdout or json.loads(report_out.read_bytes()) != evidence:
                fail("standalone --report-out did not exclusively write the exact JSON")
            if set(path.name for path in standalone.iterdir()) != {copied_deb.name, copied_log.name, report_out.name}:
                fail("standalone verifier created a transactional marker beside originals")
            outputs.append(files)
        if outputs[0][".deb"].read_bytes() != outputs[1][".deb"].read_bytes():
            fail(f"repeated {arch} builds with identical inputs are not reproducible")

    supplied = [
        ("amd64", os.environ.get("E0_AMD64_PREBUILT")),
        ("arm64", os.environ.get("E0_ARM64_PREBUILT")),
    ]
    supplied = [(arch, path) for arch, path in supplied if path]
    if not supplied:
        print("SKIP: positive DEB inspection requires an explicitly supplied real ELF prebuilt; no E0 claim made.")
    else:
        for architecture, path in supplied:
            positive_contract(architecture, path)
        print("PASS: deterministic positive DEB inspection completed only for supplied real prebuilts")
PY
