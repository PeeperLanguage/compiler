# Sets up this checkout for compiler development on Windows, as
# scripts/setup.sh does elsewhere: puts the LLVM toolchain peeper runs in
# build\toolchains\native. The first way that works:
#   1. the pack CI built for this machine, pinned in
#      toolchains\toolchains.lock.json, downloaded and checked;
#   2. an installed LLVM 23 or newer, with clang and ld.lld side by
#      side, linked in as a directory junction (a symlink needs admin rights).
#      With none, it offers to install the llvm-mingw release pinned in
#      toolchains\toolchain-sources.lock.json into %LOCALAPPDATA%\peeper-dev.
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue" # Invoke-WebRequest is slow with it
Set-Location (Join-Path $PSScriptRoot "..")

$minimum = 23 # LLVM 22 crashes on float to wide integer casts (docs\ir-design.md)
$native = "build\toolchains\native"
$stamp = "build\toolchains\pack-id" # the CI pack in $native, so a rerun skips it
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "unsupported machine $env:PROCESSOR_ARCHITECTURE" }
}

# Downloads the lock entry's file to $file; gives whether its size and
# sha256 are the ones the lock pins.
function Get-Pinned($entry, $file) {
    try {
        Invoke-WebRequest -Uri $entry.url -OutFile $file -UseBasicParsing
    } catch {
        Write-Host "cannot download $($entry.url): $_"
        return $false
    }
    $hash = (Get-FileHash -Algorithm SHA256 $file).Hash.ToLower()
    return ((Get-Item $file).Length -eq $entry.size) -and ($hash -eq $entry.sha256)
}

function Remove-Toolchain {
    if (Test-Path $native) {
        $item = Get-Item $native
        if ($item.LinkType -eq "Junction") {
            $item.Delete() # only the link: the LLVM it points to is the user's
        } else {
            Remove-Item -Recurse -Force $native
        }
    }
    Remove-Item -Force -ErrorAction SilentlyContinue $stamp
}

# Way 1: unpacks the CI pack for this machine into $native.
function Use-Pack {
    $lock = Get-Content -Raw "toolchains\toolchains.lock.json" | ConvertFrom-Json
    $entry = $lock.toolchains | Where-Object { $_.os -eq "windows" -and $_.arch -eq $arch }
    if (-not $entry) {
        Write-Host "no CI pack for windows-$arch"
        return $false
    }
    $isJunction = (Test-Path $native) -and ((Get-Item $native).LinkType -eq "Junction")
    # A rerun keeps a pack it already unpacked; a junction (way 2) is replaced.
    if ((Test-Path $native) -and -not $isJunction -and (Test-Path $stamp) -and
        ((Get-Content -Raw $stamp).Trim() -eq $entry.id)) {
        Write-Host "build\toolchains\native already holds $($entry.id)"
        return $true
    }
    $work = "build\.setup"
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $work
    New-Item -ItemType Directory -Force $work | Out-Null
    Write-Host "downloading $($entry.id)"
    if (-not (Get-Pinned $entry "$work\pack")) {
        Write-Host "cannot download $($entry.id), or it is not the file the lock pins"
        Remove-Item -Recurse -Force $work
        return $false
    }
    # Windows' own tar reads a tar.gz pack and the zip packs before them; a
    # GNU tar on the PATH would take `C:` for another machine.
    & "$env:SystemRoot\System32\tar.exe" -xf "$work\pack" -C $work
    if ($LASTEXITCODE -ne 0) { throw "cannot unpack $($entry.id)" }
    # The old toolchain goes only once the new one is all there.
    $tools = "$work\toolchains\native\bin"
    if (-not ((Test-Path "$tools\clang.exe") -and (Test-Path "$tools\ld.lld.exe"))) {
        throw "$($entry.id) holds no toolchains\native with clang and ld.lld"
    }
    Remove-Toolchain
    New-Item -ItemType Directory -Force "build\toolchains" | Out-Null
    Move-Item "$work\toolchains\native" $native
    Set-Content -NoNewline $stamp $entry.id
    Remove-Item -Recurse -Force $work
    Write-Host "build\toolchains\native holds $($entry.id)"
    return $true
}

# Way 2: the folder of an installed LLVM $minimum or newer, the one whose bin
# holds clang and ld.lld; $null when there is none.
function Find-InstalledLlvm {
    $clangs = @(Get-Command clang.exe -All -CommandType Application -ErrorAction SilentlyContinue |
        ForEach-Object { $_.Source })
    $clangs += @(Get-ChildItem -ErrorAction SilentlyContinue "$env:LOCALAPPDATA\peeper-dev\*\bin\clang.exe" |
        ForEach-Object { $_.FullName })
    foreach ($clang in $clangs) {
        $bin = Split-Path $clang
        if (-not (Test-Path "$bin\ld.lld.exe")) { continue }
        $version = & $clang -dumpversion
        if ([int]($version.Split(".")[0]) -lt $minimum) { continue }
        return (Split-Path $bin)
    }
    return $null
}

# Asks to install the llvm-mingw release pinned for this machine into
# %LOCALAPPDATA%\peeper-dev; gives whether it did.
function Install-Llvm {
    $lock = Get-Content -Raw "toolchains\toolchain-sources.lock.json" | ConvertFrom-Json
    $entry = $lock.assets | Where-Object { $_.id -eq "llvm-mingw-windows-$arch" }
    if (-not $entry) {
        Write-Host "no LLVM release is pinned for windows-$arch"
        return $false
    }
    $folder = "$env:LOCALAPPDATA\peeper-dev\llvm-mingw-$($entry.version)"
    $answer = Read-Host "No LLVM $minimum or newer found. Install llvm-mingw $($entry.version) into $folder? [y/N]"
    if ($answer -ne "y") { return $false }
    $root = "$env:LOCALAPPDATA\peeper-dev"
    New-Item -ItemType Directory -Force $root | Out-Null
    $archive = "$root\llvm-mingw-$($entry.version).zip"
    Write-Host "downloading llvm-mingw $($entry.version)"
    if (-not (Get-Pinned $entry $archive)) {
        Write-Host "cannot download llvm-mingw $($entry.version), or it is not the file the lock pins"
        Remove-Item -Force -ErrorAction SilentlyContinue $archive
        return $false
    }
    $part = "$folder.part"
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $part
    Expand-Archive -Path $archive -DestinationPath $part
    Remove-Item -Force $archive
    # The zip holds one folder named after the release.
    Move-Item (Get-ChildItem $part | Select-Object -First 1).FullName $folder
    Remove-Item -Recurse -Force $part
    return $true
}

if (Use-Pack) { exit 0 }
Write-Host "trying an installed LLVM instead"
$llvm = Find-InstalledLlvm
if (-not $llvm -and (Install-Llvm)) { $llvm = Find-InstalledLlvm }
if ($llvm) {
    Remove-Toolchain
    New-Item -ItemType Directory -Force "build\toolchains" | Out-Null
    New-Item -ItemType Junction -Path $native -Target $llvm | Out-Null
    Write-Host "build\toolchains\native links $llvm"
    exit 0
}
Write-Error "no toolchain: install LLVM $minimum or newer with clang and ld.lld, then run this again"
exit 1
