# WireZTNA Desktop

Tauri 2/Svelte desktop shell for WireZTNA. The frontend consumes the authenticated IPC v2 controller contract and keeps privileged WireGuard, route, and DNS mutations outside the UI process.

## Platform boundary

- **macOS:** functional IPC v2 bridge over `/var/run/wireztna/desktop-v2.sock`, including root peer verification, command/event channels, lifecycle events, cancellation, and shutdown.
- **Linux:** the shared Tauri shell, tray, and application assets compile from the same sources, but native IPC still returns `IPC_UNSUPPORTED_PLATFORM`. The separate Go tray and build-only DEB use the same status palette and application branding. This is visual/package parity, not functional desktop parity.
- Closing the main window hides it. Quitting exits only the UI process and never disconnects the tunnel.

## Visual contract

The tray uses one shield/check status design on desktop platforms:

- gray `#8E8E93`: stable disconnected state only;
- green `#34C759`: connected and confirmed healthy;
- orange `#FF9500`: startup, transition, degraded health, authentication/setup requirement, unavailable service, or error.

Application icons use the public blue WireZTNA `W` mark from `wireztna-web/apple-touch-icon.png`. `branding/app-icon.json` declares that source and the pinned Tauri 2.11.4 icon generator produces PNG, ICNS, and ICO derivatives consumed by macOS and Linux packaging.

Regenerate the derived assets without network access:

```bash
npm run icons
```

Linux packages install the launcher at `/usr/share/applications/wireztna.desktop` and 32/128/256-pixel icons under the freedesktop hicolor hierarchy. Their canonical hashes are verified by the E0 package inspector.

## Contract boundary

- `src/lib/ipc/wire-v2.ts` owns strict snake_case DTOs and exact-object decoders.
- Mutation responses return an `operation_id`; the store waits for the matching terminal lifecycle event and a snapshot proving the result. Connect/switch success additionally requires healthy aggregate state.
- Startup establishes a baseline snapshot and subscribes after its exact stream cursor. `RESYNC_REQUIRED` renegotiates, refreshes the baseline, and resubscribes.
- Controller `desired` and `applied` remain distinct. Project and exit-node choices come from the service catalog and are filtered fail-closed.
- No private key or PSK enters the frontend contract.

## Local validation

```bash
npm test
npm run check
npm run build
```

Rust checks use the toolchain pinned in `rust-toolchain.toml`. The macOS local PKG workflow is documented under `client/wireztna-go/packaging/macos/`; the Linux DEB remains an E0 build-only artifact and makes no installation, runtime, signing, publication, or support claim.
