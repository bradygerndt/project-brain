#Requires -Version 5.1
# project-brain installer for native Windows (PowerShell). Not for WSL —
# WSL presents as Linux, so it uses install.sh instead (see the README).
$ErrorActionPreference = "Stop"

$Repo = "bradygerndt/project-brain"
$BinDir = if ($env:BRAIN_BIN_DIR) { $env:BRAIN_BIN_DIR } else { Join-Path $env:LOCALAPPDATA "brain" }
$BrainExe = Join-Path $BinDir "brain.exe"

Write-Host ""
Write-Host "  project-brain installer" -ForegroundColor White
Write-Host ""

# -- Detect architecture --------------------------------------------------
$Arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "Unsupported architecture: $($env:PROCESSOR_ARCHITECTURE). See https://github.com/$Repo/releases for manual builds." }
}
Write-Host "-> Detected windows/$Arch" -ForegroundColor Cyan

# -- Resolve latest release ------------------------------------------------
Write-Host "-> Looking up latest release..." -ForegroundColor Cyan
$Release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
$Tag = $Release.tag_name
if (-not $Tag) { throw "Couldn't resolve the latest release. Check https://github.com/$Repo/releases" }
Write-Host "-> Latest release: $Tag" -ForegroundColor Cyan

# -- Download the matching binary ------------------------------------------
$Asset = "brain_windows_$Arch.zip"
$Url = "https://github.com/$Repo/releases/download/$Tag/$Asset"
$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $TmpDir | Out-Null

try {
    Write-Host "-> Downloading $Asset..." -ForegroundColor Cyan
    $ZipPath = Join-Path $TmpDir $Asset
    Invoke-WebRequest -Uri $Url -OutFile $ZipPath
    Expand-Archive -Path $ZipPath -DestinationPath $TmpDir -Force

    New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    Copy-Item -Path (Join-Path $TmpDir "brain.exe") -Destination $BrainExe -Force
    Write-Host "OK brain -> $BrainExe" -ForegroundColor Green
} finally {
    Remove-Item -Recurse -Force $TmpDir -ErrorAction SilentlyContinue
}

# Sanity check
& $BrainExe version | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Installed binary failed to run - see $BrainExe" }

# -- PATH -------------------------------------------------------------------
$UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (($UserPath -split ";") -notcontains $BinDir) {
    [Environment]::SetEnvironmentVariable("Path", "$UserPath;$BinDir", "User")
    Write-Host "OK Added $BinDir to your user PATH" -ForegroundColor Green
    Write-Host "!  Open a new terminal for it to take effect" -ForegroundColor Yellow
} else {
    Write-Host "OK $BinDir is already in your PATH" -ForegroundColor Green
}

# -- %APPDATA%\brain\.env ----------------------------------------------------
$ConfigDir = if ($env:BRAIN_CONFIG_DIR) { $env:BRAIN_CONFIG_DIR } else { Join-Path $env:APPDATA "brain" }
$EnvFile = Join-Path $ConfigDir ".env"
if (-not (Test-Path $EnvFile)) {
    New-Item -ItemType Directory -Path $ConfigDir -Force | Out-Null
    Set-Content -Path $EnvFile -Value "ANTHROPIC_API_KEY=" -NoNewline
    Write-Host "!  .env created - edit $EnvFile and add your ANTHROPIC_API_KEY" -ForegroundColor Yellow
    Write-Host "!  (only needed for the memory_extract tool)" -ForegroundColor Yellow
} else {
    Write-Host "OK .env exists" -ForegroundColor Green
}

# -- Docker -------------------------------------------------------------------
if (Get-Command docker -ErrorAction SilentlyContinue) {
    $DockerVer = (docker --version) -replace '[^\d.]*(\d+\.\d+\.\d+).*', '$1'
    Write-Host "OK Docker $DockerVer" -ForegroundColor Green
} else {
    Write-Host "!  Docker not found" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "   Install Docker Desktop: https://docs.docker.com/desktop/install/windows-install/"
    Write-Host ""
}

# -- Done -----------------------------------------------------------------
Write-Host ""
Write-Host "OK Installation complete!" -ForegroundColor Green
Write-Host ""
Write-Host "  Next steps:"
Write-Host "    brain help                  see all commands"
Write-Host "    brain ps                    check instance status"
Write-Host "    brain start                 start all instances (requires Docker)"
Write-Host "    brain config                get MCP config for Claude Code"
Write-Host ""
