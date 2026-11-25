param(
    [Alias("GoExe")][string]$Go = "go",
    [ValidateSet("amd64", "arm64")][string]$Architecture = "amd64",
    [string]$Output = ""
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
if (-not $Output) { $Output = Join-Path $root "dist/windows-client/AIBOT-Client.exe" }
$Output = [IO.Path]::GetFullPath($Output)
New-Item -ItemType Directory -Force (Split-Path -Parent $Output) | Out-Null
$oldOS, $oldArch, $oldCGO = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
Push-Location $root
try {
    # Generate Windows resources with a pinned build tool; no SDK or C compiler is required.
    & $Go run github.com/akavel/rsrc@v0.10.2 -arch $Architecture -manifest cmd/windows-client/app.manifest -ico cmd/windows-client/app.ico -o "cmd/windows-client/resource_windows_$Architecture.syso"
    if ($LASTEXITCODE -ne 0) { throw "Resource generation failed ($LASTEXITCODE)" }
    $env:GOOS = "windows"; $env:GOARCH = $Architecture; $env:CGO_ENABLED = "0"
    & $Go build -trimpath -ldflags "-H=windowsgui -s -w" -o $Output ./cmd/windows-client
    if ($LASTEXITCODE -ne 0) { throw "Windows client build failed ($LASTEXITCODE)" }
    Write-Output "Built $Output"
} finally {
    $env:GOOS = $oldOS; $env:GOARCH = $oldArch; $env:CGO_ENABLED = $oldCGO
    Pop-Location
}
