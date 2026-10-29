# Installs the latest Peeper release in %LOCALAPPDATA%\Peeper, as
# scripts/install.sh does elsewhere: downloads this machine's archive
# (.github/workflows/release.yml), checks it against the release's
# SHA256SUMS, unpacks it beside the old installation and only then swaps the
# two, and adds its bin folder to the user's PATH.
#   irm https://github.com/PeeperLanguage/compiler/releases/latest/download/install.ps1 | iex
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue" # Invoke-WebRequest is slow with it

$baseUrl = "https://github.com/PeeperLanguage/compiler/releases/latest/download"
$installRoot = Join-Path $env:LOCALAPPDATA "Peeper"
if ($env:PROCESSOR_ARCHITECTURE -ne "AMD64") {
    throw "peeper install: no release for windows-$($env:PROCESSOR_ARCHITECTURE.ToLower()) yet: build from source (README.md)"
}
$archive = "peeper-windows-amd64.tar.gz"

# Beside the installation, so the swap below is a rename on one drive.
$work = "$installRoot.install"
Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $work
New-Item -ItemType Directory -Path "$work/peeper" | Out-Null
try {
    Invoke-WebRequest "$baseUrl/SHA256SUMS" -OutFile "$work/SHA256SUMS" -UseBasicParsing
    Write-Host "downloading $archive"
    Invoke-WebRequest "$baseUrl/$archive" -OutFile "$work/$archive" -UseBasicParsing
    $expected = Get-Content "$work/SHA256SUMS" |
        Where-Object { ($_ -split '\s+')[1] -eq $archive } |
        ForEach-Object { ($_ -split '\s+')[0] }
    if (-not $expected) { throw "peeper install: SHA256SUMS has no $archive" }
    $actual = (Get-FileHash -Algorithm SHA256 "$work/$archive").Hash.ToLower()
    if ($actual -ne $expected) { throw "peeper install: $archive is not the file SHA256SUMS names" }

    # tar.exe, which Windows has had since Windows 10 1803.
    tar -xzf "$work/$archive" -C "$work/peeper"
    if ($LASTEXITCODE -ne 0) { throw "peeper install: cannot unpack $archive" }
    if (-not (Test-Path "$work/peeper/bin/peeper.exe")) { throw "peeper install: $archive holds no bin/peeper.exe" }

    $old = "$installRoot.old"
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $old
    if (Test-Path $installRoot) { Move-Item $installRoot $old }
    try {
        Move-Item "$work/peeper" $installRoot
    } catch {
        if (Test-Path $old) { Move-Item $old $installRoot }
        throw "peeper install: cannot move the new installation to ${installRoot}: $_"
    }
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $old
    Write-Host "installed Peeper in $installRoot"
} finally {
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $work
}

$bin = Join-Path $installRoot "bin"
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$entries = @($userPath -split ';' | Where-Object { $_ })
if ($entries -notcontains $bin) {
    [Environment]::SetEnvironmentVariable("Path", (($entries + $bin) -join ';'), "User")
    Write-Host "added $bin to your PATH; open a new terminal to use peeper"
}
if (($env:Path -split ';') -notcontains $bin) { $env:Path = "$bin;$env:Path" }
