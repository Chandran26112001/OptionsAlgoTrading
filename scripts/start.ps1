param([switch]$Demo, [switch]$Build)
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)
if (-not (Test-Path 'frontend/node_modules') -or $Build -or -not (Test-Path 'backend/web/assets')) {
    Push-Location frontend
    try {
        npm ci
        if ($LASTEXITCODE -ne 0) { throw 'Frontend dependency installation failed.' }
        npm run build
        if ($LASTEXITCODE -ne 0) { throw 'Frontend build failed.' }
    } finally { Pop-Location }
}
if (-not (Test-Path '.env')) { Copy-Item .env.example .env }
$previousMarketMode = $env:MARKET_DATA_MODE
if ($Demo) { $env:MARKET_DATA_MODE = 'demo' }
try { go run ./backend } finally { if ($Demo) { $env:MARKET_DATA_MODE = $previousMarketMode } }
