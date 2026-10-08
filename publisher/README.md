# publisher (CE)

Publisher agent: the outbound-connecting foot inside a private network. Go binary
(8 MB static ELF) or Docker container.

> **Status: populated.** The CE source is in this directory. See `../docs/ce-build-plan.md`.

**Stack:** Go (wgctrl, netlink); Alpine + wireguard-tools for the Docker variant.

**CE notes:**
- Enrolls with `--token "<enroll URL>"` at runtime; broker URL is not compiled in.
- Connects outbound only; no inbound firewall ports required.
- One generic binary per OS/arch — no per-broker rebuild.
