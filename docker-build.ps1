# docker-build.ps1 - Windows PowerShell one-shot Docker deploy script
#
# Builds (optional) and starts:
#   - cli-proxy-api
#   - log-uploader
#   - log-qa

$ErrorActionPreference = "Stop"
Set-Location -Path $PSScriptRoot

function Ensure-FileFromExample {
    param(
        [string]$Target,
        [string]$Example
    )
    if (Test-Path $Target) {
        return
    }
    if (-not (Test-Path $Example)) {
        throw "Missing $Example; cannot create $Target"
    }
    Copy-Item $Example $Target
    Write-Host "[prep] created $Target from $Example"
}

Write-Host "--- Preparing config files ---"
Ensure-FileFromExample -Target "config.yaml" -Example "config.example.yaml"
Ensure-FileFromExample -Target "log-uploader.yaml" -Example "log-uploader.example.yaml"
Ensure-FileFromExample -Target "log-qa.yaml" -Example "log-qa.example.yaml"

if (-not (Test-Path ".env")) {
    @"
# Optional environment for docker compose
# VOLC_TOS_ACCESS_KEY_ID=
# VOLC_TOS_SECRET_ACCESS_KEY=
"@ | Set-Content -Path ".env" -Encoding UTF8
    Write-Host "[prep] created empty .env"
}

New-Item -ItemType Directory -Force -Path "logs","auths" | Out-Null

Write-Host "Please select an option:"
Write-Host "1) Run using Pre-built Image (Recommended)"
Write-Host "2) Build from Source and Run (For Developers)"
$choice = Read-Host -Prompt "Enter choice [1-2]"

function Show-Status {
    Write-Host ""
    Write-Host "========================================"
    Write-Host "  Deploy complete"
    Write-Host "========================================"
    Write-Host "Services: cli-proxy-api, log-uploader, log-qa"
    Write-Host "Management UI: http://<server-ip>:8317/management.html"
    Write-Host "Log QA button: right side of Management after login"
    Write-Host "QA reports:    .\logs\log-qa\reports\"
    Write-Host ""
    docker compose ps
}

switch ($choice) {
    "1" {
        Write-Host "--- Running with Pre-built Image ---"
        Write-Host "Note: remote image must include ./log-qa. If missing, use option 2."
        docker compose up -d --remove-orphans --no-build
        Show-Status
    }
    "2" {
        Write-Host "--- Building from Source and Running ---"
        $VERSION = (git describe --tags --always --dirty)
        $COMMIT  = (git rev-parse --short HEAD)
        $BUILD_DATE = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

        Write-Host "Building with:"
        Write-Host "  Version: $VERSION"
        Write-Host "  Commit: $COMMIT"
        Write-Host "  Build Date: $BUILD_DATE"

        $env:CLI_PROXY_IMAGE = "cli-proxy-api:local"
        $env:DOCKER_BUILDKIT = "1"

        docker compose build --build-arg VERSION=$VERSION --build-arg COMMIT=$COMMIT --build-arg BUILD_DATE=$BUILD_DATE
        docker compose up -d --remove-orphans --pull never
        Show-Status
    }
    default {
        Write-Host "Invalid choice. Please enter 1 or 2."
        exit 1
    }
}
