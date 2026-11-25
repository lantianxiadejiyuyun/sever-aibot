param(
    [string]$GoExe = "go",
    [ValidateSet("amd64", "arm64")][string]$Architecture = "amd64",
    [ValidatePattern('^[a-zA-Z0-9.-]*$')][string]$ReleaseTag = ""
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$suffix = if ($ReleaseTag) { "-$ReleaseTag" } else { "" }
$directory = Join-Path $root "dist/windows-client$suffix"
& (Join-Path $PSScriptRoot "build-windows-client.ps1") -GoExe $GoExe -Architecture $Architecture -Output (Join-Path $directory "AIBOT-Client.exe")
Copy-Item (Join-Path $root "docs/Windows客户端.md") (Join-Path $directory "使用说明.md") -Force
$executable = Join-Path $directory "AIBOT-Client.exe"
$hash = (Get-FileHash $executable -Algorithm SHA256).Hash.ToLowerInvariant()
[IO.File]::WriteAllText((Join-Path $directory "SHA256SUMS.txt"), "$hash  AIBOT-Client.exe`n", [Text.UTF8Encoding]::new($false))
$archive = Join-Path $root "dist/AIBOT-Client-windows-$Architecture$suffix.zip"
Compress-Archive -Path $executable, (Join-Path $directory "使用说明.md"), (Join-Path $directory "SHA256SUMS.txt") -DestinationPath $archive -Force
Write-Output "Package: $archive"
Write-Output "SHA256: $hash"
