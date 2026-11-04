<#
.SYNOPSIS
  Builds the Lanfolk Windows release: binaries, third-party notices, the
  installer and SHA256SUMS.

.DESCRIPTION
  Steps:
    1. Read the default relay from build.env (or the "ip" key of .env).
    2. Embed the Windows resources of the UI (icon, version, manifest) with
       go-winres when ui\winres\winres.json exists.
    3. Build lanfolk-service, lanfolk-cli, natprobe and lanprobe for
       windows/amd64 with -trimpath, and the desktop UI (./ui) when it exists.
    4. Fetch and verify wintun.dll (scripts\fetch-wintun.ps1).
    5. Generate THIRD_PARTY_NOTICES.md (scripts\gen-notices).
    6. Build the installer (installer\lanfolk.iss) with Inno Setup 6 (ISCC)
       when it is available.
    7. Write a portable zip and SHA256SUMS into the output folder.

  Staging folder: <repository>\build     Output folder: <repository>\dist
  Both are ignored by git.

  The default relay comes from a local, untracked file and is injected with
  -ldflags; it is never written to the repository or printed. build.env
  format (one value per line, lines starting with # are comments):

    LANFOLK_DEFAULT_RELAY_HOST=relay.example.org
    LANFOLK_DEFAULT_RELAY_UDP_PORT=3478
    LANFOLK_DEFAULT_RELAY_TCP_PORT=443

  If build.env does not exist, the "ip" key of the local .env file (the VPS
  credentials file) is used as the host, with the default ports. Only that
  key is read.

  Works in Windows PowerShell 5.1 and PowerShell 7. Needs Go (on PATH).

.PARAMETER Version
  Release version, x.y.z (for example 1.0.0).

.PARAMETER NoDefaultRelay
  Build without a default relay (the user must then configure one). Without
  this switch the build fails when no default relay is configured.

.PARAMETER AllowMissing
  Skip programs whose ./cmd directory does not exist yet (lanfolk-service,
  lanfolk-cli) with a warning instead of failing. The installer is skipped
  then, because it needs every file.

.PARAMETER Iscc
  Path to ISCC.exe (Inno Setup 6.7 or newer). Default: search PATH,
  ~\sdk\innosetup6, Program Files. Install it per user without admin with
  innosetup-6.x.x.exe /VERYSILENT /CURRENTUSER /NORESTART /DIR="%USERPROFILE%\sdk\innosetup6"

.PARAMETER SignScript
  Optional script that signs one file. It is called as
  `& $SignScript -Path <file>` for each Lanfolk .exe before packaging and for
  the installer. wintun.dll is never signed again.

.EXAMPLE
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build-release.ps1 -Version 1.0.0
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Version,
    [switch]$NoDefaultRelay,
    [switch]$AllowMissing,
    [string]$Iscc,
    [string]$SignScript
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

if ($Version -notmatch '^\d+\.\d+\.\d+$') {
    throw "Version must look like 1.2.3, got '$Version'."
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$stage = Join-Path $repoRoot 'build'
$dist = Join-Path $repoRoot 'dist'
$module = 'github.com/NotrinoResearch/lanfolk'
$ver = "$module/internal/version"
# Pinned release of the tool that turns ui\winres\winres.json into .syso files.
$goWinres = 'github.com/tc-hib/go-winres@v0.3.3'

function Invoke-Native {
    param([string]$Exe, [string[]]$Arguments)
    & $Exe @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Exe failed with exit code $LASTEXITCODE."
    }
}

# Reads KEY=VALUE lines. Only the requested keys are returned.
function Read-KeyFile {
    param([string]$Path, [string[]]$Keys)
    $result = @{}
    foreach ($line in Get-Content -LiteralPath $Path) {
        $t = $line.Trim()
        if ($t.Length -eq 0 -or $t.StartsWith('#')) { continue }
        $i = $t.IndexOf('=')
        if ($i -lt 1) { continue }
        $k = $t.Substring(0, $i).Trim()
        if ($Keys -notcontains $k) { continue }
        $v = $t.Substring($i + 1).Trim()
        if ($v.Length -ge 2 -and (($v.StartsWith('"') -and $v.EndsWith('"')) -or ($v.StartsWith("'") -and $v.EndsWith("'")))) {
            $v = $v.Substring(1, $v.Length - 2)
        }
        $result[$k] = $v
    }
    return $result
}

# ---------------------------------------------------------------- tools ----

$go = Get-Command go -ErrorAction SilentlyContinue
if (-not $go) { throw 'go was not found on PATH.' }
Write-Host ("Go: " + (& go version))

# ------------------------------------------------------- default relay ----

$relayHost = ''
$relayUdp = '3478'
$relayTcp = '443'
$buildEnv = Join-Path $repoRoot 'build.env'
$dotEnv = Join-Path $repoRoot '.env'
if (Test-Path -LiteralPath $buildEnv) {
    $kv = Read-KeyFile -Path $buildEnv -Keys @('LANFOLK_DEFAULT_RELAY_HOST', 'LANFOLK_DEFAULT_RELAY_UDP_PORT', 'LANFOLK_DEFAULT_RELAY_TCP_PORT')
    if ($kv.ContainsKey('LANFOLK_DEFAULT_RELAY_HOST')) { $relayHost = $kv['LANFOLK_DEFAULT_RELAY_HOST'] }
    if ($kv.ContainsKey('LANFOLK_DEFAULT_RELAY_UDP_PORT')) { $relayUdp = $kv['LANFOLK_DEFAULT_RELAY_UDP_PORT'] }
    if ($kv.ContainsKey('LANFOLK_DEFAULT_RELAY_TCP_PORT')) { $relayTcp = $kv['LANFOLK_DEFAULT_RELAY_TCP_PORT'] }
    Write-Host 'Default relay settings read from build.env.'
} elseif (Test-Path -LiteralPath $dotEnv) {
    $kv = Read-KeyFile -Path $dotEnv -Keys @('ip')
    if ($kv.ContainsKey('ip')) { $relayHost = $kv['ip'] }
    Write-Host 'Default relay host read from the ip key of .env.'
}
if ($relayHost -eq '') {
    if (-not $NoDefaultRelay) {
        throw ('No default relay configured. Create build.env (see the help of this script) or pass -NoDefaultRelay.')
    }
    Write-Warning 'Building without a default relay.'
} else {
    # The values go into -ldflags, so allow only plain host and port characters.
    if ($relayHost -notmatch '^[A-Za-z0-9.:\-]+$') { throw 'The default relay host contains unexpected characters.' }
    if ($relayUdp -notmatch '^\d{1,5}$') { throw 'The default relay UDP port is not a number.' }
    if ($relayTcp -notmatch '^\d{1,5}$') { throw 'The default relay TCP port is not a number.' }
    Write-Host ("Default relay: configured (UDP port {0}, TCP port {1}); the host is not printed." -f $relayUdp, $relayTcp)
}

$ldflags = "-s -w -X $ver.Version=$Version"
if ($relayHost -ne '') {
    $ldflags += " -X $ver.DefaultRelayHost=$relayHost -X $ver.DefaultRelayUDPPort=$relayUdp -X $ver.DefaultRelayTCPPort=$relayTcp"
}

# ---------------------------------------------------------------- build ----

if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
New-Item -ItemType Directory -Path $stage | Out-Null
if (Test-Path -LiteralPath $dist) { Get-ChildItem -LiteralPath $dist -Force | Remove-Item -Recurse -Force }
else { New-Item -ItemType Directory -Path $dist | Out-Null }

$oldEnv = @{ GOOS = $env:GOOS; GOARCH = $env:GOARCH; CGO_ENABLED = $env:CGO_ENABLED }
$missing = @()
Push-Location $repoRoot
try {
    # Windows resources of the UI (icon, version info, manifest). Runs on the
    # host, so before GOOS/GOARCH are changed. Skipped when not configured yet.
    $winresJson = Join-Path $repoRoot 'ui\winres\winres.json'
    if (Test-Path -LiteralPath $winresJson) {
        Write-Host 'Embedding Windows resources (go-winres)'
        Invoke-Native 'go' @('run', $goWinres, 'make', '--in', 'ui/winres/winres.json', '--out', 'ui/rsrc',
            '--file-version', "$Version.0", '--product-version', "$Version.0")
    } else {
        Write-Host 'ui\winres\winres.json not found; building the UI without embedded resources.'
    }

    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'

    $programs = @(
        @{ Cmd = 'lanfolk-service'; Out = 'lanfolk-service.exe'; Required = $true },
        @{ Cmd = 'lanfolk-cli';     Out = 'lanfolk-cli.exe';     Required = $true },
        @{ Cmd = 'natprobe';        Out = 'natprobe.exe';        Required = $true },
        @{ Cmd = 'lanprobe';        Out = 'lanprobe.exe';        Required = $true }
    )
    foreach ($p in $programs) {
        $dir = Join-Path $repoRoot ("cmd\" + $p.Cmd)
        if (-not (Test-Path -LiteralPath $dir)) {
            if ($AllowMissing) {
                Write-Warning "./cmd/$($p.Cmd) does not exist yet; skipping $($p.Out)."
                $missing += $p.Out
                continue
            }
            throw "./cmd/$($p.Cmd) does not exist. Use -AllowMissing to skip programs that are not written yet."
        }
        Write-Host "Building $($p.Out)"
        Invoke-Native 'go' @('build', '-trimpath', '-ldflags', $ldflags, '-o', (Join-Path $stage $p.Out), "./cmd/$($p.Cmd)")
    }

    if (Test-Path -LiteralPath (Join-Path $repoRoot 'ui')) {
        Write-Host 'Building Lanfolk.exe (desktop UI)'
        $uiFlags = "-H windowsgui -s -w -X $ver.Version=$Version"
        Invoke-Native 'go' @('build', '-trimpath', '-tags', 'desktop,production', '-ldflags', $uiFlags, '-o', (Join-Path $stage 'Lanfolk.exe'), './ui')
    } else {
        Write-Warning './ui does not exist yet; skipping Lanfolk.exe.'
        $missing += 'Lanfolk.exe'
    }
} finally {
    $env:GOOS = $oldEnv['GOOS']
    $env:GOARCH = $oldEnv['GOARCH']
    $env:CGO_ENABLED = $oldEnv['CGO_ENABLED']
    Pop-Location
}

# --------------------------------------------------- wintun and notices ----

& (Join-Path $PSScriptRoot 'fetch-wintun.ps1') -OutDir $stage

Write-Host 'Generating THIRD_PARTY_NOTICES.md'
Push-Location $repoRoot
try {
    Invoke-Native 'go' @('run', './scripts/gen-notices')
} finally {
    Pop-Location
}
Copy-Item -LiteralPath (Join-Path $repoRoot 'THIRD_PARTY_NOTICES.md') -Destination $stage -Force
Copy-Item -LiteralPath (Join-Path $repoRoot 'LICENSE') -Destination $stage -Force

# -------------------------------------------------------------- signing ----

function Invoke-Sign {
    param([string]$Path)
    if ($SignScript) {
        Write-Host "Signing $(Split-Path -Leaf $Path)"
        & $SignScript -Path $Path
    }
}
foreach ($exe in Get-ChildItem -LiteralPath $stage -Filter '*.exe') { Invoke-Sign $exe.FullName }

# ------------------------------------------------------------ installer ----

$installer = Join-Path $dist "Lanfolk-$Version-setup.exe"
if (Test-Path -LiteralPath $installer) { Remove-Item -LiteralPath $installer -Force }

if (-not $Iscc) {
    $cmd = Get-Command ISCC.exe -ErrorAction SilentlyContinue
    if ($cmd) {
        $Iscc = $cmd.Source
    } else {
        $candidates = @()
        if ($env:USERPROFILE) { $candidates += (Join-Path $env:USERPROFILE 'sdk\innosetup6\ISCC.exe') }
        foreach ($pf in @($env:ProgramFiles, ${env:ProgramFiles(x86)})) {
            if ($pf) { $candidates += (Join-Path $pf 'Inno Setup 6\ISCC.exe') }
        }
        foreach ($c in $candidates) {
            if (Test-Path -LiteralPath $c) { $Iscc = $c; break }
        }
    }
}
if ($missing.Count -gt 0) {
    Write-Warning ("Skipping the installer: missing " + ($missing -join ', ') + '.')
} elseif (-not $Iscc) {
    Write-Warning 'ISCC.exe was not found; skipping the installer. Install Inno Setup 6 or pass -Iscc.'
} else {
    Write-Host "Building the installer with $Iscc"
    # The output name (Lanfolk-<version>-setup.exe) is set in installer\lanfolk.iss.
    Invoke-Native $Iscc @('/Qp', "/DVERSION=$Version", "/DBUILD_DIR=$stage", "/O$dist", (Join-Path $repoRoot 'installer\lanfolk.iss'))
    if (-not (Test-Path -LiteralPath $installer)) { throw "ISCC did not produce $installer." }
    Invoke-Sign $installer
}

# ---------------------------------------------------- archive and sums ----

$zip = Join-Path $dist "Lanfolk-$Version-windows-amd64.zip"
if (Test-Path -LiteralPath $zip) { Remove-Item -LiteralPath $zip -Force }
Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $zip -CompressionLevel Optimal

$files = @(Get-ChildItem -LiteralPath $dist -File | Where-Object { $_.Name -ne 'SHA256SUMS' } | Sort-Object Name)
$lines = foreach ($f in $files) {
    $h = (Get-FileHash -LiteralPath $f.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    "$h  $($f.Name)"
}
$sumsPath = Join-Path $dist 'SHA256SUMS'
[IO.File]::WriteAllText($sumsPath, (($lines -join "`n") + "`n"), (New-Object System.Text.UTF8Encoding($false)))

Write-Host ''
Write-Host "Done. Output in $dist :"
Get-Content -LiteralPath $sumsPath | ForEach-Object { Write-Host "  $_" }
if ($missing.Count -gt 0) {
    Write-Warning ("Incomplete release: " + ($missing -join ', ') + ' not built.')
}
