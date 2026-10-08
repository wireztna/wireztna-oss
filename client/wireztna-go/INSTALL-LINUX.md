# WireZTNA Client — Linux Installation

## Prerequisites

1. **Linux kernel 5.6+** (WireGuard built-in) or `wireguard-go` for older kernels
2. **systemd-resolved** (optional, for split DNS — most modern distros include it)

## Installation

```bash
# Download the binary (amd64 or arm64 depending on your architecture)
chmod +x wireztna-linux-amd64
sudo mv wireztna-linux-amd64 /usr/local/bin/wireztna
```

### Recommended: enable rootless operation

Using `setcap` avoids issues with `sudo` changing `$HOME` (which breaks token and config file lookup). This is the recommended setup:

```bash
sudo setcap 'cap_net_admin+ep cap_net_raw+ep' /usr/local/bin/wireztna
```

After this, all `wireztna` commands run without `sudo`.

> **Why not sudo?** Running with `sudo` changes `$HOME` to `/root`, so the client cannot find your config and token in `~/.wireztna/`. If you must use sudo, use `sudo -E` to preserve your environment.

## Enrollment (one-time setup)

Your administrator will provide you with an enrollment URL.

```bash
wireztna enroll "http://<server>/api/v1/clients/enroll?token=<your-token>"
```

This generates your WireGuard keys and downloads your configuration automatically.

## Login

```bash
wireztna login
```

Enter your username and password when prompted.

## Connect

```bash
wireztna connect
```

Or use the interactive TUI:

```bash
wireztna tui
```

If you did **not** configure `setcap`, use `sudo -E` (the `-E` preserves your HOME directory):

```bash
sudo -E wireztna connect
sudo -E wireztna tui
```

## Verify connection

```bash
# Check WireGuard interface
wg show wg-wireztna

# Check DNS routing (systemd-resolved)
resolvectl status wg-wireztna

# Test connectivity to internal resource
ping <internal-hostname>
```

## Disconnect

Press `Ctrl+C` in the terminal, or from another shell:

```bash
wireztna disconnect
```

Or press `q` in the TUI.

## Troubleshooting

| Problem | Solution |
|---------|----------|
| "failed to create wgctrl client" | Ensure WireGuard kernel module is loaded: `sudo modprobe wireguard` |
| "Operation not permitted" | Apply `setcap` (see Installation above) or use `sudo -E` |
| DNS not resolving but ping by IP works | You used `sudo` without `-E`. Use `sudo -E wireztna connect` or apply `setcap` |
| "Token expired" | Ask your admin for a new enrollment token |
| "Token rejected" | Run `wireztna login` to refresh your session |
| DNS not resolving internal names | Check: `resolvectl status wg-wireztna` — verify DNS and Domains are set |
| "interface not found" on older kernels | Install `wireguard-go`: `sudo apt install wireguard-go` or `sudo yum install wireguard-tools` |
| "API URL required" after sudo | `sudo` changed HOME. Use `sudo -E` or `setcap` |

## Notes

- Config is stored in `~/.wireztna/config.yaml`
- Session auto-renews in the background (default: 30 min before expiry)
- The tunnel stays active until you disconnect or the process is killed
- Split DNS routes only configured zones through the tunnel; all other DNS uses your system resolver
