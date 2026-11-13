# Dot-source this file to refresh Go in a terminal opened before environment setup.
# Usage: . .\scripts\use-go.ps1
param([string]$GoRoot = [Environment]::GetEnvironmentVariable('GOROOT', 'User'))

if ([string]::IsNullOrWhiteSpace($GoRoot)) {
    throw 'No user GOROOT configured. Pass -GoRoot with an installed 64-bit Go SDK.'
}
$goExecutable = Join-Path $GoRoot 'bin\go.exe'
if (-not (Test-Path -LiteralPath $goExecutable -PathType Leaf)) {
    throw "Go executable not found: $goExecutable"
}
$env:GOROOT = $GoRoot
$goUserPath = [Environment]::GetEnvironmentVariable('GOPATH', 'User')
if ([string]::IsNullOrWhiteSpace($goUserPath)) {
    $goUserPath = [Environment]::GetEnvironmentVariable('GOPATH', 'Machine')
}
if (-not [string]::IsNullOrWhiteSpace($goUserPath)) {
    $env:GOPATH = $goUserPath
}
$goBin = Join-Path $GoRoot 'bin'
$otherPaths = $env:Path -split ';' | Where-Object { $_ -and $_.TrimEnd('\') -ine $goBin.TrimEnd('\') }
$env:Path = (@($goBin) + $otherPaths) -join ';'
# Clear temporary cross-compilation overrides for native local development.
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
Remove-Item Env:GOOS -ErrorAction SilentlyContinue
& $goExecutable version
