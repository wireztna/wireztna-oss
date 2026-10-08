# WireZTNA Auth

Native WPF enrollment and authentication wizard for WireZTNA 0.9.30. It communicates with the local `wireztna` Windows service over its named pipe, never changes tunnel mode from a status snapshot, and has no external NuGet dependencies.

## Build

Install the .NET SDK version pinned by `global.json`, then run from this directory:

```powershell
dotnet restore
dotnet publish -c Release -r win-x64 --self-contained true
```

The single-file, untrimmed executable is written under `bin\Release\net8.0-windows\win-x64\publish\wireztna-auth.exe`.

Arguments: `--step enroll|login` requests an initial wizard step (reconciled with service status), and `--parent-pid <pid>` closes the wizard when its parent exits.
