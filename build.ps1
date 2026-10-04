
# 注意：本文件必须保持 UTF-8 with BOM 编码，改完检查一下 BOM 还在不在。
#
# Windows PowerShell 5.1 读「无 BOM」的文件时按系统 ANSI 代码页解码（中文机器上是 cp936）。
# UTF-8 汉字的最后一个字节落在 0x80-0xBF，和 cp936 前导字节的范围 0x81-0xFE 重叠，
# 于是「汉字紧跟一个 ASCII 引号」时，那个引号会被当成双字节字符的尾字节一起吃掉，
# 字符串不闭合，报错是「字符串缺少终止符」，位置却落在文件末尾，离真正出问题的地方
# 十万八千里。全角标点（）：）最容易踩，因为它们总是紧挨着收尾引号。
#
# 用 PowerShell 7 测不出来：它默认按 UTF-8 读无 BOM 文件，怎么改都不会报错。
# 只有 Windows PowerShell 5.1 会炸。所以改完 .ps1 要在 5.1 下过一遍。
#
# 检查 BOM（Git Bash）：
#   head -c 3 build.ps1 | od -An -tx1      # 应输出  ef bb bf
# 补 BOM：
#   printf '\xef\xbb\xbf' | cat - build.ps1 > t && mv t build.ps1

param(
  [string]$Version = "",
  [string]$Go = "go"
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

if (-not $env:GOPROXY) { $env:GOPROXY = "https://goproxy.cn,direct" }

# 本脚本调 go / gofmt 是按名字调的（`& go vet ...`）。PowerShell 要把 `go` 补全成
# `go.exe` 靠的是 PATHEXT，而 PATHEXT 不一定是标准值：受限或加固过的环境里见过只剩
# `.CPL` 的（正常应该是 `.COM;.EXE;.BAT;.CMD;...`，Windows 默认由 HKLM 提供）。
# 那种环境里 `go` 解析不出来，报 "The term 'go' is not recognized"，看着像「没装 Go」；
# 更别扭的是连写全路径调用都会静默返回空（没有输出，$LASTEXITCODE 也是空的）。
# 把缺的补回来，省得脚本在那种环境里死在一句看不出原因的报错上。
# 补的是 Windows 的标准值，只影响当前 PowerShell 会话，不写进系统设置。
# `& .\build.ps1` 是同一个进程，所以会留在调用方的会话里；README 里那种
# `powershell -File build.ps1` 用法各起一个进程，不受影响。
if ($env:PATHEXT -notlike "*.EXE*") {
  $oldPathext = $env:PATHEXT
  $env:PATHEXT = ".COM;.EXE;.BAT;.CMD;.VBS;.JS;.WS;.MSC"
  Write-Host "==> PATHEXT 缺 .EXE（原值：$oldPathext），已补上（本次 PowerShell 会话有效）"
}

# 把 Go 工具链解析成明确的路径，并给出可操作的报错，
# 而不是让 PowerShell 抛一句 "The term 'go' is not recognized" 就结束。
$goCmd = Get-Command $Go -ErrorAction SilentlyContinue
if (-not $goCmd) { $goCmd = Get-Command "$Go.exe" -ErrorAction SilentlyContinue }
if (-not $goCmd) {
  Write-Host "!! 找不到 Go 工具链：$Go"
  Write-Host "   装好 Go 并加进 PATH，或者显式指定它的位置："
  Write-Host "   powershell -ExecutionPolicy Bypass -File build.ps1 -Go '<go.exe 的完整路径>'"
  exit 1
}
$Go = $goCmd.Source
Write-Host "==> toolchain: $(& $Go version)"

# gofmt 总是和 go 在同一个 bin 目录里，按相对位置取，省得再依赖一次 PATH。
$gofmt = Join-Path (Split-Path $Go) "gofmt.exe"
if (-not (Test-Path $gofmt)) { $gofmt = "gofmt" }

# 版本号的唯一来源是仓库根目录的 VERSION 文件，改版本只改那一个文件。
# -Version 参数仍然优先。
if (-not $Version) {
  $vf = Join-Path $PSScriptRoot "VERSION"
  if (Test-Path $vf) {
    $Version = (Get-Content -LiteralPath $vf -Raw -ErrorAction SilentlyContinue).Trim()
  }
}
if (-not $Version) {
  $tag = ""
  try { $tag = (git describe --tags --abbrev=0 2>$null) } catch { $tag = "" }
  if ($tag) { $Version = $tag.TrimStart("v") }
}
if (-not $Version) { $Version = "dev" }
Write-Host "==> version: $Version"

$ldflags = "-s -w -X main.version=$Version"

Write-Host "==> gofmt"
$unformatted = & $gofmt -l .
if ($unformatted) {
  Write-Host "these files need gofmt -w . :"
  $unformatted | ForEach-Object { Write-Host "  $_" }
  exit 1
}

Write-Host "==> go vet"
& $Go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "==> build windows/amd64"
New-Item -ItemType Directory -Force -Path dist | Out-Null
$oldGOOS = $env:GOOS
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
& $Go build -trimpath -ldflags="$ldflags -H windowsgui" -o dist/web-ics-ca.exe .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

# 再交叉编译一份 Linux 版。它不是交付物，交付的是单文件 Windows EXE，
# 这一步只为确认 window_other.go / dpi_other.go 那些非 Windows 分支还能编过。
# 在 Windows 上开发的人改坏了它们，没有这一步是发现不了的。
Write-Host "==> build linux/amd64（只为确认跨平台分支能编过）"
$env:GOOS = "linux"
& $Go build -trimpath -ldflags="$ldflags" -o dist/web-ics-ca-linux-amd64 .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
# GOOS 是用 $env: 设的，会留在当前会话里。用完还原，免得后面手敲的 go build
# 莫名其妙按 linux 目标编。
$env:GOOS = $oldGOOS

Write-Host "==> check: no absolute paths in the binaries"
# 与 build.sh 用同一条规则。盘符后面可能是反斜杠（Windows 原生写法）也可能是正斜杠
# （-trimpath 之后的样子），行首也要算。只认正斜杠会漏掉源码里写死的 Windows 路径。
$badPath = '(^|[^A-Za-z0-9])[A-Za-z]:[\\/][A-Za-z0-9_][A-Za-z0-9_./\\-]{4,}'
# 用 Latin-1 解码读二进制：每个字节一对一映射成一个字符，不会因为非法 UTF-8 序列
# 丢字节，所以 ASCII 模式在二进制上仍然可靠。
$latin1 = [System.Text.Encoding]::GetEncoding(28591)
foreach ($f in @("dist/web-ics-ca.exe", "dist/web-ics-ca-linux-amd64")) {
  $text = $latin1.GetString([System.IO.File]::ReadAllBytes($f))
  $hits = @([regex]::Matches($text, $badPath) | ForEach-Object { $_.Value } | Sort-Object -Unique)
  if ($hits.Count -gt 0) {
    Write-Host "!! absolute path found in ${f}:"
    $hits | Select-Object -First 5 | ForEach-Object { Write-Host "   $_" }
    exit 1
  }
  if (Select-String -Path $f -Pattern "go/pkg/mod" -Quiet -ErrorAction SilentlyContinue) {
    Write-Host "!! module cache path found in ${f}"
    exit 1
  }
}
Write-Host "    clean"

Get-ChildItem dist | Format-Table Name, Length -AutoSize
Write-Host "done. dist/web-ics-ca.exe is the deliverable."
Write-Host "      dist/web-ics-ca-linux-amd64 只是跨平台编译检查，不用发。"
