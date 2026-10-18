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

# The latest release is found from the github.com/<repo>/releases/latest
# redirect, which (unlike the REST API) has no 60-requests-per-hour limit.
# HttpWebRequest behaves the same in Windows PowerShell 5.1 and PowerShell 7.
function Get-LatestTag {
    $url = "https://github.com/$repo/releases/latest"
    $loc = $null
    try {
        $req = [System.Net.HttpWebRequest]::Create($url)
        $req.Method = 'HEAD'
        $req.AllowAutoRedirect = $false
        $req.UserAgent = 'wharfdrop-installer'
        $resp = $req.GetResponse()
        $loc = $resp.Headers['Location']
        $resp.Close()
    } catch [System.Net.WebException] {
        if ($_.Exception.Response) { $loc = $_.Exception.Response.Headers['Location'] }
    }
    if ($loc -match '/releases/tag/(v\d+\.\d+\.\d+)$') { return $Matches[1] }

    # Fallback: follow the redirect and read the final address.
    $r = Invoke-WebRequest -UseBasicParsing -Method Head -Uri $url
    $final = $null
    if ($r.BaseResponse.ResponseUri) { $final = $r.BaseResponse.ResponseUri.AbsoluteUri }                       # PowerShell 5.1
    elseif ($r.BaseResponse.RequestMessage) { $final = $r.BaseResponse.RequestMessage.RequestUri.AbsoluteUri }  # PowerShell 7
    if ($final -match '/releases/tag/(v\d+\.\d+\.\d+)$') { return $Matches[1] }
    throw "Could not find the latest release (redirect: '$loc', final address: '$final')."
}

Write-Host 'Finding the latest Wharfdrop release...'
$tag = Get-LatestTag
Write-Host "Latest release: $tag"

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
