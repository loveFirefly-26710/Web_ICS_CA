package certs

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"net"
	"path/filepath"
	"time"
)

// IssueServer 用 ca 签一张服务器证书。
//
// 服务器证书的关键在 SAN：DNSNames 与 IPs 会被原样写进证书，浏览器连上来时
// 拿地址去比对的就是它们。两者同时为空时直接拒绝签发：openssl 默认不写 SAN，
// 手工建的证书常犯这个错，而症状是浏览器报「证书不受信任」，看不出根因。
//
// KeyUsage 只给 digitalSignature：web_ics 走 ECDHE 密钥交换，不需要 keyEncipherment；
// EKU 给 serverAuth。ExtKeyUsage 是扫描时判定角色的依据。
func IssueServer(ca *CA, req ServerRequest) (*CertInfo, error) {
	if len(req.DNSNames) == 0 && len(req.IPs) == 0 {
		return nil, fmt.Errorf("服务器证书至少要有一个 SAN（内网地址或外网地址），" +
			"否则浏览器不会认这张证书")
	}
	key, err := newKey()
	if err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: req.CN},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     now.AddDate(0, 0, req.Days),

		DNSNames:    req.DNSNames,
		IPAddresses: parseIPs(req.IPs),

		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	return ca.sign(key, tmpl, req.OutDir, fileBase(req.CN), KindServer, "")
}

// IssueClient 用 ca 签一张客户端证书，供某台设备做 mTLS 用。
//
// KeyUsage 必须是 digitalSignature，不能省：只给 EKU 时，Windows 的证书选择器
// 可能不认这张证书，浏览器根本不会把它递出去，服务端只看到一次没有证书的握手。
// EKU 给 clientAuth。
//
// PFXPassword 非空时额外导出一份 .pfx（Windows 双击即可装进个人证书库）；
// 导出失败会把整次签发判为失败，因为界面已经告诉用户「勾了就有 .pfx」。
func IssueClient(ca *CA, req ClientRequest) (*CertInfo, error) {
	key, err := newKey()
	if err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	subject := pkix.Name{CommonName: req.CN}
	if req.Org != "" {
		subject.Organization = []string{req.Org}
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      subject,
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     now.AddDate(0, 0, req.Days),

		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	info, err := ca.sign(key, tmpl, req.OutDir, fileBase(req.CN), KindClient, req.Org)
	if err != nil {
		return nil, err
	}
	if req.PFXPassword != "" {
		pfxPath := uniquePath(filepath.Join(req.OutDir, fileBase(req.CN)+".pfx"))
		if err := ExportPFX(info.CertPath, info.KeyPath, req.PFXPassword, pfxPath); err != nil {
			return nil, err
		}
		info.PFXPath = pfxPath
	}
	return info, nil
}

// sign 是签发子证书的公共后半段：用 ca 签出 key/tmpl，落盘证书与私钥，返回 CertInfo。
//
// 调用方负责填好 tmpl（Subject、SAN、KeyUsage、EKU、有效期）与文件名前缀 base。
// 文件名走 uniquePath，同名不会覆盖，第二次签「我的手机」会得到「我的手机-2.crt」。
//
// org 只用于界面显示（证书里的 Organization 由 tmpl.Subject 决定，两者保持一致）。
func (ca *CA) sign(key crypto.Signer, tmpl *x509.Certificate, outDir, base string, kind Kind, org string) (*CertInfo, error) {
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, key.Public(), ca.Key)
	if err != nil {
		return nil, fmt.Errorf("签发失败: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("解析刚签发的证书失败: %w", err)
	}
	keyOut, err := keyPEM(key)
	if err != nil {
		return nil, err
	}
	certPath := uniquePath(filepath.Join(outDir, base+".crt"))
	keyPath := uniquePath(filepath.Join(outDir, base+".key"))
	if err := writeFile(certPath, certPEM(der)); err != nil {
		return nil, err
	}
	if err := writeFile(keyPath, keyOut); err != nil {
		return nil, err
	}
	info := Info(cert, kind, certPath, keyPath)
	info.Org = org
	return &info, nil
}

// parseIPs 把界面传来的地址字符串里能解析成 IP 的挑出来，转成 net.IP 写进 SAN。
//
// 解析不了的直接跳过（它们已经以 DNS 名的身份从另一条路进来了）。
// 校验在 internal/validate 里做过了，这里只是类型转换。
func parseIPs(list []string) []net.IP {
	out := make([]net.IP, 0, len(list))
	for _, s := range list {
		if ip := net.ParseIP(s); ip != nil {
			out = append(out, ip)
		}
	}
	return out
}

// samePublicKey 判断证书里的公钥与给定私钥是不是一对。
//
// 走 MarshalPKIXPublicKey 后比字节，而不是比 *rsa.PublicKey 的字段：
// 这样 RSA / ECDSA / Ed25519 都能用同一段代码判，不用按类型分支。
// 私钥与证书配错是最容易发生也最难查的部署错误，签发前拦一道很值。
func samePublicKey(cert *x509.Certificate, key any) bool {
	signer, ok := key.(crypto.Signer)
	if !ok {
		return false
	}
	a, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return false
	}
	b, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return false
	}
	return bytes.Equal(a, b)
}
