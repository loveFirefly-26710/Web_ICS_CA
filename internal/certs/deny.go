package certs

import (
	"sort"
	"strings"
)

// DenyEntry 是吊销名单里的一条：一枚 SHA-256 指纹，加一句给人看的备注。
//
// 名单不落在输出目录里，而是存在程序自己的状态文件（见 internal/app/store.go）；
// 只有点「写出 deny.txt」时才渲染成文件。所以吊销本身不要求输出目录可用。
type DenyEntry struct {
	Fingerprint string `json:"fingerprint"`
	Note        string `json:"note"`
}

// FormatDeny 把吊销名单渲染成 deny.txt 的内容。
//
// 格式与 web_ics 的 auth.go 解析口径绑定：一行一个 sha256:<64 位小写十六进制>，
// 后面可以跟空格分隔的备注，`#` 开头是注释行。格式对不上时 web_ics 会把整份名单
// 判成非法并拒绝启动，所以这里的排版不要随手改。
//
// 输出前按指纹排序，保证同一份名单每次写出的字节完全一致。名单是要进版本库、
// 要 diff 的东西，顺序抖动会制造假改动。
func FormatDeny(entries []DenyEntry) string {
	sorted := append([]DenyEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Fingerprint < sorted[j].Fingerprint
	})
	var b strings.Builder
	b.WriteString("# web_ics 客户端证书吊销名单\n")
	b.WriteString("# 一行一个 sha256 指纹，后面可以跟备注。改完需要重启服务生效。\n")
	b.WriteString("# 指纹由本工具在证书库页面直接生成，不要手写。\n")
	for _, e := range sorted {
		b.WriteString("sha256:" + e.Fingerprint)
		if e.Note != "" {
			b.WriteString(" " + e.Note)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// WriteDeny 把吊销名单写到 path（通常是 <输出目录>/deny.txt）。
// 写完 web_ics 不会自动生效，要重启服务。
func WriteDeny(path string, entries []DenyEntry) error {
	return writeFile(path, []byte(FormatDeny(entries)))
}
