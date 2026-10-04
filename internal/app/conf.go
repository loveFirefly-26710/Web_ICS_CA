package app

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ConfInput 是生成 web_ics.conf 片段所需的四项事实。
//
// 每项为空都表示「这一项还没有」。空值不会被跳过，而是渲染成一行注释，
// 让用户看得见缺什么，比留白有用：配置文件里少一行是没有提示的。
type ConfInput struct {
	CAPath         string
	ServerCertPath string
	ServerKeyPath  string
	DenyPath       string
}

// ConfSnippet 生成可以直接贴进 web_ics.conf 的片段。
//
// 这几行是一组：client-ca 是唯一开关，写了它就要求客户端证书并改用 HTTPS，
// 此时 tls-cert / tls-key 必须同时配上，否则 web_ics 直接拒绝启动。
// 所以缺哪项就注释掉哪项并写明原因，不给出「半套配置」的假象。
func ConfSnippet(in ConfInput) string {
	var b strings.Builder
	b.WriteString("# 客户端证书认证。client-ca 是开关：不写就是普通的 HTTP，谁都能访问。\n")
	b.WriteString("# 写了它就要求客户端证书，服务改用 HTTPS，同时必须配上服务器证书与私钥。\n")
	if in.CAPath != "" {
		fmt.Fprintf(&b, "client-ca = %s\n", in.CAPath)
	} else {
		b.WriteString("# client-ca = <还没有 CA，先生成一张>\n")
	}
	if in.ServerCertPath != "" && in.ServerKeyPath != "" {
		fmt.Fprintf(&b, "tls-cert = %s\n", in.ServerCertPath)
		fmt.Fprintf(&b, "tls-key = %s\n", in.ServerKeyPath)
	} else {
		b.WriteString("# tls-cert = <还没有服务器证书，先签一张>\n")
		b.WriteString("# tls-key = <同上>\n")
	}
	if in.DenyPath != "" {
		fmt.Fprintf(&b, "client-cert-deny = %s\n", in.DenyPath)
	}
	return b.String()
}

// ConfWarnings 列出这份配置还差什么、以及贴完之后必须做什么。
//
// 只报「用户现在需要知道的」：缺 CA、缺服务器证书、以及配齐之后要重启服务。
// 第三条只在确实配齐时出现，否则会盖住更该看的前两条。
func ConfWarnings(in ConfInput) []string {
	var out []string
	if in.CAPath == "" {
		out = append(out, "还没有 CA。web_ics 的 client-ca 指向它，缺了这一步服务不会开启客户端证书认证。")
	}
	if in.ServerCertPath == "" || in.ServerKeyPath == "" {
		out = append(out, "还没有服务器证书。配了 client-ca 却没配 tls-cert/tls-key 时，web_ics 会直接拒绝启动。")
	}
	if in.CAPath != "" && in.ServerCertPath != "" {
		out = append(out, "改完 web_ics.conf 需要重启服务。证书与吊销名单都不做热加载。")
	}
	return out
}

// DefaultPaths 按约定推出一份 ConfInput：CA 与吊销名单用固定文件名，
// 服务器证书用它自己的路径（.key 由 .crt 换后缀得到）。
//
// serverCertPath 为空时不填服务器证书那两项，交由 ConfSnippet 渲染成待办注释。
// 路径分隔符用 filepath.Join，所以在 Windows 上生成的是反斜杠，
// 贴进 web_ics.conf 后由那侧按本机路径解析。
func DefaultPaths(outDir string, serverCertPath string) ConfInput {
	in := ConfInput{
		CAPath:   filepath.Join(outDir, "ca.crt"),
		DenyPath: filepath.Join(outDir, "deny.txt"),
	}
	if serverCertPath != "" {
		in.ServerCertPath = serverCertPath
		in.ServerKeyPath = strings.TrimSuffix(serverCertPath, ".crt") + ".key"
	}
	return in
}
