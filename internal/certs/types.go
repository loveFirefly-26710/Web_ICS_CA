// Package certs 是证书体系的全部领域逻辑：建 CA、签发、解析、导出 PKCS#12、
// 维护吊销名单、扫描证书目录。
//
// 这一层不依赖 HTTP，也不认识界面；internal/app 把它包成接口暴露给前端。
// 所有签发都走 Go 标准库的 crypto/x509，不调用 openssl。
//
// 证书参数（算法、KeyUsage、EKU、有效期偏移）与 web_ics 仓库的证书约定对齐，
// 改这里之前先确认 web_ics 那侧还认。
package certs

import (
	"crypto"
	"crypto/x509"
	"time"
)

// Kind 是证书在整套体系里扮演的角色。
//
// 角色不是从文件名猜的，而是从证书自身的内容判出来的（见 scan.go 的 classify）：
// IsCA 为真就是根 CA，EKU 含 serverAuth 就是服务器证书，含 clientAuth 就是客户端证书。
// 界面上的标签、排序、能否吊销都由它决定。
type Kind string

const (
	KindCA     Kind = "ca"     // 根 CA：唯一的签发者
	KindServer Kind = "server" // 服务器证书：给 web_ics 自己用
	KindClient Kind = "client" // 客户端证书：每台要访问的设备一张
)

// CertInfo 是界面展示一张证书所需的全部信息，也是 /api/state 里证书行的形状。
//
// 它描述的是磁盘上已经存在的一张证书，不含私钥内容。私钥只在签名时按路径读一次。
// 字段与 ui/app.js 的渲染一一对应，加字段时记得那边也要用起来。
type CertInfo struct {
	Kind      Kind      `json:"kind"`
	CN        string    `json:"cn"`
	Org       string    `json:"org,omitempty"`
	Serial    string    `json:"serial"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	DNSNames  []string  `json:"dnsNames"`
	IPs       []string  `json:"ips"`

	// Fingerprint 是 SHA-256 指纹（小写十六进制）。它是吊销名单里的主键。
	Fingerprint string `json:"fingerprint"`

	CertPath string `json:"certPath"`
	KeyPath  string `json:"keyPath"` // 同名的 .key 不存在时为空
	PFXPath  string `json:"pfxPath,omitempty"`
}

// CA 是一张已经装进内存、随时可以拿去签发的 CA。
//
// CertPEM / KeyPEM 保留磁盘上的原始字节：导入 CA 时直接原样写出，
// 不做重新编码：重新编码会改变指纹，客户端已经装好的那份就对不上了。
type CA struct {
	Cert    *x509.Certificate
	Key     crypto.Signer
	CertPEM []byte
	KeyPEM  []byte
	Info    CertInfo
}

// ServerRequest 是签服务器证书的输入。
//
// DNSNames 与 IPs 会原样写进证书的 SAN。两者不能同时为空：现代浏览器只认 SAN，
// 没有 SAN 的服务器证书一律被判为无效。地址该进哪个字段由 validate 包判好后分开传入。
type ServerRequest struct {
	CN       string
	DNSNames []string
	IPs      []string
	Days     int
	OutDir   string
}

// ClientRequest 是签客户端证书的输入。
//
// PFXPassword 非空时，签完会额外导出一份同名 .pfx（Windows 上双击即可导入）。
type ClientRequest struct {
	CN     string
	Org    string
	Days   int
	OutDir string

	PFXPassword string
}
