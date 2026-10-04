package certs

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// keyBits 是生成的 RSA 密钥长度。
//
// 选 RSA 2048 而不是 ECDSA，是为了和 web_ics README 里那套 openssl 命令对齐：
// 客户端证书最终要装进 Windows 证书存储，RSA 在那里的兼容性最稳。
const keyBits = 2048

// newKey 生成一张 RSA 私钥。
func newKey() (*rsa.PrivateKey, error) {
	k, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return nil, fmt.Errorf("生成密钥失败: %w", err)
	}
	return k, nil
}

// newSerial 生成一个 128 位随机序列号，加 1 保证非零。
//
// 不用递增计数器：同一个 CA 可能被拷到别的机器上继续签发，计数器会撞号。
// 随机大数不会有这个问题。RFC 5280 只要求正数且不重复。
func newSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("生成序列号失败: %w", err)
	}
	return n.Add(n, big.NewInt(1)), nil
}

// certPEM 把 DER 编码的证书包成 PEM 文本。
func certPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// keyPEM 把私钥编码成 PKCS#8 的 PEM 文本。
//
// 统一用 PKCS#8 而不是 PKCS#1：openssl 3 与现代工具默认都认它，
// 而且同一个函数能处理 RSA / EC 两种私钥，导入 CA 时不用分支。
func keyPEM(key any) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("编码私钥失败: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// writeFile 写文件，顺带建目录，权限 0600。
//
// 0600 是给私钥定的：证书目录里有 ca.key，不能让同机器的其他用户读走。
// 证书本身也用 0600，多给权限没有收益。
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("写 %s 失败: %w", filepath.Base(path), err)
	}
	return nil
}

// fileBase 把用户填的名字洗成一个安全的文件名前缀。
//
// 处理顺序：去掉控制字符，把 Windows 非法字符 `/\:*?"<>|` 换成 `-`，去掉首尾空白与点，
// 合并连续的 `-`，超长截到 64 个字符，全空时退回 "cert"。
//
// 按 rune 而不是 byte 截断：名字通常是中文，按字节截会切出半个汉字。
// 名字已经在 validate 里校验过，这里是第二道防线。文件名会进日志和路径，
// 不该因为一个奇怪的名字让写盘失败。
func fileBase(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsControl(r):
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	s = strings.Trim(s, ". ")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if s == "" {
		return "cert"
	}
	if len([]rune(s)) > 64 {
		s = string([]rune(s)[:64])
	}
	return s
}

// uniquePath 给一个想用的路径找第一个不冲突的名字。
//
// 已存在时依次试 `<名字>-2.crt`、`-3`……最多到 -999，实在不行返回原路径
// （宁可让写盘失败，也不要静默覆盖）。
//
// 重名不覆盖是故意的。覆盖会把私钥换掉，而旧证书可能还装在某台设备上，
// 两边指纹对不上，排查时没有任何线索指向签发环节。
func uniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; i < 1000; i++ {
		p := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
	}
	return path
}
