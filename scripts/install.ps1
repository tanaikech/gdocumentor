# ==============================================================================
# DocuMentor AI (gdocumentor) — Windows PowerShell One-Liner Installer
# Repository: https://github.com/tanaikech/gdocumentor
#
# Usage Examples (PowerShell):
#   # Install gdocumentor.exe + configure ALL AI agents (Claude Desktop, Claude Code, Cursor, VS Code/Copilot, Windsurf, Gemini CLI, Antigravity)
#   irm https://raw.githubusercontent.com/tanaikech/gdocumentor/main/scripts/install.ps1 | iex
#
#   # Or with arguments:
#   & ([scriptblock]::Create((irm https://raw.githubusercontent.com/tanaikech/gdocumentor/main/scripts/install.ps1))) --agent claude-desktop,cursor,vscode
# ==============================================================================
$ErrorActionPreference = "Stop"

$ReleaseBase = if ($env:GDOCUMENTOR_RELEASE_BASE) { $env:GDOCUMENTOR_RELEASE_BASE } else { "https://github.com/tanaikech/gdocumentor/releases/latest/download" }
$InstallDir = if ($env:GDOCUMENTOR_INSTALL_DIR) { $env:GDOCUMENTOR_INSTALL_DIR } else { Join-Path $env:USERPROFILE ".local\bin" }

if (-not (Test-Path -Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}

$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$BinName = "gdocumentor_windows_$Arch.exe"
$DownloadUrl = "$ReleaseBase/$BinName"
$TargetBin = Join-Path $InstallDir "gdocumentor.exe"

Write-Host "⬇️  Downloading gdocumentor (windows/$Arch) to $TargetBin ..."
Invoke-WebRequest -Uri $DownloadUrl -OutFile $TargetBin -UseBasicParsing

# Add $InstallDir to User PATH if not already present
$UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (-not $UserPath) { $UserPath = "" }
if ($UserPath -notlike "*$InstallDir*") {
    $NewPath = if ($UserPath.EndsWith(";")) { "$UserPath$InstallDir" } elseif ($UserPath -eq "") { $InstallDir } else { "$UserPath;$InstallDir" }
    [Environment]::SetEnvironmentVariable("Path", $NewPath, "User")
    $env:Path = "$env:Path;$InstallDir"
    Write-Host "✅ Added $InstallDir to User PATH."
}

Write-Host "✅ Installed binary: $TargetBin"

if ($args.Count -eq 0) {
    & $TargetBin setup --all --binary-path $TargetBin
} else {
    & $TargetBin setup --binary-path $TargetBin @args
}
