<#
.SYNOPSIS
  Downloads the signed Wintun 0.14.1 release, verifies its SHA-256 and
  extracts wintun.dll (amd64) and its license into the build folder.

.DESCRIPTION
  Source: https://www.wintun.net/builds/wintun-0.14.1.zip
  The expected SHA-256 of the zip is fixed in this script. If the download
  does not match, the script stops with an error and writes nothing to the
  output folder. Never change the expected hash to whatever was downloaded:
  find out why it differs first.

  Output files:
    <OutDir>\wintun.dll          amd64 DLL, unmodified, signed by WireGuard LLC
    <OutDir>\wintun-LICENSE.txt  Prebuilt Binaries License (LICENSE.txt in the zip)

  Works in Windows PowerShell 5.1 and PowerShell 7.

.PARAMETER OutDir
  Output folder. Default: <repository>\build

.PARAMETER Force
  Download again even if the output files already exist.

.EXAMPLE
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\fetch-wintun.ps1
#>
[CmdletBinding()]
param(
    [string]$OutDir,
    [switch]$Force
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$WintunVersion = '0.14.1'
$WintunUrl = "https://www.wintun.net/builds/wintun-$WintunVersion.zip"
$ExpectedSha256 = '07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51'

$repoRoot = Split-Path -Parent $PSScriptRoot
if (-not $OutDir) { $OutDir = Join-Path $repoRoot 'build' }
$dllOut = Join-Path $OutDir 'wintun.dll'
$licOut = Join-Path $OutDir 'wintun-LICENSE.txt'

if ((-not $Force) -and (Test-Path -LiteralPath $dllOut) -and (Test-Path -LiteralPath $licOut)) {
    Write-Host "wintun.dll and its license already exist in $OutDir (use -Force to download again)."
    return
}

# Windows PowerShell 5.1 does not enable TLS 1.2 by default.
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
    # Not available on this platform; the default is fine.
}

$work = Join-Path ([IO.Path]::GetTempPath()) ('lanfolk-wintun-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    $zip = Join-Path $work "wintun-$WintunVersion.zip"
    Write-Host "Downloading $WintunUrl"
    $oldProgress = $ProgressPreference
    $ProgressPreference = 'SilentlyContinue'
    try {
        Invoke-WebRequest -Uri $WintunUrl -OutFile $zip -UseBasicParsing
    } finally {
        $ProgressPreference = $oldProgress
    }

    $actual = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $ExpectedSha256) {
        throw ("SHA-256 mismatch for $WintunUrl`n  expected $ExpectedSha256`n  actual   $actual`n" +
               "Nothing was written. Do not continue until you know why the file differs.")
    }
    Write-Host "SHA-256 verified: $actual"

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [IO.Compression.ZipFile]::OpenRead($zip)
    try {
        New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
        $wanted = @(
            @{ Entry = 'wintun/bin/amd64/wintun.dll'; Target = $dllOut },
            @{ Entry = 'wintun/LICENSE.txt';          Target = $licOut }
        )
        foreach ($w in $wanted) {
            $entry = $archive.GetEntry($w.Entry)
            if ($null -eq $entry) { throw "The zip has no entry $($w.Entry)" }
            [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $w.Target, $true)
        }
    } finally {
        $archive.Dispose()
    }
} finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}

# The DLL must be the one signed by WireGuard LLC; it is never rebuilt.
$sig = Get-AuthenticodeSignature -LiteralPath $dllOut
$signer = 'no signer certificate'
if ($null -ne $sig.SignerCertificate) { $signer = $sig.SignerCertificate.Subject }
Write-Host ("wintun.dll signature: {0} ({1})" -f $sig.Status, $signer)
if ($sig.Status -eq 'NotSigned' -or $sig.Status -eq 'HashMismatch') {
    Remove-Item -LiteralPath $dllOut -Force
    throw "wintun.dll is not validly signed ($($sig.Status)); it was removed."
}

Write-Host "Wrote $dllOut"
Write-Host "Wrote $licOut"
