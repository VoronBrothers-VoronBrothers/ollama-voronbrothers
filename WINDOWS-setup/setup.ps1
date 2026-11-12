#Requires -Version 5.1
# Ollama fork launcher for Windows — reads env.txt and starts ollama.exe
# Usage: .\setup.ps1 [extra args passed to ollama]

$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path

# --- Load environment variables from env.txt ---
$EnvFile = Join-Path $ScriptDir "env.txt"
if (Test-Path $EnvFile) {
    Write-Host "Loading env: $EnvFile" -ForegroundColor DarkGray
    Get-Content $EnvFile | ForEach-Object {
        $line = $_.Trim()
        if ($line -eq "" -or $line.StartsWith("#")) { return }
        if ($line -match '^(.+?)=(.*)$') {
            $key = $matches[1].Trim()
            $value = $matches[2].Trim()
            Set-Item "env:$key" $value
            Write-Host "  $key=$value" -ForegroundColor DarkGray
        }
    }
} else {
    Write-Warning "env.txt not found in $(Split-Path $ScriptDir) — using current environment"
}

# --- Find and launch ollama.exe ---
$Ollama = Join-Path $ScriptDir "ollama.exe"
if (-not (Test-Path $Ollama)) {
    Write-Error "ollama.exe not found in $ScriptDir`nPlace the Windows build here: $(Split-Path $ScriptDir)"
    exit 1
}

Write-Host ""
Write-Host "Starting Ollama fork..." -ForegroundColor Green
& $Ollama @args
exit $LASTEXITCODE
