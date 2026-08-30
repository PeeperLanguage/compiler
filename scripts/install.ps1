# Peeper bootstrap installer: downloads the native installer for the detected
# platform, verifies it against the published SHA256SUMS, runs it, and
# persists the binary directory in the user PATH.
$ErrorActionPreference = "Stop"

$repository = "PeeperLanguage/compiler"
$baseUrl = "https://github.com/$repository/releases/latest/download"

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "peeper install: unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}
$installer = "peeper-installer-windows-$arch.exe"

$work = Join-Path ([System.IO.Path]::GetTempPath()) "peeper-install-$([Guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $work | Out-Null
try {
    $ProgressPreference = 'Continue'
    Invoke-WebRequest "$baseUrl/$installer" -OutFile "$work/$installer"
    $ProgressPreference = 'SilentlyContinue'
    Invoke-WebRequest "$baseUrl/SHA256SUMS" -OutFile "$work/SHA256SUMS"

    $expected = $null
    foreach ($line in Get-Content "$work/SHA256SUMS") {
        if ($line -match "^([0-9a-f]{64})\s+\*?$installer$") { $expected = $Matches[1] }
    }
    if (-not $expected) { throw "peeper install: checksum for $installer not found in SHA256SUMS" }
    $actual = (Get-FileHash "$work/$installer" -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) { throw "peeper install: checksum mismatch for $installer" }

    $output = & "$work/$installer" 2>&1
    $output | ForEach-Object { Write-Host $_ }

    $binDir = $null
    foreach ($line in $output) {
        if ($line -match '^Add (.+) to PATH\.$') { $binDir = $Matches[1] }
    }
    if (-not $binDir) { throw "peeper install: could not determine binary directory" }

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $entries = @()
    if ($userPath) { $entries = $userPath -split ';' | Where-Object { $_ } }
    if ($entries -notcontains $binDir) {
        [Environment]::SetEnvironmentVariable("Path", (($entries + $binDir) -join ';'), "User")
        Write-Host "Added $binDir to user PATH. Restart your terminal to apply."
    }
    if (($env:Path -split ';') -notcontains $binDir) {
        $env:Path = "$binDir;$env:Path"
    }
} finally {
    Remove-Item -Recurse -Force $work
}
