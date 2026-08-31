# Peeper bootstrap installer: downloads the release manifest, verifies it
# against the published SHA256SUMS, downloads the compiler and toolchain packs
# for the detected platform, verifies every SHA-256, extracts into a staging
# directory, activates atomically, and persists the binary directory in the
# user PATH.
$ErrorActionPreference = "Stop"

$repository = "PeeperLanguage/compiler"
$baseUrl = "https://github.com/$repository/releases/latest/download"

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "peeper install: unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

$work = Join-Path ([System.IO.Path]::GetTempPath()) "peeper-install-$([Guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $work | Out-Null
try {
    Invoke-WebRequest "$baseUrl/SHA256SUMS" -OutFile "$work/SHA256SUMS"
    $ProgressPreference = 'Continue'
    Invoke-WebRequest "$baseUrl/release-manifest.json" -OutFile "$work/release-manifest.json"
    $ProgressPreference = 'SilentlyContinue'

    $expected = $null
    foreach ($line in Get-Content "$work/SHA256SUMS") {
        if ($line -match "^([0-9a-f]{64})\s+\*?release-manifest\.json$") { $expected = $Matches[1] }
    }
    if (-not $expected) { throw "peeper install: checksum for release-manifest.json not found in SHA256SUMS" }
    $actual = (Get-FileHash "$work/release-manifest.json" -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) { throw "peeper install: release manifest checksum mismatch" }

    $manifest = Get-Content "$work/release-manifest.json" -Raw | ConvertFrom-Json
    $components = $manifest.components | Where-Object { $_.os -eq "windows" -and $_.arch -eq $arch }
    $compiler = $components | Where-Object { $_.kind -eq "compiler" }
    $toolchain = $components | Where-Object { $_.kind -eq "toolchain" }
    if (-not $compiler -or -not $toolchain) { throw "peeper install: release manifest has no complete component set for windows/$arch" }

    function Download-Component($component, $output) {
        if (-not $component.url.StartsWith("https://")) { throw "peeper install: component URL is not HTTPS: $($component.url)" }
        $ProgressPreference = 'Continue'
        Invoke-WebRequest $component.url -OutFile $output
        $ProgressPreference = 'SilentlyContinue'
        $actual = (Get-FileHash $output -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $component.sha256) { throw "peeper install: component checksum mismatch: $output" }
    }

    Download-Component $compiler "$work/compiler.zip"
    Download-Component $toolchain "$work/toolchain.zip"

    # Windows PowerShell 5.1 Expand-Archive fails when extracting into a
    # non-empty destination, so extract each archive into its own directory
    # and merge into staging afterwards.
    $staging = Join-Path $work "staging"
    New-Item -ItemType Directory -Path $staging | Out-Null
    foreach ($pair in @(@("compiler.zip", "compiler"), @("toolchain.zip", "toolchain"))) {
        $extractDir = Join-Path $work $pair[1]
        Expand-Archive "$work/$($pair[0])" -DestinationPath $extractDir
        Copy-Item -Recurse -Force "$extractDir/*" $staging
    }
    if (-not (Test-Path (Join-Path $staging "bin\peeper.exe"))) { throw "peeper install: staged installation has no peeper executable" }
    if (-not (Test-Path (Join-Path $staging "toolchains\native\profile.json"))) { throw "peeper install: staged installation has no managed toolchain profile" }

    $installRoot = if ($env:LOCALAPPDATA) { Join-Path $env:LOCALAPPDATA "Peeper" } else { Join-Path $HOME ".peeper" }
    $backup = "$installRoot.old"
    if (Test-Path $installRoot) {
        if (Test-Path $backup) { Remove-Item -Recurse -Force $backup }
        Move-Item $installRoot $backup
    }
    try {
        Move-Item $staging $installRoot
        if (Test-Path $backup) { Remove-Item -Recurse -Force $backup }
    } catch {
        if (Test-Path $backup) { Move-Item $backup $installRoot }
        throw "peeper install: could not activate installation at $installRoot"
    }

    Write-Host "Installed Peeper $($manifest.version) in $installRoot"
    Write-Host "Add $(Join-Path $installRoot 'bin') to PATH."

    $binDir = Join-Path $installRoot "bin"
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
