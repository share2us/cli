# Share2Us CLI installer for Windows (PowerShell).
#
#   irm https://share2.us/install.ps1 | iex
#
# Mirrors install.sh: GitHub Releases is the source of truth (CI builds every
# platform); the hosted mirror on share2.us/downloads is a fallback. Integrity
# is checked against the .sha256 sidecar (Windows has no cksum).

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Repo       = if ($env:SHARE2US_INSTALL_REPO)     { $env:SHARE2US_INSTALL_REPO }     else { 'share2us/cli' }
$Version    = if ($env:SHARE2US_VERSION)          { $env:SHARE2US_VERSION }          else { 'latest' }
$BaseUrl    = if ($env:SHARE2US_INSTALL_BASE_URL) { $env:SHARE2US_INSTALL_BASE_URL }  else { 'https://share2.us' }
$InstallDir = if ($env:SHARE2US_INSTALL_DIR)      { $env:SHARE2US_INSTALL_DIR }      else { Join-Path $env:LOCALAPPDATA 'Share2Us\bin' }
$BinaryName = 'share2us.exe'
$AliasName  = 's2u.exe'

function Fail($msg) { Write-Error "share2us install: $msg"; exit 1 }
function Log($msg)  { Write-Host $msg }

function Get-Arch {
  switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { Fail "unsupported architecture: $($env:PROCESSOR_ARCHITECTURE)" }
  }
}

function Download($url, $dest) {
  # -UseBasicParsing keeps this working on Windows PowerShell 5.1 without IE.
  Invoke-WebRequest -Uri $url -OutFile $dest -UseBasicParsing -Headers @{ 'User-Agent' = 'share2us-ps-installer' }
}

# Try the archive and its .sha256 sidecar from each URL in turn; verify before
# accepting. Returns $true on the first verified download.
function Download-Verified($dest, $sidecar, [string[]]$urls) {
  foreach ($url in $urls) {
    Log "Trying $url"
    try {
      Download $url $dest
      Download "$url.sha256" $sidecar
    } catch {
      continue
    }
    $want = (Get-Content -Raw $sidecar).Trim().Split()[0]
    $got  = (Get-FileHash -Algorithm SHA256 -LiteralPath $dest).Hash
    if ($got -ieq $want) {
      Log "SHA-256 check passed for $(Split-Path -Leaf $dest)"
      return $true
    }
    Fail "SHA-256 check failed for $(Split-Path -Leaf $dest)"
  }
  return $false
}

$arch    = Get-Arch
$archive = "share2us_windows_${arch}.zip"

if ($Version -eq 'latest') {
  $hostedUrl = "$($BaseUrl.TrimEnd('/'))/downloads/$archive"
  $githubUrl = "https://github.com/$Repo/releases/latest/download/$archive"
} else {
  $hostedUrl = "$($BaseUrl.TrimEnd('/'))/downloads/$Version/$archive"
  $githubUrl = "https://github.com/$Repo/releases/download/$Version/$archive"
}

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("s2u-install-" + [System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
try {
  $archivePath = Join-Path $tmp $archive
  $sidecar     = "$archivePath.sha256"

  Log "Downloading Share2Us CLI for windows/$arch..."
  if (-not (Download-Verified $archivePath $sidecar @($githubUrl, $hostedUrl))) {
    Fail "could not download and verify $archive"
  }

  Expand-Archive -Force -LiteralPath $archivePath -DestinationPath $tmp
  $src = Get-ChildItem -Path $tmp -Recurse -Filter $BinaryName | Select-Object -First 1
  if (-not $src) { Fail "archive did not contain $BinaryName" }

  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  Copy-Item -Force -LiteralPath $src.FullName -Destination (Join-Path $InstallDir $BinaryName)
  # Windows has no cheap symlink; ship s2u.exe as a copy of the same binary.
  Copy-Item -Force -LiteralPath $src.FullName -Destination (Join-Path $InstallDir $AliasName)
} finally {
  Remove-Item -Recurse -Force -LiteralPath $tmp -ErrorAction SilentlyContinue
}

# Put InstallDir on the user's PATH (persisted) if it is not already there.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$onPath = ($userPath -split ';') -contains $InstallDir
if (-not $onPath) {
  $newPath = if ([string]::IsNullOrEmpty($userPath)) { $InstallDir } else { "$userPath;$InstallDir" }
  [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
  # Make it usable in THIS session too.
  $env:Path = "$env:Path;$InstallDir"
}

Log ""
Log "Installed the Share2Us CLI to $InstallDir:"
Log "  s2u          <- short command (use this)"
Log "  share2us     (same tool, long name)"
if (-not $onPath) {
  Log ""
  Log "Added $InstallDir to your PATH. Open a new terminal (or restart the current"
  Log "one) so the s2u command is found."
}
Log ""
Log "Get started:  s2u login"
