package certs

import (
	"fmt"
	"os"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// ExportPFX 把证书与私钥打包成一份带密码的 PKCS#12（.pfx），写到 outPath。
//
// 这是整个项目唯一的第三方依赖：标准库不提供 PKCS#12 编码。
// 用 pkcs12.Modern（PBES2 + AES-256-CBC + SHA-256）而不是 Legacy：
// 老式的 RC2/3DES 方案在 Windows 上会被塞进
// 「Microsoft Enhanced Cryptographic Provider」，浏览器可能递不出证书。
//
// 打包前再验一次证书与私钥是不是一对。把不匹配的两样东西包进去，
// 错误要到用户在 Windows 上双击导入时才暴露。
func ExportPFX(certPath, keyPath, password, outPath string) error {
	certRaw, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("读证书失败: %w", err)
	}
	keyRaw, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("读私钥失败: %w", err)
	}
	cert, err := parseCertPEM(certRaw)
	if err != nil {
		return err
	}
	key, err := parseKeyPEM(keyRaw)
	if err != nil {
		return err
	}
	if !samePublicKey(cert, key) {
		return fmt.Errorf("证书与私钥不是一对，无法打包")
	}

	// 第二个 nil 是 CA 链：这里只装叶子证书，CA 由用户在设备上单独装进信任库。
	der, err := pkcs12.Modern.Encode(key, cert, nil, password)
	if err != nil {
		return fmt.Errorf("打包 PKCS#12 失败: %w", err)
	}
	return writeFile(outPath, der)
}
