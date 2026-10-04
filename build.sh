#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

GO="${GO:-go}"
echo "==> 工具链: $("$GO" version)"

export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

# 版本号的唯一来源是仓库根目录的 VERSION 文件。改版本只改那一个文件。
# 命令行传 VERSION= 仍然优先，方便脚本临时覆盖。
VERSION="${VERSION:-}"
if [ -z "$VERSION" ] && [ -f VERSION ]; then
  VERSION=$(tr -d '[:space:]' < VERSION)
fi
if [ -z "$VERSION" ]; then
  VERSION=$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//') || true
fi
[ -n "$VERSION" ] || VERSION="dev"
echo "==> 版本号: $VERSION"

LDFLAGS="-s -w -X main.version=$VERSION"
mkdir -p dist

if [ -e .git ] && ! git rev-parse --git-dir >/dev/null 2>&1; then
  echo "==> 警告：.git 存在但不可用，跳过 VCS 标记"
  export GOFLAGS="${GOFLAGS:+$GOFLAGS }-buildvcs=false"
fi

echo "==> 检查格式"
unformatted=$(gofmt -l . || true)
if [ -n "$unformatted" ]; then
  echo "有文件没格式化，先跑 gofmt -w ."
  echo "$unformatted"
  exit 1
fi

echo "==> 静态检查"
"$GO" vet ./...

echo "==> 核对：build.ps1 带 UTF-8 BOM"
# Windows PowerShell 5.1 读「无 BOM」的文件时按系统 ANSI 代码页解码（中文机器上是 cp936）。
# UTF-8 汉字的最后一个字节落在 0x80-0xBF，与 cp936 前导字节的范围 0x81-0xFE 重叠，于是
# 「汉字紧跟一个 ASCII 引号」时那个引号会被当成双字节字符的尾字节一起吃掉，字符串不闭合，
# 报错是「字符串缺少终止符」，位置却落在文件末尾，离真正出问题的地方十万八千里。
# 全角标点（）：）最容易踩，因为它们总是紧挨着收尾引号。
#
# 纯 ASCII 的 .ps1 不需要 BOM，所以这个坑是「给 .ps1 加中文」那一刻才出现的。
# PowerShell 7 测不出来（它默认按 UTF-8 读无 BOM 文件），只有 5.1 会炸。
# 补 BOM 的命令写在 build.ps1 顶部的注释里。
ps1_head=$(head -c 3 build.ps1 | od -An -tx1 | tr -d ' \n')
if [ "$ps1_head" != "efbbbf" ]; then
  echo "!! build.ps1 缺少 UTF-8 BOM（当前前 3 字节：$ps1_head）"
  echo "   它里面有中文，没有 BOM 的话 Windows PowerShell 5.1 会报「字符串缺少终止符」。"
  echo "   补法见 build.ps1 顶部的注释。"
  exit 1
fi
echo "    有"

echo "==> Windows amd64（主产物）"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  "$GO" build -trimpath -ldflags="$LDFLAGS -H windowsgui" -o dist/web-ics-ca.exe .

echo "==> Linux amd64（只为确认跨平台分支能编过）"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  "$GO" build -trimpath -ldflags="$LDFLAGS" -o dist/web-ics-ca-linux-amd64 .

echo "==> 核对：二进制里不该出现绝对路径"
# 盘符后面可能是反斜杠（Windows 原生写法）也可能是正斜杠（-trimpath 之后的样子），
# 两种都要认。只认正斜杠会漏掉源码里写死的 Windows 路径，实测漏过
# openbrowser_windows.go 里那两条 Program Files 下的 msedge.exe。
# 行首也要算：grep -a 把二进制按行切开之后，路径可能正好落在行首。
BAD='(^|[^A-Za-z0-9])[A-Za-z]:[\\/][A-Za-z0-9_][A-Za-z0-9_./\\-]{4,}'
for f in dist/web-ics-ca.exe dist/web-ics-ca-linux-amd64; do
  hits=$(grep -aoE "$BAD" "$f" 2>/dev/null | sort -u | head -5 || true)
  if [ -n "$hits" ]; then
    echo "!! $f 里仍有绝对路径："
    echo "$hits"
    exit 1
  fi
  if grep -aq 'go/pkg/mod' "$f"; then
    echo "!! $f 里仍有模块缓存路径"
    exit 1
  fi
done
echo "    干净"

echo
ls -lh dist/
echo "完成。dist/web-ics-ca.exe 是给用户的那一个，拷走就能跑。"
