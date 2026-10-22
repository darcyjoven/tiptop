# Windows 无 make 时的构建脚本。
$ErrorActionPreference = "Stop"

$env:GOENV = "off"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "1"
if (-not $env:CC) { $env:CC = "gcc" }
if (-not $env:CXX) { $env:CXX = "g++" }
if (-not $env:GOPROXY) { $env:GOPROXY = "https://goproxy.cn,direct" }

$out = "artifacts/bin/tiptop.exe"
New-Item -ItemType Directory -Force -Path (Split-Path $out) | Out-Null
go build -trimpath -o $out .
Write-Host "built $out"
