package certs

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"strings"
)

// Fingerprint 返回证书 DER 编码的 SHA-256 指纹，小写十六进制、共 64 字符。
//
// 这个值就是吊销名单（deny.txt）里用的那一串。格式必须与 web_ics 侧
// auth.go 的解析口径完全一致。对不上时服务端会把整份名单判成非法并拒绝启动。
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

// Info 把一张已解析的证书转成界面用的 CertInfo。
//
// kind 是调用方对角色做的判定（CA / 服务器 / 客户端），因为 x509.Certificate
// 本身分不清「服务器证书」和「客户端证书」之外还想要什么语义。
// certPath、keyPath 是磁盘位置；keyPath 传空表示没找到配对的私钥（扫描时常见）。
//
// 这里只做字段搬运，不校验、不补全。IPAddresses 是 net.IP，转成字符串才进得了 JSON。
func Info(c *x509.Certificate, kind Kind, certPath, keyPath string) CertInfo {
	ips := make([]string, 0, len(c.IPAddresses))
	for _, ip := range c.IPAddresses {
		ips = append(ips, ip.String())
	}
	return CertInfo{
		Kind:        kind,
		CN:          c.Subject.CommonName,
		Org:         strings.Join(c.Subject.Organization, " "),
		Serial:      serialText(c.SerialNumber),
		NotBefore:   c.NotBefore,
		NotAfter:    c.NotAfter,
		DNSNames:    append([]string(nil), c.DNSNames...),
		IPs:         ips,
		Fingerprint: Fingerprint(c),
		CertPath:    certPath,
		KeyPath:     keyPath,
	}
}
