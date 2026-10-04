// Package validate 是全部字段校验规则的唯一来源。
//
// 界面不做本地校验：每敲一次键（防抖 300ms）就带着整个表单调一次 /api/validate，
// 由这里判定，错误按字段名回给前端标红。这样「界面上说的」和「程序判的」
// 不可能不一致，规则只有一份。
//
// 同一份规则还被 docs/字段说明与校验规则.md 引用。那份文档是手工同步的，
// 改了 Catalog() 记得回去改它。
//
// 校验函数的返回约定：nil 表示通过；error 的文案直接展示给用户，
// 所以每条消息都要说清哪里不对、怎么改。
package validate

import (
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"
)

// FieldSpec 是界面上一个输入框的说明，由 /api/spec 提供给前端。
//
// 前端只用 Key（按 data-help 找元素）、Purpose、Format、Example、Rules；
// Label / Group / Required 是给 docs/字段说明与校验规则.md 用的元数据，
// 那份文档的标题与「必填」行就来自它们。
type FieldSpec struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Group    string   `json:"group"`
	Purpose  string   `json:"purpose"`
	Format   string   `json:"format"`
	Example  string   `json:"example"`
	Required bool     `json:"required"`
	Rules    []string `json:"rules"`
}

// Catalog 返回全部字段的说明表。
//
// Key 必须与 ui/index.html 里 <div class="help" data-help="..."> 的取值一致，
// 对不上的字段界面上不会显示任何提示，也不会报错，属于静默失效。
//
// 这里的文字是给用户看的，不是给机器判的。真正的判定在下面的校验函数里。
func Catalog() []FieldSpec {
	return []FieldSpec{
		{
			Key: "ca_name", Label: "CA 名称", Group: "ca", Required: true,
			Purpose: "这套内部 CA 的名字。它只是给人看的标识，会写进证书的 Subject，" +
				"客户端装证书时能靠它认出「这是谁签的」。",
			Format:  "1–64 个字符，可用中英文、数字、空格、点、连字符、下划线、括号",
			Example: "我的文档站 CA",
			Rules: []string{
				"必填，长度 1–64",
				"不能以空格开头或结尾",
				"不能包含控制字符，以及 / \\ , \" ' = @ : 这些会破坏日志或 Subject 的字符",
			},
		},
		{
			Key: "ca_days", Label: "CA 有效期", Group: "ca", Required: true,
			Purpose: "CA 证书从签发日算起能用多少天。所有由它签出的证书，有效期都不能超过它。" +
				"CA 一过期，它签出去的全部证书立刻失效，所以这个值要留足。",
			Format:  "整数天数，1–7300",
			Example: "3650（10 年）",
			Rules: []string{
				"必填，整数",
				"范围 1–7300",
				"换 CA 比换服务器证书麻烦得多，建议不小于 3650",
			},
		},
		{
			Key: "server_cn", Label: "服务器名（CN）", Group: "server", Required: true,
			Purpose: "服务器证书的主体名。现代浏览器校验证书时只看 SAN、不看 CN，" +
				"所以这个值主要是给人看的标识，以及在没有 SAN 时的兜底。填服务对外的主机名最省事。",
			Format:  "1–64 个字符，主机名、IP 或一个便于识别的中文名",
			Example: "docs.example.com",
			Rules: []string{
				"必填，长度 1–64",
				"不能以空格开头或结尾",
				"不能包含控制字符，以及 / \\ , \" ' = @ :",
				"真正决定浏览器认不认的是下面的内网 / 外网地址，别只填这一个框",
			},
		},
		{
			Key: "server_internal", Label: "内网地址", Group: "server", Required: false,
			Purpose: "局域网里访问这台服务用的地址。填 IP 就进证书的 IP 字段，" +
				"填名字就进 DNS 字段——两种都支持，按你实际在浏览器里敲的那个填。" +
				"一台机器在内网可能有多个入口，所以可以写多行。",
			Format:  "每行一个，IPv4 / IPv6 或主机名",
			Example: "192.168.1.10 ／ nas.lan ／ docs",
			Rules: []string{
				"可选，但内网地址与外网地址至少要填一个",
				"每行一个，不要写端口、路径、协议前缀",
				"是 IP 就按 IP 校验；否则按主机名校验，允许单段名（如 nas）",
				"同一份证书里不能重复",
			},
		},
		{
			Key: "server_external", Label: "外网地址", Group: "server", Required: false,
			Purpose: "从公网访问这台服务用的域名。填了它，别人用这个域名打开时" +
				"浏览器才不会报证书不受信任。没有公网入口就留空。",
			Format:  "每行一个完整域名",
			Example: "docs.example.com ／ www.example.com",
			Rules: []string{
				"可选，但内网地址与外网地址至少要填一个",
				"必须是有至少两段的完整域名，不接受单段名",
				"不能带协议前缀、端口、路径",
				"不支持通配符（*.example.com）",
			},
		},
		{
			Key: "server_days", Label: "服务器证书有效期", Group: "server", Required: true,
			Purpose: "服务器证书能用多少天。到期后浏览器会直接拒绝连接，需要重新签发并替换。",
			Format:  "整数天数，1–3650",
			Example: "365",
			Rules: []string{
				"必填，整数，范围 1–3650",
				"不能超过 CA 的剩余有效期",
			},
		},
		{
			Key: "client_cn", Label: "终端名（CN）", Group: "client", Required: true,
			Purpose: "客户端证书的主体名，代表一台设备或一个人。" +
				"服务端把「有证书」等同于「有权访问」，这个值不参与授权判断，" +
				"只作为日志里的身份显示名——所以起个一眼能认出来的名字最有用。",
			Format:  "1–64 个字符，设备名、人名都行",
			Example: "我的手机 ／ 张三的笔记本",
			Rules: []string{
				"必填，长度 1–64",
				"不能以空格开头或结尾",
				"不能包含控制字符，以及 / \\ , \" ' = @ :",
				"同一个人有多个设备时建议带后缀区分，吊销时才好定位",
			},
		},
		{
			Key: "client_org", Label: "归属（O）", Group: "client", Required: false,
			Purpose: "写进证书 Subject 的 Organization 字段。用于把终端按部门或项目分组，" +
				"证书多起来之后靠它筛。不填就不写这一项。",
			Format:  "0–64 个字符",
			Example: "运维部",
			Rules: []string{
				"可选",
				"长度不超过 64",
				"字符限制同终端名",
			},
		},
		{
			Key: "client_days", Label: "客户端证书有效期", Group: "client", Required: true,
			Purpose: "客户端证书能用多少天。客户端证书是设备丢失后的主要风险点，" +
				"有效期短一些更安全，到期重新签一张即可。",
			Format:  "整数天数，1–3650",
			Example: "365",
			Rules: []string{
				"必填，整数，范围 1–3650",
				"不能超过 CA 的剩余有效期",
			},
		},
		{
			Key: "pfx_password", Label: "证书包密码", Group: "client", Required: false,
			Purpose: "导出 .pfx 时给私钥加密用的密码。Windows 双击导入 .pfx 会要求输入它。" +
				"只导出 PEM 的话用不到。",
			Format:  "6 位以上",
			Example: "自己定一个，记牢",
			Rules: []string{
				"导出 .pfx 时必填，长度至少 6",
				"不导出 .pfx 可以留空",
				"这个密码只是保护文件本身，忘掉就只能重新签发",
			},
		},
		{
			Key: "out_dir", Label: "输出目录", Group: "global", Required: true,
			Purpose: "签发出的证书、私钥、配置片段都写到这个目录。建议单独建一个，" +
				"不要和文档库混在一起——这里面有私钥。",
			Format:  "目录路径，相对路径按程序工作目录展开",
			Example: `./ics-certs`,
			Rules: []string{
				"必填",
				"目录不存在时会自动创建",
				"目录必须可写，否则签发会失败",
			},
		},
	}
}

// badNameChars 是不允许出现在名字类字段里的字符。
//
// 两个原因：一是会破坏 pkix.Name 的字符串编码，二是会让日志里的身份串产生歧义
// （服务端日志会记录证书的 Subject，名字里带逗号就会让「谁是谁」读不出来）。
const badNameChars = `/\,;"'=@:`

// checkName 是名字类字段（CA 名称、服务器名、终端名、归属）的公共校验。
//
// 查四项：非空、首尾无空格、长度上限、逐字符扫控制字符与禁用字符。
// 长度按 rune 算，名字通常是中文，按字节算会把 21 个汉字判成超长。
//
// max 由调用方给。这几个字段的上限都是 64，但语义不同，不写死在这里。
func checkName(field, s string, max int) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%s不能为空", field)
	}
	if s != strings.TrimSpace(s) {
		return fmt.Errorf("%s的首尾不能有空格", field)
	}
	if len([]rune(s)) > max {
		return fmt.Errorf("%s最多 %d 个字符，现在有 %d 个", field, max, len([]rune(s)))
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s里不能有控制字符", field)
		}
		if strings.ContainsRune(badNameChars, r) {
			return fmt.Errorf("%s里不能有 %q", field, r)
		}
	}
	return nil
}

// CAName 校验 CA 名称。
func CAName(s string) error { return checkName("CA 名称", s, 64) }

// ServerCN 校验服务器证书的主体名。
func ServerCN(s string) error { return checkName("服务器名", s, 64) }

// ClientCN 校验客户端证书的主体名（终端名）。
func ClientCN(s string) error { return checkName("终端名", s, 64) }

// Org 校验证书的 Organization 字段。空值是合法的（不写这一项）。
func Org(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return checkName("归属", s, 64)
}

// Days 校验有效期的天数：1 到 max 之间的整数。
//
// max 由调用方给：CA 是 7300（20 年），子证书是 3650（10 年）。
// 这里的 n 是 JSON 解出来的 int，界面上的空输入会变成 0，所以「至少 1 天」
// 同时承担了「必填」的职责。
func Days(field string, n, max int) error {
	if n < 1 {
		return fmt.Errorf("%s至少 1 天", field)
	}
	if n > max {
		return fmt.Errorf("%s最多 %d 天，现在填的是 %d", field, max, n)
	}
	return nil
}

// NotAfterCA 检查子证书的有效期有没有超出 CA 的剩余有效期。
//
// 必须拦：签发者过期之后，它签出的证书全部失效，即使那些证书自己的有效期还没到。
// 这种故障很难查，证书看着完好、日期也对，浏览器却一律拒绝。
//
// now 作为参数传入而不是内部取 time.Now()，是为了让判定可复现。
// caNotAfter 为零值时直接放行（调用方还没加载到 CA）。
//
// 报错文案里的「改到 %d 以内」用的是 left-1：剩余 N 天时，能签的最大天数是 N-1
// （第 N 天正好是 CA 的到期时刻，取等号不算超出，但按整天算会落在那之后）。
func NotAfterCA(field string, days int, caNotAfter, now time.Time) error {
	if caNotAfter.IsZero() {
		return nil
	}
	end := now.AddDate(0, 0, days)
	if end.After(caNotAfter) {
		left := int(caNotAfter.Sub(now).Hours() / 24)
		return fmt.Errorf("%s是 %d 天，超过 CA 的剩余有效期（还有 %d 天）。"+
			"签发者一过期，它签出去的证书就全部失效，请把天数改到 %d 以内，或者先换一张 CA",
			field, days, left, left-1)
	}
	return nil
}

// PFXPassword 校验证书包密码。
//
// need 为真（勾了导出 .pfx）时必填；不导出时留空合法。
// 上限 128 位是给实现留余量，不是密码学上的要求。
func PFXPassword(s string, need bool) error {
	if s == "" {
		if need {
			return fmt.Errorf("导出证书包（.pfx）时必须设密码，Windows 导入时会要求输入它")
		}
		return nil
	}
	if len([]rune(s)) < 6 {
		return fmt.Errorf("证书包密码至少 6 位")
	}
	if len([]rune(s)) > 128 {
		return fmt.Errorf("证书包密码最多 128 位")
	}
	return nil
}

// AddressKind 是地址被识别出来的类型，决定它进证书的哪个字段。
type AddressKind string

const (
	KindIP  AddressKind = "ip"  // 进 IPAddresses
	KindDNS AddressKind = "dns" // 进 DNSNames
)

// Address 校验单个地址，并判断它是 IP 还是域名。
//
// 判定顺序有讲究：先拦掉「明显是 URL 的东西」（协议前缀、路径、通配符），
// 再试 IP，最后才当域名判。net.ParseIP 对 "https://a.b" 这种输入只返回 nil，
// 直接落到域名分支的话，报错会变成「域名里有非法字符」，而不是「去掉协议前缀」。
//
// 各条规则的由来：
//   - 协议前缀 / 路径 / 查询串：用户从地址栏复制过来时经常带上。写进 SAN 会得到一个
//     永远匹配不上的证书，浏览器不报错，只是安静地判定证书无效。
//   - 通配符：不支持。要覆盖多个名字就逐条列出，比一个可能过宽的通配符可控。
//   - 未指定地址（0.0.0.0 / ::）：没有实际含义。
//   - 冒号：IPv6 已经被上面的 ParseIP 接走，走到这里的冒号只可能是端口。
//
// external 为真时按公网域名要求（至少两段），见 domain。
func Address(field, s string, external bool) (AddressKind, error) {
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("%s不能是空行", field)
	}
	if s != strings.TrimSpace(s) {
		return "", fmt.Errorf("%s的首尾不能有空格: %q", field, s)
	}
	if len(s) > 253 {
		return "", fmt.Errorf("%s太长了（%d 字符），主机名上限是 253", field, len(s))
	}

	if i := strings.Index(s, "://"); i >= 0 {
		return "", fmt.Errorf("%s不要带协议前缀，去掉 %q 只留主机名", field, s[:i+3])
	}
	if strings.ContainsAny(s, "/?#") {
		return "", fmt.Errorf("%s不要带路径或查询串，只填主机名: %q", field, s)
	}
	if strings.Contains(s, "*") {
		return "", fmt.Errorf("%s不支持通配符，请逐条列出实际要用的名字: %q", field, s)
	}

	if ip := net.ParseIP(s); ip != nil {
		if ip.IsUnspecified() {
			return "", fmt.Errorf("%s不能是 %s，这个地址没有实际含义", field, s)
		}
		return KindIP, nil
	}

	if strings.Contains(s, ":") {
		return "", fmt.Errorf("%s不要带端口，只填主机名: %q", field, s)
	}
	if err := domain(field, s, external); err != nil {
		return "", err
	}
	return KindDNS, nil
}

// domain 按 DNS 主机名的规则逐段校验一个域名。
//
// external 为真时要求至少两段：`intranet` 这种名字靠 DNS 搜索域补全，
// 只有内网环境解析得了，写在公网用的证书里没有意义。内网地址允许单段。
//
// 下划线单独报错而不是归到「非法字符」里：它是很常见的一类错误
// （主机名里能出现，证书里不能），单独说一句用户才知道该改什么。
func domain(field, s string, external bool) error {
	if strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return fmt.Errorf("%s的首尾不能有点: %q", field, s)
	}
	labels := strings.Split(s, ".")
	if external && len(labels) < 2 {
		return fmt.Errorf("%s要填完整域名（至少两段，如 example.com），"+
			"%q 只有一段。内网地址才允许单段名", field, s)
	}
	for _, l := range labels {
		if l == "" {
			return fmt.Errorf("%s里有连续的点: %q", field, s)
		}
		if len(l) > 63 {
			return fmt.Errorf("%s里的 %q 这一段超过 63 个字符", field, l)
		}
		if strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
			return fmt.Errorf("%s里的 %q 这一段不能以连字符开头或结尾", field, l)
		}
		for _, r := range l {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-'
			if !ok {
				if r == '_' {
					return fmt.Errorf("%s里的 %q 含有下划线。"+
						"下划线在证书里属于非法字符，浏览器会拒绝这张证书", field, l)
				}
				return fmt.Errorf("%s里不能有 %q 这个字符: %q", field, r, s)
			}
		}
	}
	return nil
}

// AddressList 校验多行地址，返回该进 IPAddresses 与 DNSNames 的两组值。
//
// 空行直接跳过。界面是多行文本框，用户粘一大段进来时首尾空行很常见。
// 域名统一转小写（DNS 不区分大小写），IP 保持原样。
//
// 去重按小写后的值比：同一份证书里出现两条一样的 SAN 没有意义，
// 而且会让「覆盖了哪些地址」变得难读。冲突时把两条原样报出来，用户才知道删哪条。
func AddressList(field string, lines []string, external bool) (ips, dns []string, err error) {
	seen := map[string]string{}
	for _, raw := range lines {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		kind, err := Address(field, s, external)
		if err != nil {
			return nil, nil, err
		}
		key := strings.ToLower(s)
		if prev, dup := seen[key]; dup {
			return nil, nil, fmt.Errorf("%s里有重复的地址: %q 和 %q", field, prev, s)
		}
		seen[key] = s
		if kind == KindIP {
			ips = append(ips, s)
		} else {
			dns = append(dns, strings.ToLower(s))
		}
	}
	return ips, dns, nil
}
