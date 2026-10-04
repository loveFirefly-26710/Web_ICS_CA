package app

import (
	"time"

	"web_ics_ca/internal/certs"
	"web_ics_ca/internal/validate"
)

// 这个文件是「界面表单」与「校验规则」之间的桥：
// 每个表单结构体负责把散装字段收齐，调 validate 包逐项检查，
// 再把错误按字段名收集成 map 交给前端（前端按 data-err 找到对应的提示框）。
//
// 字段名（cn / days / internal ...）必须与 ui/index.html 里的 data-err 一致。
// 校验规则一条都不写在这里，只有一份，在 internal/validate。

// serverForm 是「服务器证书」页签的表单。
type serverForm struct {
	CN       string   `json:"cn"`
	Internal []string `json:"internal"`
	External []string `json:"external"`
	Days     int      `json:"days"`
}

// validate 校验服务器证书表单，并顺手把地址拆成 IP 与 DNS 两组。
//
// 返回的 ips / dns 直接就是证书 SAN 要写的内容：内网的排在前，外网的排在后，
// 两边的重复检查各自独立（内网写 nas、外网写 nas 不算冲突）。
//
// ca 为 nil 表示还没有 CA，此时跳过「有效期不能超过 CA 剩余有效期」这一项：
// 没 CA 时那条无从判断，界面会在签发那一步再拦一次。
//
// 有一处联动校验：内网与外网都合法、但两边加起来一个地址都没有时，
// 把错误挂在内网字段上（用户最可能在那里补）。
func (f serverForm) validate(ca *certs.CA) (ips, dns []string, errs map[string]string) {
	errs = map[string]string{}
	if err := validate.ServerCN(f.CN); err != nil {
		errs["cn"] = err.Error()
	}

	inIPs, inDNS, err := validate.AddressList("内网地址", f.Internal, false)
	if err != nil {
		errs["internal"] = err.Error()
	}
	exIPs, exDNS, err := validate.AddressList("外网地址", f.External, true)
	if err != nil {
		errs["external"] = err.Error()
	}

	// 只在两边都没报错时才判「一个都没填」，否则用户会同时看到两条互相矛盾的提示。
	_, inBad := errs["internal"]
	_, exBad := errs["external"]
	if !inBad && !exBad && len(inIPs)+len(inDNS)+len(exIPs)+len(exDNS) == 0 {
		errs["internal"] = "内网地址与外网地址至少要填一个。" +
			"服务器证书没有 SAN 时，浏览器一律不认这张证书。"
	}

	if err := validate.Days("服务器证书有效期", f.Days, 3650); err != nil {
		errs["days"] = err.Error()
	} else if ca != nil {
		if err := validate.NotAfterCA("服务器证书有效期", f.Days, ca.Cert.NotAfter, time.Now()); err != nil {
			errs["days"] = err.Error()
		}
	}

	// 复制一份再拼接，避免把 inIPs / inDNS 的底层数组暴露给调用方。
	ips = append(append([]string(nil), inIPs...), exIPs...)
	dns = append(append([]string(nil), inDNS...), exDNS...)
	return ips, dns, errs
}

// clientForm 是「客户端证书」页签的表单。
type clientForm struct {
	CN          string `json:"cn"`
	Org         string `json:"org"`
	Days        int    `json:"days"`
	PFXPassword string `json:"pfxPassword"`
	ExportPFX   bool   `json:"exportPfx"`
}

// validate 校验客户端证书表单。
//
// PFXPassword 的必填与否取决于 ExportPFX：勾了导出才要求密码。
// 没勾导出时密码留空是合法的（也允许填了但不导出，不报错）。
func (f clientForm) validate(ca *certs.CA) map[string]string {
	errs := map[string]string{}
	if err := validate.ClientCN(f.CN); err != nil {
		errs["cn"] = err.Error()
	}
	if err := validate.Org(f.Org); err != nil {
		errs["org"] = err.Error()
	}
	if err := validate.Days("客户端证书有效期", f.Days, 3650); err != nil {
		errs["days"] = err.Error()
	} else if ca != nil {
		if err := validate.NotAfterCA("客户端证书有效期", f.Days, ca.Cert.NotAfter, time.Now()); err != nil {
			errs["days"] = err.Error()
		}
	}
	if err := validate.PFXPassword(f.PFXPassword, f.ExportPFX); err != nil {
		errs["pfxPassword"] = err.Error()
	}
	return errs
}

// caForm 是「新建 CA」页签的表单。
type caForm struct {
	Name string `json:"name"`
	Days int    `json:"days"`
}

// validate 校验新建 CA 表单。上限 7300 天（20 年）比子证书的 3650 宽，
// 因为换 CA 的代价远大于换服务器证书：每台设备都要重装信任库。
func (f caForm) validate() map[string]string {
	errs := map[string]string{}
	if err := validate.CAName(f.Name); err != nil {
		errs["name"] = err.Error()
	}
	if err := validate.Days("CA 有效期", f.Days, 7300); err != nil {
		errs["days"] = err.Error()
	}
	return errs
}
