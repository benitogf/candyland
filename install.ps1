# Install the latest candyland release on Windows. Detects arch, downloads the
# matching standalone binary from GitHub releases into %LOCALAPPDATA%\candyland.
# Linux / macOS / WSL: use install.sh.
$ErrorActionPreference = "Stop"

$repo = "benitogf/candyland"
$binary = "candyland.exe"

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }

$release = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
$version = $release.tag_name
if (-not $version) {
    Write-Error "No release found yet for $repo."
    exit 1
}

Write-Host "Installing candyland $version (windows/$arch)..."

$installDir = "$env:LOCALAPPDATA\candyland"
New-Item -ItemType Directory -Force -Path $installDir | Out-Null

$asset = "candyland-windows-$arch.exe"
$url = "https://github.com/$repo/releases/download/$version/$asset"
Invoke-WebRequest -Uri $url -OutFile "$installDir\$binary"

Write-Host "Installed to $installDir\$binary"
if ($env:Path -notlike "*$installDir*") {
    Write-Host "Add it to PATH: setx PATH `"$installDir;`$env:PATH`""
}
Write-Host "Run: candyland   (UI on http://localhost:8080)"
