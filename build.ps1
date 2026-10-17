# Build Wharfdrop on Windows without make.
#
#   powershell -ExecutionPolicy Bypass -File build.ps1          # test + build bin\wharfdrop.exe
#   powershell -ExecutionPolicy Bypass -File build.ps1 -NoTest  # build only
#
# Needs Go 1.22+ (https://go.dev/dl/ or: winget install GoLang.Go).
param([switch]$NoTest)
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw 'Go is not installed. Install it from https://go.dev/dl/ or with: winget install GoLang.Go  (then open a new terminal)'
}
if (-not $NoTest) {
    go test -count=1 ./...
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
}
New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -o bin\wharfdrop.exe ./cmd/wharfdrop
if ($LASTEXITCODE -ne 0) { throw 'Build failed.' }
Write-Host "Built $(Resolve-Path bin\wharfdrop.exe)"
Write-Host 'Run it with: .\bin\wharfdrop.exe ui    (or double-click it)'
