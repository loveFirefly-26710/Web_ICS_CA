package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"web_ics_ca/internal/certs"
)

// Store 是程序自己的状态文件，只记两样东西：上次用的输出目录、当前吊销名单。
//
// 证书本身不存在这里，证书永远以磁盘为准（见 certs.ScanDir）。
// 这里存的是「扫磁盘扫不出来的」信息，尤其是吊销名单：吊销只写进这份 JSON，
// 要生效还得用户点「写出 deny.txt」把它落到输出目录、再重启 web_ics。
//
// 所有导出方法都自己加锁，调用方不需要关心并发。Save 是唯一落盘入口，
// 改完字段必须显式调 Save，没有自动保存。
type Store struct {
	mu   sync.Mutex
	path string

	OutDir string            `json:"outDir"`
	Deny   []certs.DenyEntry `json:"deny"`
}

// storeFileName 是状态文件名，放在 exe 同目录或用户配置目录下（见 storePath）。
const storeFileName = "web-ics-ca.json"

// LoadStore 读取状态文件。文件不存在不算错误，首次运行就是这样，返回一个空 Store。
//
// 文件存在但解析失败会直接报错，不静默重置：状态文件里带着吊销名单，
// 悄悄丢掉它等于悄悄放行一批本该被拒的证书。
func LoadStore() (*Store, error) {
	path, err := storePath()
	if err != nil {
		return nil, err
	}
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("读状态文件失败: %w", err)
	}
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, fmt.Errorf("状态文件 %s 解析失败: %w", path, err)
	}
	return s, nil
}

// storePath 决定状态文件放哪儿。
//
// 优先放在 exe 同目录（便携用法：整个目录拷走，设置跟着走）；
// 该目录不可写时退回用户配置目录。判断可写靠真建一个临时文件试，
// 不看文件权限位，Windows 上那个跟实际能不能写关系不大。
func storePath() (string, error) {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if writable(dir) {
			return filepath.Join(dir, storeFileName), nil
		}
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("找不到可写的配置目录: %w", err)
	}
	dir := filepath.Join(cfg, "Web_ICS_CA")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建配置目录失败: %w", err)
	}
	return filepath.Join(dir, storeFileName), nil
}

// writable 用「建一个临时文件再删掉」来判断目录是否可写。
//
// 探针文件名带 .wtest- 前缀，失败时会立刻删掉；它是运行时探针，不是测试产物。
// 不用 os.Access 之类的检查，因为那在不同平台上语义不一致。
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".wtest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// Save 把当前状态写回磁盘，权限 0600（里面有吊销名单，不该给其他用户读）。
//
// 先 Marshal 再写：序列化失败时不会留下一个被截断的文件。
// 没有用「写临时文件再 rename」那套原子替换：这个文件只有本进程会写
// （单实例保护已经保证了这一点），为它引入额外的复杂度不划算。
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o600)
}

// SetOutDir 记住输出目录并落盘。传进来前应当是绝对路径。
func (s *Store) SetOutDir(dir string) error {
	s.mu.Lock()
	s.OutDir = dir
	s.mu.Unlock()
	return s.Save()
}

// DenyList 返回吊销名单的副本，调用方随便改都不会影响 Store。
func (s *Store) DenyList() []certs.DenyEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]certs.DenyEntry(nil), s.Deny...)
}

// Revoke 把一枚指纹加入吊销名单；已在名单里就只更新备注。
//
// 只改内存，调用方要自己调 Save。幂等是故意的：同一张证书被吊销两次
// 不应该产生两条记录，否则 deny.txt 里会出现重复行。
func (s *Store) Revoke(fingerprint, note string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Deny {
		if s.Deny[i].Fingerprint == fingerprint {
			s.Deny[i].Note = note
			return
		}
	}
	s.Deny = append(s.Deny, certs.DenyEntry{Fingerprint: fingerprint, Note: note})
}

// Unrevoke 从名单里移掉一枚指纹，返回是否真的移掉了（false 表示本来就不在）。
//
// 用 append(s.Deny[:i], s.Deny[i+1:]...) 原地前移，没有另开数组。
// 上面 DenyList 已经做过拷贝，这里不用担心暴露内部切片。
func (s *Store) Unrevoke(fingerprint string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Deny {
		if s.Deny[i].Fingerprint == fingerprint {
			s.Deny = append(s.Deny[:i], s.Deny[i+1:]...)
			return true
		}
	}
	return false
}

// IsRevoked 判断一枚指纹是否在吊销名单里，供扫描证书目录时给每一行打标。
func (s *Store) IsRevoked(fingerprint string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.Deny {
		if e.Fingerprint == fingerprint {
			return true
		}
	}
	return false
}
