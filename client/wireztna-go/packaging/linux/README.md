# Linux DL-5.1a / E0 package contract

This directory provides **build infrastructure only** for the Linux desktop DEB slice. It does not complete Desktop Linux task 5.1 and does not produce positive evidence unless the caller supplies a real, architecture-matched Linux desktop ELF.

## Contract

`build-deb.sh` is a compatibility entrypoint for the Python 3 stdlib builder. It accepts exactly one already-built Linux desktop ELF through `--desktop-binary`, explicit `--arch` (`amd64` or `arm64`), canonical semver `--version` (`x.y.z`), literal `--release 1`, a full 40-character lowercase `--git-sha`, and a separately supplied full 40-character lowercase `--producer-git-sha`. `SOURCE_DATE_EPOCH` is required. Neither SHA is inferred from a mutable checkout. The builder does not invoke Go, npm, Cargo, an external package builder, an external DEB reader, an installer, or the network.

The canonical DEB is an `ar` archive containing exactly `debian-binary`, `control.tar.gz`, and `data.tar.gz`, in that order. Both tar archives use deterministic ustar metadata and deterministic gzip headers derived from `SOURCE_DATE_EPOCH`. The exact root-owned payload contains `/usr/bin/wireztna-desktop` (`0755`), `/usr/share/applications/wireztna.desktop` (`0644`), and the canonical WireZTNA application mark at 32, 128, and 256 pixels under `/usr/share/icons/hicolor/*/apps/wireztna.png` (`0644`). The launcher resolves the packaged binary through `/usr/bin` and names the icon `wireztna`.

No symlink, hardlink, traversal, duplicate path, special file, PAX metadata, maintainer hook, service, socket, activation rule, capability, extra payload, or legacy `CmdQuit` marker is accepted. Historical `postinstall.sh`, `preremove.sh`, and `wireztna.service` files remain unconsumed scaffolding. The consumed `wireztna.desktop` and PNGs are snapshotted with no-follow opens and verified byte-for-byte against the public WireZTNA branding contract before assembly.

## Reproducible build-only usage

Python 3 is the only packaging and inspection dependency. The input must be an absolute path to a non-symlink, non-empty, executable regular file no larger than 64 MiB. It is opened no-follow, checked through the descriptor, and copied to a private authoritative snapshot. The ELF parser requires a 64-bit little-endian Linux/System V ET_EXEC or ET_DYN image for the requested machine, valid bounded `PT_LOAD` segments, and an entry point backed by file bytes in an executable `PT_LOAD`. The completed DEB and compressed/uncompressed data representations are bounded at no more than 128 MiB.

```bash
SOURCE_DATE_EPOCH=1700000000 \
./packaging/linux/build-deb.sh \
  --arch amd64 \
  --version 0.9.30 \
  --release 1 \
  --git-sha 0123456789abcdef0123456789abcdef01234567 \
  --producer-git-sha 89abcdef0123456789abcdef0123456789abcdef \
  --desktop-binary /absolute/path/to/wireztna-desktop-amd64 \
  --output-dir /absolute/path/to/output
```

Repeat with a real arm64 ELF and `--arch arm64` for arm64. Unsupported or repeated options, noncanonical coordinates, missing epoch, unsafe files, ABI mismatch, malformed ELF, existing outputs, incomplete/orphaned outputs, or noncanonical archives fail closed.

Identical prebuilt bytes and coordinates produce identical outputs. A successful assembly publishes exactly four files:

- `wireztna-<version>-linux-<arch>.deb`
- `wireztna-<version>-linux-<arch>.build.log`
- `wireztna-<version>-linux-<arch>.sha256`
- `wireztna-<version>-linux-<arch>.e0.json`

Before publication, the integrated verifier snapshots and reparses the DEB and parses every build-log field, requiring exact agreement with the inspected candidate. Publication snapshots all four staged files into the destination filesystem using directory-relative, no-follow operations. It records inode identities, refuses every overwrite and every pre-existing orphan set without a marker, and never cleans up a name that no longer identifies an inode created by the current attempt. The DEB, finalized log, and digest are linked first; `.e0.json` is linked last as the logical transaction marker. Consumers must reject any set without all four files and must verify the evidence-bound hashes.

## Standalone verifier

`verify-deb.sh` independently snapshots the supplied DEB and build log, parses `ar`, gzip, tar, control metadata, md5sums, payload, ELF, and every build-log field without extraction, installation, or payload execution. It requires the exact artifact basename `wireztna-<version>-linux-<arch>.deb` and matching build-log basename.

The verifier is not a transactional publisher and never writes `.sha256` or `.e0.json` beside the originals. On success it emits only the `release.PlatformEvidence` v1 JSON document to stdout, or writes that same JSON exclusively to `--report-out` with no stdout output:

```bash
SOURCE_DATE_EPOCH=1700000000 \
./packaging/linux/verify-deb.sh \
  --artifact /absolute/path/to/wireztna-0.9.30-linux-amd64.deb \
  --expected-arch amd64 \
  --expected-version 0.9.30 \
  --expected-release 1 \
  --expected-package wireztna-desktop \
  --expected-payload-sha256 <64-lowercase-hex> \
  --git-sha 0123456789abcdef0123456789abcdef01234567 \
  --producer-git-sha 89abcdef0123456789abcdef0123456789abcdef \
  --build-log /absolute/path/to/wireztna-0.9.30-linux-amd64.build.log
```

The evidence envelope has exactly `identity`, `source`, `tuple`, `claim`, `gates`, `signature`, `provenance`, and `limitations`. Its tuple is the exact candidate identity and bytes. The claim is literal `build-only`; runtime and public eligibility are false. The source identifies `linux-deb-source-contract-inspector`, binds the explicit producer Git SHA, and sets `test_fixture` to false. Signature and provenance remain `not-asserted`; the evidence does not authorize promotion or publication.

Run the existing local contract script with `./packaging/linux/package_contract_test.sh`. Its positive path runs only for explicit `E0_AMD64_PREBUILT` and/or `E0_ARM64_PREBUILT` values pointing to real executable prebuilts. Without one it skips the positive package path and makes no positive E0 claim.

E0 is not E1-E4: these files make no installation, lifecycle, desktop-session, network, service, socket, runtime, signing, repository-trust, or support-matrix claim.
