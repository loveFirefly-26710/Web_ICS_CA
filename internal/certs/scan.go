package certs

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ScanDir 扫描目录里的所有 .crt，解析成 CertInfo 列表。
//
// 这个项目不维护证书索引，每次都直接扫磁盘。索引与磁盘不同步是最难查的一类问题：
// 界面说证书在、服务端说验不过。直接扫的话，手工删掉一个文件、从别处拷一张进来，
// 刷新就对得上。
//
// 容错策略是「跳过而不是报错」：单个文件读不了或解析不了（可能是别的工具的证书、
// 半截文件、非 PEM 内容）就跳过它，不影响整个目录的展示。
//
// 顺带补上同名 .key / .pfx 是否存在，界面靠这个显示「有 .pfx」。
// 排序：先按角色（CA → 服务器 → 客户端 → 未识别），同角色内按到期时间从早到晚，
// 这样「最近要处理的」总是排在最前面。
func ScanDir(dir string) ([]CertInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// 目录还不存在是正常状态（用户刚启动、还没设输出目录），返回空列表即可。
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []CertInfo
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".crt") {
			continue
		}
		certPath := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(certPath)
		if err != nil {
			continue
		}
		c, err := parseCertPEM(raw)
		if err != nil {
			continue
		}
		info := Info(c, classify(c), certPath, "")
		if keyPath := strings.TrimSuffix(certPath, ".crt") + ".key"; fileExists(keyPath) {
			info.KeyPath = keyPath
		}
		if pfxPath := strings.TrimSuffix(certPath, ".crt") + ".pfx"; fileExists(pfxPath) {
			info.PFXPath = pfxPath
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return kindOrder(out[i].Kind) < kindOrder(out[j].Kind)
		}
		return out[i].NotAfter.Before(out[j].NotAfter)
	})
	return out, nil
}

// FindCA 返回目录里第一张根 CA 的 CertInfo，没有则返回 (nil, nil)。
//
// 第二个返回值是「扫描本身失败」，不是「没找到」；没找到时两者都是 nil。
// 调用方普遍写成 `info, _ := FindCA(dir); if info == nil {...}`，
// 因为扫描失败在这里等价于没有可用 CA，处理方式一样。
func FindCA(dir string) (*CertInfo, error) {
	list, err := ScanDir(dir)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Kind == KindCA {
			return &list[i], nil
		}
	}
	return nil, nil
}

// classify 从证书内容判定它的角色。
//
// 不看文件名：文件可以被随便改名，而 IsCA 与 EKU 是签进去的，改不掉。
// 判定顺序是先 CA 后服务器再客户端，因为一张证书可能同时带多个 EKU。
// 都不匹配时返回空 Kind，界面显示成「未识别」。目录里放别的工具的证书时会出现。
func classify(c *x509.Certificate) Kind {
	if c.IsCA {
		return KindCA
	}
	for _, e := range c.ExtKeyUsage {
		if e == x509.ExtKeyUsageServerAuth {
			return KindServer
		}
	}
	for _, e := range c.ExtKeyUsage {
		if e == x509.ExtKeyUsageClientAuth {
			return KindClient
		}
	}
	return ""
}

// kindOrder 给角色定排序权重，供 ScanDir 用。
func kindOrder(k Kind) int {
	switch k {
	case KindCA:
		return 0
	case KindServer:
		return 1
	case KindClient:
		return 2
	}
	return 3
}

// fileExists 判断路径存在且是普通文件（目录不算）。
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// KindLabel 把角色翻译成界面上的中文标签。
func KindLabel(k Kind) string {
	switch k {
	case KindCA:
		return "根 CA"
	case KindServer:
		return "服务器证书"
	case KindClient:
		return "客户端证书"
	}
	return "未识别"
}
