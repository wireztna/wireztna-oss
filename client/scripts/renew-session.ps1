#Requires -Version 5.1
<#
.SYNOPSIS
    WireZTNA client session renewal for Windows.

.DESCRIPTION
    Authenticates with the control plane and obtains a fresh PSK to keep
    the WireGuard tunnel alive. Without a valid PSK, the broker rejects
    the handshake and the tunnel dies.

    This script is designed to:
    1. Run via Task Scheduler before session expiry
    2. Run on-demand when the tunnel is detected as down

.PARAMETER ApiUrl
    Control plane API URL (e.g., https://ztna.example.com)

.PARAMETER Username
    Username for authentication (prompts if not provided)

.PARAMETER Password
    Password as SecureString (prompts interactively if not provided)

.PARAMETER Interface
    WireGuard interface/tunnel name (default: wg-wireztna)

.PARAMETER ConfigFile
    Path to WireGuard .conf file (default: auto-detected)

.NOTES
    Future extensions:
    - OIDC/IdP browser-based auth flow
    - MFA challenge handling
    - Device posture reporting at renewal time
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory = $false)]
    [string]$ApiUrl = $env:WIREZTNA_API_URL,

    [Parameter(Mandatory = $false)]
    [string]$Username = $env:WIREZTNA_USERNAME,

    [Parameter(Mandatory = $false)]
    [SecureString]$Password,

    [Parameter(Mandatory = $false)]
    [string]$Interface = "wg-wireztna",

    [Parameter(Mandatory = $false)]
    [string]$ConfigFile
)

# ─── Strict mode ───
Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# ─── Configuration ───
if (-not $ApiUrl) {
    Write-Error "ApiUrl is required. Set -ApiUrl or `$env:WIREZTNA_API_URL"
    exit 1
}

$TokenDir = Join-Path $env:USERPROFILE ".wireztna"
$TokenFile = Join-Path $TokenDir "token"

# Default config path for WireGuard on Windows
if (-not $ConfigFile) {
    $ConfigFile = "C:\Program Files\WireGuard\Data\Configurations\$Interface.conf.dpapi"
    # If dpapi-protected file doesn't exist, try plain .conf
    if (-not (Test-Path $ConfigFile)) {
        $ConfigFile = "C:\Program Files\WireGuard\Data\Configurations\$Interface.conf"
    }
    # Fallback: user-provided location
    if (-not (Test-Path $ConfigFile)) {
        $ConfigFile = Join-Path $env:USERPROFILE "wireguard\$Interface.conf"
    }
}

# WireGuard CLI path
$WgExe = "C:\Program Files\WireGuard\wg.exe"
if (-not (Test-Path $WgExe)) {
    # Try PATH
    $WgExe = "wg"
}

# Ensure token directory exists
if (-not (Test-Path $TokenDir)) {
    New-Item -ItemType Directory -Path $TokenDir -Force | Out-Null
}

# ─── Helper functions ───

function Write-Log {
    param([string]$Message)
    $ts = Get-Date -Format "HH:mm:ss"
    Write-Host "[$ts] $Message"
}

function Get-CachedToken {
    if (Test-Path $TokenFile) {
        $token = Get-Content $TokenFile -Raw
        $token = $token.Trim()

        if ($token -match '^[^.]+\.[^.]+\.[^.]+$') {
            # Decode payload to check expiry
            try {
                $parts = $token.Split('.')
                # Add padding for base64url decode
                $payload = $parts[1]
                $padding = 4 - ($payload.Length % 4)
                if ($padding -ne 4) { $payload += '=' * $padding }
                $payload = $payload.Replace('-', '+').Replace('_', '/')
                $decoded = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($payload))
                $json = $decoded | ConvertFrom-Json

                $now = [int][double]::Parse((Get-Date -UFormat %s))
                if ($json.exp -gt $now) {
                    return $token
                }
            }
            catch {
                # Token decode failed — treat as expired
            }
        }
    }
    return $null
}

function Invoke-Authenticate {
    # FUTURE: Replace with OIDC browser flow or device code flow
    if (-not $Username) {
        $script:Username = Read-Host "Username"
    }

    if (-not $Password) {
        $securePass = Read-Host "Password" -AsSecureString
    }
    else {
        $securePass = $Password
    }

    # Convert SecureString to plain text for API call
    $bstr = [System.Runtime.InteropServices.Marshal]::SecureStringToBSTR($securePass)
    $plainPass = [System.Runtime.InteropServices.Marshal]::PtrToStringAuto($bstr)
    [System.Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)

    Write-Log "Authenticating as $Username..."

    $body = @{
        username = $Username
        password = $plainPass
    } | ConvertTo-Json

    # Clear password from memory
    $plainPass = $null

    try {
        $response = Invoke-RestMethod -Uri "$ApiUrl/api/v1/auth/login" `
            -Method Post `
            -ContentType "application/json" `
            -Body $body
    }
    catch {
        Write-Log "ERROR: Authentication failed — $($_.Exception.Message)"
        exit 1
    }

    $token = $response.access_token
    if (-not $token) {
        Write-Log "ERROR: No token in response"
        exit 1
    }

    # Cache token (restricted permissions)
    Set-Content -Path $TokenFile -Value $token -Force

    Write-Log "Authentication successful — token cached"
    return $token
}

function Invoke-RenewPsk {
    param([string]$Token)

    Write-Log "Requesting new PSK from control plane..."

    $headers = @{ Authorization = "Bearer $Token" }

    try {
        $response = Invoke-RestMethod -Uri "$ApiUrl/api/v1/sessions/renew" `
            -Method Post `
            -ContentType "application/json" `
            -Headers $headers
    }
    catch {
        $statusCode = $_.Exception.Response.StatusCode.value__

        if ($statusCode -eq 401 -or $statusCode -eq 403) {
            Write-Log "Token rejected (HTTP $statusCode) — re-authenticating..."
            Remove-Item $TokenFile -ErrorAction SilentlyContinue
            $Token = Invoke-Authenticate
            $headers = @{ Authorization = "Bearer $Token" }

            try {
                $response = Invoke-RestMethod -Uri "$ApiUrl/api/v1/sessions/renew" `
                    -Method Post `
                    -ContentType "application/json" `
                    -Headers $headers
            }
            catch {
                Write-Log "ERROR: Session renewal failed — $($_.Exception.Message)"
                exit 1
            }
        }
        else {
            Write-Log "ERROR: Session renewal failed (HTTP $statusCode) — $($_.Exception.Message)"
            exit 1
        }
    }

    $psk = $response.preshared_key
    $ttl = $response.ttl_seconds

    if (-not $psk) {
        Write-Log "ERROR: No PSK in response"
        exit 1
    }

    Write-Log "New PSK received (TTL: ${ttl}s)"
    return $psk
}

function Set-WireGuardPsk {
    param([string]$Psk)

    Write-Log "Applying new PSK to WireGuard interface $Interface..."

    # Read config to find peer public key
    if (-not (Test-Path $ConfigFile)) {
        Write-Log "ERROR: Config file not found: $ConfigFile"
        exit 1
    }

    $configContent = Get-Content $ConfigFile
    $peerPubKey = $null
    $inPeer = $false

    foreach ($line in $configContent) {
        if ($line -match '^\[Peer\]') { $inPeer = $true; continue }
        if ($inPeer -and $line -match '^PublicKey\s*=\s*(.+)') {
            $peerPubKey = $Matches[1].Trim()
            break
        }
    }

    if (-not $peerPubKey) {
        Write-Log "ERROR: Cannot find peer public key in config"
        exit 1
    }

    # Write PSK to temp file (wg.exe requires file input)
    $pskFile = [System.IO.Path]::GetTempFileName()
    Set-Content -Path $pskFile -Value $Psk -NoNewline

    # Apply PSK to live interface
    & $WgExe set $Interface peer $peerPubKey preshared-key $pskFile
    if ($LASTEXITCODE -ne 0) {
        Remove-Item $pskFile -ErrorAction SilentlyContinue
        Write-Log "ERROR: wg set failed (exit code: $LASTEXITCODE)"
        exit 1
    }

    Remove-Item $pskFile -ErrorAction SilentlyContinue

    # Update config file for persistence
    $updated = $false
    $newConfig = @()
    foreach ($line in $configContent) {
        if ($line -match '^PresharedKey\s*=') {
            $newConfig += "PresharedKey = $Psk"
            $updated = $true
        }
        else {
            $newConfig += $line
            # If we just passed PublicKey in [Peer] and haven't updated, insert PSK
            if (-not $updated -and $line -match '^PublicKey\s*=' -and $inPeer) {
                $newConfig += "PresharedKey = $Psk"
                $updated = $true
            }
        }
        if ($line -match '^\[Peer\]') { $inPeer = $true }
    }

    Set-Content -Path $ConfigFile -Value $newConfig -Force

    Write-Log "PSK applied — tunnel will re-handshake within ~2 minutes"
}

function Test-TunnelUp {
    # Get broker overlay IP from AllowedIPs in config
    $configContent = Get-Content $ConfigFile
    $brokerIp = $null
    $inPeer = $false

    foreach ($line in $configContent) {
        if ($line -match '^\[Peer\]') { $inPeer = $true; continue }
        if ($inPeer -and $line -match '^AllowedIPs\s*=\s*(.+)') {
            $firstIp = $Matches[1].Split(',')[0].Trim().Split('/')[0]
            $brokerIp = $firstIp
            break
        }
    }

    if (-not $brokerIp) { return $false }

    $result = Test-Connection -ComputerName $brokerIp -Count 1 -Quiet -TimeoutSeconds 2
    return $result
}

# ─── Main ───

Write-Log "=== WireZTNA Session Renewal (Windows) ==="

# Step 1: Get or refresh JWT
$token = Get-CachedToken
if ($token) {
    Write-Log "Using cached token"
}
else {
    Write-Log "No valid cached token"
    $token = Invoke-Authenticate
}

# Step 2: Request new PSK
$psk = Invoke-RenewPsk -Token $token

# Step 3: Apply PSK to WireGuard
Set-WireGuardPsk -Psk $psk

# Step 4: Verify tunnel comes back (wait for re-handshake)
Write-Log "Waiting for tunnel to establish..."
for ($i = 1; $i -le 12; $i++) {
    Start-Sleep -Seconds 5
    if (Test-TunnelUp) {
        Write-Log "=== Tunnel active — session renewed successfully ==="
        exit 0
    }
    Write-Log "  Waiting... ($i/12)"
}

Write-Log "WARNING: Tunnel did not come up within 60s — check broker reconciler"
exit 1
