# Wharfdrop installer for Windows.
#
#   irm https://raw.githubusercontent.com/Kumarsujay03/wharfdrop/main/install.ps1 | iex
#
# Downloads the latest release, checks its SHA-256 against the release's
# SHA256SUMS, installs it to %LOCALAPPDATA%\Programs\Wharfdrop, adds that folder
# to your user PATH and creates a Start menu shortcut. It does NOT make
# Wharfdrop start at login. To uninstall, set $env:WHARFDROP_UNINSTALL=1 first.
#
# Optional: $env:WHARFDROP_NO_LAUNCH=1 to skip opening Wharfdrop afterwards.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'   # Invoke-WebRequest is very slow with the progress bar
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$repo     = 'Kumarsujay03/wharfdrop'
$dir      = Join-Path $env:LOCALAPPDATA 'Programs\Wharfdrop'
$exe      = Join-Path $dir 'wharfdrop.exe'
$shortcut = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Wharfdrop.lnk'

function Remove-FromUserPath($folder) {
    $p = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($p) {
        $new = ($p -split ';' | Where-Object { $_ -and ($_.TrimEnd('\') -ne $folder.TrimEnd('\')) }) -join ';'
        [Environment]::SetEnvironmentVariable('Path', $new, 'User')
    }
}

if (Test-Path $exe) {
    # stop a running copy so the file can be replaced
    try { & $exe stop --force | Out-Null } catch {}
}

if ($env:WHARFDROP_UNINSTALL) {
    Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue
    Remove-Item -Force $shortcut -ErrorAction SilentlyContinue
    Remove-FromUserPath $dir
    Write-Host 'Wharfdrop removed. Your settings and keys are still in' (Join-Path $env:APPDATA 'wharfdrop')
    return
}

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }

Write-Host 'Finding the latest Wharfdrop release...'
$tag = $null
try {
    $resp = Invoke-WebRequest -UseBasicParsing -Uri "https://github.com/$repo/releases/latest" -MaximumRedirection 0 -ErrorAction Stop
    $loc = $resp.Headers.Location
} catch {
    $loc = $_.Exception.Response.Headers.Location
    if ($loc -and $loc.GetType().Name -eq 'Uri') { $loc = $loc.ToString() }
}
if ($loc -match '/releases/tag/(v\d+\.\d+\.\d+)$') { $tag = $Matches[1] }
if (-not $tag) {
    # fall back to the API
    $tag = (Invoke-RestMethod -UseBasicParsing "https://api.github.com/repos/$repo/releases/latest").tag_name
}
if (-not $tag) { throw 'Could not find the latest release.' }

$asset = "wharfdrop-$tag-windows-$arch.exe"
$base  = "https://github.com/$repo/releases/download/$tag"
New-Item -ItemType Directory -Force $dir | Out-Null
$tmp = Join-Path $dir 'wharfdrop.download'

Write-Host "Downloading $asset ..."
Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $tmp
$sums = (Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS").Content
if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
$line = ($sums -split "`n") | Where-Object { $_ -match ([regex]::Escape($asset) + '\s*$') } | Select-Object -First 1
if (-not $line) { Remove-Item $tmp; throw "No checksum for $asset in the release." }
$want = ($line -split '\s+')[0].ToLower()
$got  = (Get-FileHash -Algorithm SHA256 $tmp).Hash.ToLower()
if ($want -ne $got) { Remove-Item $tmp; throw "Checksum mismatch for $asset. Not installing." }

Move-Item -Force $tmp $exe
Unblock-File $exe -ErrorAction SilentlyContinue   # avoid the SmartScreen "downloaded from the internet" block

# add to the user PATH (no admin needed)
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not (($userPath -split ';') | Where-Object { $_.TrimEnd('\') -eq $dir })) {
    [Environment]::SetEnvironmentVariable('Path', (($userPath, $dir) | Where-Object { $_ }) -join ';', 'User')
}
$env:Path = "$env:Path;$dir"

# Start menu shortcut (launching it starts Wharfdrop and opens the web UI)
$ws = New-Object -ComObject WScript.Shell
$lnk = $ws.CreateShortcut($shortcut)
$lnk.TargetPath = $exe
$lnk.WorkingDirectory = $dir
$lnk.Description = 'Secure file sharing on your local network'
$lnk.Save()

Write-Host ""
Write-Host "Wharfdrop $tag installed to $exe"
Write-Host "Open it from the Start menu (Wharfdrop), or run: wharfdrop ui"
Write-Host "Stop it from the web page or with: wharfdrop stop"
Write-Host "When Windows Firewall asks, allow Wharfdrop on Private networks."

if (-not $env:WHARFDROP_NO_LAUNCH) {
    & $exe ui
}
