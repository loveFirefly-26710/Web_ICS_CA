package certs

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// clockSkew 是 NotBefore 往前挪的量。
//
// 签发机与验证方（浏览器、web_ics 服务器）的时钟不可能完全一致，差几分钟很常见。
// 不往前挪的话，刚签出来的证书在慢一点的机器上会短暂处于「还没生效」状态，
// 握手直接失败，而现象只是「证书不能用」，很难联想到时钟。
const clockSkew = 5 * time.Minute

// 根 CA 在输出目录里的固定文件名。扫描、加载、导入都靠这两个名字定位。
const (
	CACertName = "ca.crt"
	CAKeyName  = "ca.key"
)

// CreateCA 生成一张自签名的根 CA，写出 ca.crt 与 ca.key，并返回装好内存的 CA。
//
// name 写进 Subject.CommonName，只是给人看的标识；days 是从今天算起的有效天数，
// 界面侧已校验过范围；outDir 不存在会被创建。
//
// 单层结构：没有中间 CA，服务器证书与客户端证书都由它直接签。
// 私钥以 0600 落盘，不应该被部署到服务器上，服务器只要 ca.crt。
func CreateCA(name string, days int, outDir string) (*CA, error) {
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
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     now.AddDate(0, 0, days),

		// IsCA + CertSign 才是「能拿来签发」的 CA；CRLSign 一并给上，
		// 虽然本项目不用 CRL，但缺了它有些校验工具会报 CA 用途不完整。
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	// 自签：模板、父证书、被签公钥、签名私钥都是自己。
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("自签 CA 失败: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("解析刚生成的 CA 失败: %w", err)
	}

	certOut := certPEM(der)
	keyOut, err := keyPEM(key)
	if err != nil {
		return nil, err
	}
	certPath := filepath.Join(outDir, CACertName)
	keyPath := filepath.Join(outDir, CAKeyName)
	if err := writeFile(certPath, certOut); err != nil {
		return nil, err
	}
	if err := writeFile(keyPath, keyOut); err != nil {
		return nil, err
	}
	return &CA{
		Cert:    cert,
		Key:     key,
		CertPEM: certOut,
		KeyPEM:  keyOut,
		Info:    Info(cert, KindCA, certPath, keyPath),
	}, nil
}

// LoadCA 从磁盘读回一张 CA 并做签发前的自检。
//
// 每次都自检，不信任文件：这个目录是给人手工放东西的，把私钥与证书配错、
// 把过期 CA 留在那儿、把普通证书改名成 ca.crt 都会真实发生。提前拦下来并带上文件名，
// 比等到签出一堆验不过的证书再回头查要好。
//
// 检查三项：证书与私钥是不是一对、是不是 CA、有没有过期。
func LoadCA(certPath, keyPath string) (*CA, error) {
	certRaw, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("读 CA 证书失败: %w", err)
	}
	keyRaw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("读 CA 私钥失败: %w", err)
	}
	cert, err := parseCertPEM(certRaw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(certPath), err)
	}
	key, err := parseKeyPEM(keyRaw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(keyPath), err)
	}
	if !samePublicKey(cert, key) {
		return nil, fmt.Errorf("%s 里的私钥与 %s 不是一对，签出来的证书链会验不过",
			filepath.Base(keyPath), filepath.Base(certPath))
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("%s 不是 CA 证书（没有 basicConstraints=CA:TRUE），不能用来签发",
			filepath.Base(certPath))
	}
	if time.Now().After(cert.NotAfter) {
		return nil, fmt.Errorf("%s 已经在 %s 过期，请换一张 CA",
			filepath.Base(certPath), cert.NotAfter.Format("2006-01-02"))
	}
	return &CA{
		Cert:    cert,
		Key:     key,
		CertPEM: certRaw,
		KeyPEM:  keyRaw,
		Info:    Info(cert, KindCA, certPath, keyPath),
	}, nil
}

// ImportCA 把别处建好的 CA 收进输出目录，写成标准的 ca.crt / ca.key。
//
// 只做字节复制，不重新编码、不重新签名：CA 证书一旦被重新编码，指纹就变了，
// 而指纹已经印在每台客户端装好的那份里，改了就对不上。
//
// 源文件与目标文件相同时（重复导入自己）会被覆盖成同样的内容，无副作用。
func ImportCA(certPath, keyPath, outDir string) (*CA, error) {
	ca, err := LoadCA(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	dstCert := filepath.Join(outDir, CACertName)
	dstKey := filepath.Join(outDir, CAKeyName)
	if err := writeFile(dstCert, ca.CertPEM); err != nil {
		return nil, err
	}
	if err := writeFile(dstKey, ca.KeyPEM); err != nil {
		return nil, err
	}
	ca.Info.CertPath = dstCert
	ca.Info.KeyPath = dstKey
	return ca, nil
}

// parseCertPEM 从 PEM 文本里找出第一段 CERTIFICATE 并解析。
//
// 会跳过前面的其他段落（比如某些工具会把私钥和证书拼在同一个文件里）。
// 用循环而不是只看第一段，是因为 pem.Decode 每次只吐一段。
func parseCertPEM(data []byte) (*x509.Certificate, error) {
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("没有找到 CERTIFICATE 段落")
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		return x509.ParseCertificate(block.Bytes)
	}
}

// parseKeyPEM 从 PEM 文本里找出第一段可用的私钥。
//
// 三种编码都收：PKCS#8（"PRIVATE KEY"，Go 自己写的和现代 openssl 的默认）、
// PKCS#1（"RSA PRIVATE KEY"，老 openssl）、SEC1（"EC PRIVATE KEY"，EC 密钥）。
// 收 EC 是为了能导入别人用 ECDSA 建的 CA。本项目自己签的仍然是 RSA。
func parseKeyPEM(data []byte) (crypto.Signer, error) {
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("没有找到私钥段落")
		}
		var (
			k   any
			err error
		)
		switch block.Type {
		case "PRIVATE KEY":
			k, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		case "RSA PRIVATE KEY":
			k, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		case "EC PRIVATE KEY":
			k, err = x509.ParseECPrivateKey(block.Bytes)
		default:
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", block.Type, err)
		}
		signer, ok := k.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("%s 里的密钥不能用于签名", block.Type)
		}
		return signer, nil
	}
}

// serialText 把序列号渲染成大写十六进制，供界面显示。
func serialText(n *big.Int) string {
	return fmt.Sprintf("%X", n)
}
