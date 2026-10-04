// Package app 是界面与领域逻辑之间的一层：本地 HTTP 服务、会话令牌、证书库状态、
// 配置片段生成、状态文件读写。
//
// 服务只监听 127.0.0.1，端口由系统挑，外面进不来。
// 除首页外每个接口都要带会话令牌（cookie），令牌启动时随机生成，
// 只有从程序自己打开的界面拿得到。
// 证书列表每次扫磁盘、不存索引，界面反映的就是目录里真实有什么。
package app

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"web_ics_ca/internal/certs"
	"web_ics_ca/internal/validate"
)

// Server 是这套本地服务的全部状态。
type Server struct {
	store   *Store
	token   string // 会话令牌，随机生成，放在 cookie 里校验
	ui      fs.FS  // 嵌进二进制的界面资源
	quit    chan struct{}
	once    sync.Once
	Version string

	srv *http.Server
}

// New 创建服务并生成会话令牌。
//
// 此时还没监听端口，Listen 才做那件事。这样调用方可以先准备好界面资源，
// 拿到地址之后再决定用什么方式显示它（内嵌窗口 / 系统浏览器 / 只打印）。
func New(store *Store, ui fs.FS, version string) (*Server, error) {
	if store == nil {
		return nil, fmt.Errorf("store 不能为空")
	}
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return nil, fmt.Errorf("生成会话令牌失败: %w", err)
	}
	return &Server{
		store:   store,
		token:   hex.EncodeToString(tok),
		ui:      ui,
		quit:    make(chan struct{}),
		Version: version,
	}, nil
}

// Token 返回会话令牌，启动流程用它拼出带令牌的界面地址。
func (s *Server) Token() string { return s.token }

// Quit 返回一个在界面请求 /api/quit 时被关闭的 channel。
// main 把它和进程信号一起等，谁先来都算退出。
func (s *Server) Quit() <-chan struct{} { return s.quit }

// Listen 在回环地址上起服务，返回界面根地址（不含令牌）。
//
// 端口写 0 让系统挑一个空闲的。固定端口会在重复启动、端口被占用时失败，
// 而服务本来就只给本机的一个窗口用，端口是多少无所谓。
//
// 静态资源（/、/app.css、/app.js、/logo.svg）不带鉴权，/api/ 下的全部走 withAuth。
// 首页自己处理令牌：带 ?t= 就把令牌种进 cookie。
func (s *Server) Listen() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("监听回环端口失败: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/app.css", s.handleStatic("app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("/app.js", s.handleStatic("app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("/logo.svg", s.handleStatic("logo.svg", "image/svg+xml"))
	mux.HandleFunc("/api/", s.withAuth(s.handleAPI))

	s.srv = &http.Server{
		Handler: mux,
		// 只设读头超时：本机请求不会慢，而写响应要传文件，
		// 设了整体超时反而可能截断大响应。
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[ca] 服务退出: %v", err)
		}
	}()
	return "http://" + ln.Addr().String() + "/", nil
}

// Shutdown 关闭监听，不等在途请求。本机单窗口场景没有需要优雅收尾的请求。
func (s *Server) Shutdown() {
	if s.srv != nil {
		_ = s.srv.Close()
	}
}

// withAuth 包住 /api/ 下的所有处理函数，校验 cookie 里的会话令牌。
//
// 用 subtle.ConstantTimeCompare 而不是 == ：本机回环上时序攻击不现实，
// 但比较秘密值的代码统一一个写法，省得以后有人把这段抄到别处去。
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("ca_session")
		if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) != 1 {
			writeErr(w, http.StatusUnauthorized, "会话已失效，请回到程序窗口重新打开界面")
			return
		}
		next(w, r)
	}
}

// handleIndex 返回界面首页，并把会话令牌种进 cookie。
//
// 地址带 ?t=<令牌> 时种 cookie 后返回首页，这是启动时打开的地址；
// 已经带着正确 cookie 时也返回首页，刷新不掉登录；
// 两者都没有时返回一张说明页，告诉用户要从程序窗口打开。
//
// 不用重定向，是为了让用户直接看到人话，而不是一个被浏览器吞掉的跳转。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("t") == s.token {
		http.SetCookie(w, &http.Cookie{
			Name:     "ca_session",
			Value:    s.token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
	} else if c, err := r.Cookie("ca_session"); err != nil || c.Value != s.token {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>需要从程序窗口打开</title>
<body style="margin:0;display:grid;place-items:center;height:100vh;font:15px/1.7 system-ui,'Microsoft YaHei',sans-serif;color:#1d2b28;background:#f4fbf8">
<div style="text-align:center"><h1 style="font-size:17px;font-weight:600;margin:0 0 8px">这个页面需要从程序窗口打开</h1>
<p style="margin:0;color:#6f8a83">请回到命令行窗口，复制里面那行带令牌的地址再打开。</p></div>`)
		return
	}
	s.serveFile(w, r, "index.html", "text/html; charset=utf-8")
}

// handleStatic 把嵌进二进制的资源直接吐出去。
func (s *Server) handleStatic(name, ctype string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.serveFile(w, r, name, ctype)
	}
}

// serveFile 从嵌入的界面资源里读一个文件写出去。
//
// 一律带 Cache-Control: no-store。界面改一次就要立刻看到效果，
// 而资源本来就从内存里读，缓存没有收益。
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name, ctype string) {
	raw, err := fs.ReadFile(s.ui, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

// handleAPI 是 /api/ 下所有接口的分发口。接口名与 ui/app.js 里 api() 的第一个参数
// 一一对应。除 spec 外都是 POST（用 GET 也能过，但没有实际用途）。
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只支持 GET 与 POST")
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/api/") {
	case "spec":
		// 字段说明表。界面拿它渲染每个输入框下面的帮助文本。
		writeOK(w, map[string]any{"fields": validate.Catalog()})
	case "validate":
		// 实时校验。界面每次输入都来问一次，规则只在服务端有一份。
		s.handleValidate(w, r)
	case "state":
		s.handleState(w)
	case "settings":
		s.handleSettings(w, r)
	case "ca":
		s.handleCA(w, r)
	case "issue/server":
		s.handleIssueServer(w, r)
	case "issue/client":
		s.handleIssueClient(w, r)
	case "revoke":
		s.handleRevoke(w, r)
	case "unrevoke":
		s.handleUnrevoke(w, r)
	case "export/deny":
		s.handleExportDeny(w)
	case "conf":
		s.handleConf(w, r)
	case "reveal":
		s.handleReveal(w, r)
	case "quit":
		writeOK(w, map[string]any{"message": "程序即将退出"})
		// 先回响应再关 channel：反过来的话界面收不到「即将退出」这句，
		// 只能看到一个连接被掐断。
		s.once.Do(func() { close(s.quit) })
	default:
		writeErr(w, http.StatusNotFound, "没有这个接口: "+r.URL.Path)
	}
}

// certRow 是证书库里的一行：CertInfo 加几个界面要用的派生字段。
//
// Revoked 要跟 Store 里的名单比对，Expired / DaysLeft 要跟当前时间比对，
// 都不是证书自身的内容，所以留在这一层，不塞进 certs.CertInfo。
type certRow struct {
	certs.CertInfo
	Label    string `json:"label"`
	Revoked  bool   `json:"revoked"`
	Expired  bool   `json:"expired"`
	DaysLeft int    `json:"daysLeft"`
}

// stateResp 是 /api/state 的响应体，也是界面渲染一次所需的全部数据。
type stateResp struct {
	OutDir    string            `json:"outDir"`
	StorePath string            `json:"storePath"`
	Version   string            `json:"version"`
	CA        *certRow          `json:"ca"` // 没有 CA 时为 null
	Certs     []certRow         `json:"certs"`
	Deny      []certs.DenyEntry `json:"deny"`
	Conf      confResp          `json:"conf"`
}

type confResp struct {
	Snippet  string   `json:"snippet"`
	Warnings []string `json:"warnings"`
}

func (s *Server) handleState(w http.ResponseWriter) {
	writeOK(w, s.buildState())
}

// buildState 组装界面渲染所需的全部状态：扫目录、比对吊销名单、算到期情况、生成配置片段。
// 签发、吊销、改设置之后都会调它，把新状态一并回给界面，省掉一次额外的刷新请求。
//
// 列表里第一张 CA 单独放进 resp.CA，不重复出现在 resp.Certs 里
// （界面上 CA 有专门的卡片，不需要再占证书库一行）。
//
// 两个 slice 都显式初始化成空切片而不是留 nil：nil 会被 JSON 编码成 null，
// 界面那边就得多写一层判空。
func (s *Server) buildState() stateResp {
	outDir := s.store.OutDir
	resp := stateResp{OutDir: outDir, Version: s.Version}
	if p, err := storePath(); err == nil {
		resp.StorePath = p
	}
	list, err := certs.ScanDir(outDir)
	if err != nil {
		resp.Conf.Warnings = append(resp.Conf.Warnings, "扫描输出目录失败: "+err.Error())
	}
	now := time.Now()
	for _, ci := range list {
		row := certRow{
			CertInfo: ci,
			Label:    certs.KindLabel(ci.Kind),
			Revoked:  s.store.IsRevoked(ci.Fingerprint),
			Expired:  now.After(ci.NotAfter),
			// 整天数，向下取整；负数表示已经过期。
			DaysLeft: int(time.Until(ci.NotAfter).Hours() / 24),
		}
		if row.Kind == certs.KindCA && resp.CA == nil {
			resp.CA = &row
			continue
		}
		resp.Certs = append(resp.Certs, row)
	}
	if resp.Certs == nil {
		resp.Certs = []certRow{}
	}
	resp.Deny = s.store.DenyList()
	if resp.Deny == nil {
		resp.Deny = []certs.DenyEntry{}
	}

	in := confInput(outDir, firstServerCert(list), resp.CA != nil, len(resp.Deny) > 0)
	resp.Conf.Snippet = ConfSnippet(in)
	resp.Conf.Warnings = append(resp.Conf.Warnings, ConfWarnings(in)...)
	return resp
}

// confInput 汇总生成配置片段需要的事实：输出目录、服务器证书路径、
// 有没有 CA、吊销名单是不是空的。
//
// buildState 与 handleConf 都要这份东西，早先两处各算一遍，改一处漏一处。
// hasCA / hasDeny 由调用方传进来，因为 buildState 手上已经有扫描结果与名单，
// 在这里再扫一次目录纯属白费。
func confInput(outDir, serverCert string, hasCA, hasDeny bool) ConfInput {
	in := DefaultPaths(outDir, serverCert)
	if !hasCA {
		in.CAPath = ""
	}
	if !hasDeny {
		in.DenyPath = ""
	}
	return in
}

// firstServerCert 从证书列表里挑出第一张服务器证书的路径，没有则返回空串。
// 列表已由 ScanDir 按「先角色、同角色按到期时间」排好序，拿到的就是最快到期的那张。
func firstServerCert(list []certs.CertInfo) string {
	for _, c := range list {
		if c.Kind == certs.KindServer {
			return c.CertPath
		}
	}
	return ""
}

// normFingerprint 把客户端传来的指纹归一化成名单里的存储形式：
// 去掉可选的 sha256: 前缀、去掉首尾空白、转小写。
//
// 归一化只做这一处，写进名单（Revoke）与从名单里找（Unrevoke）走同一条路，
// 免得出现一边大小写敏感、一边不敏感这种只在特定输入下暴露的毛病。
func normFingerprint(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "sha256:")
	return strings.ToLower(strings.TrimSpace(s))
}

// handleSettings 设置并记住输出目录。
//
// 校验顺序是：判空、转绝对路径、建目录、试写一次。最后一步不能省，
// 目录建得出来不代表能写（只读挂载、权限不足都要到写的时候才暴露），
// 而设置一个写不了的目录，问题会拖到用户点签发才出现。
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OutDir string `json:"outDir"`
	}
	if !decode(w, r, &req) {
		return
	}
	dir := strings.TrimSpace(req.OutDir)
	if dir == "" {
		writeErr(w, http.StatusBadRequest, "输出目录不能为空")
		return
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "输出目录不是合法路径: "+err.Error())
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		writeErr(w, http.StatusBadRequest, "创建输出目录失败: "+err.Error())
		return
	}
	if !writable(abs) {
		writeErr(w, http.StatusBadRequest, "输出目录不可写: "+abs)
		return
	}
	if err := s.store.SetOutDir(abs); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存设置失败: "+err.Error())
		return
	}
	writeOK(w, s.buildState())
}

// handleCA 处理根 CA 的新建与导入，req.Action 区分。
//
// 两条路都先检查目录里是否已有 CA。直接覆盖会让已经装好旧 CA 的客户端全部连不上，
// 而且没有任何迹象指向签发环节。轮换 CA 得先把旧的挪走，这个摩擦是故意的。
func (s *Server) handleCA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action   string `json:"action"`
		Name     string `json:"name"`
		Days     int    `json:"days"`
		CertPath string `json:"certPath"`
		KeyPath  string `json:"keyPath"`
	}
	if !decode(w, r, &req) {
		return
	}
	outDir, ok := s.requireOutDir(w)
	if !ok {
		return
	}

	switch req.Action {
	case "create":
		if errs := (caForm{Name: req.Name, Days: req.Days}).validate(); len(errs) > 0 {
			writeFields(w, errs)
			return
		}
		if existing, _ := certs.FindCA(outDir); existing != nil {
			writeErr(w, http.StatusConflict,
				"输出目录里已经有一张 CA（"+existing.CN+"）。轮换 CA 时请把新 CA 放在别的目录，"+
					"或者先备份走旧的再建——直接覆盖会让已经装好旧 CA 的客户端全部连不上。")
			return
		}
		ca, err := certs.CreateCA(req.Name, req.Days, outDir)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeOK(w, map[string]any{"ca": ca.Info, "state": s.buildState()})
	case "import":
		if strings.TrimSpace(req.CertPath) == "" || strings.TrimSpace(req.KeyPath) == "" {
			writeErr(w, http.StatusBadRequest, "导入 CA 需要同时给出证书文件与私钥文件")
			return
		}
		if existing, _ := certs.FindCA(outDir); existing != nil {
			writeErr(w, http.StatusConflict,
				"输出目录里已经有一张 CA（"+existing.CN+"）。请先把它移走再导入。")
			return
		}
		ca, err := certs.ImportCA(req.CertPath, req.KeyPath, outDir)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeOK(w, map[string]any{"ca": ca.Info, "state": s.buildState()})
	default:
		writeErr(w, http.StatusBadRequest, "action 只能是 create 或 import")
	}
}

// handleIssueServer 签一张服务器证书。
//
// 顺序不能变：校验依赖 CA（要判「不超过 CA 剩余有效期」），所以先加载 CA。
// 校验不过时回 400 + fields，界面据此把每个输入框标红。
func (s *Server) handleIssueServer(w http.ResponseWriter, r *http.Request) {
	var f serverForm
	if !decode(w, r, &f) {
		return
	}
	outDir, ok := s.requireOutDir(w)
	if !ok {
		return
	}
	ca, ok := s.requireCA(w, outDir)
	if !ok {
		return
	}
	ips, dns, errs := f.validate(ca)
	if len(errs) > 0 {
		writeFields(w, errs)
		return
	}

	info, err := certs.IssueServer(ca, certs.ServerRequest{
		CN: f.CN, DNSNames: dns, IPs: ips, Days: f.Days, OutDir: outDir,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]any{"cert": info, "state": s.buildState()})
}

// handleIssueClient 签一张客户端证书（可选带 .pfx）。
func (s *Server) handleIssueClient(w http.ResponseWriter, r *http.Request) {
	var f clientForm
	if !decode(w, r, &f) {
		return
	}
	outDir, ok := s.requireOutDir(w)
	if !ok {
		return
	}
	ca, ok := s.requireCA(w, outDir)
	if !ok {
		return
	}
	if errs := f.validate(ca); len(errs) > 0 {
		writeFields(w, errs)
		return
	}

	info, err := certs.IssueClient(ca, certs.ClientRequest{
		CN: f.CN, Org: f.Org, Days: f.Days, OutDir: outDir,
		PFXPassword: f.PFXPassword,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]any{"cert": info, "state": s.buildState()})
}

// handleValidate 是界面实时校验的后端。
//
// 界面每敲一次键（防抖 300ms）就问一次，所以这个接口只做校验、不碰磁盘：
// 不读 CA 文件，只在已经有 CA 时用它的到期时间做一次交叉判断
// （requireCAQuiet 失败就当没有 CA，静默降级）。
//
// form 决定用哪个表单结构体；返回的字段名与 ui/index.html 的 data-err 对应。
func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Form        string   `json:"form"`
		CN          string   `json:"cn"`
		Org         string   `json:"org"`
		Name        string   `json:"name"`
		Internal    []string `json:"internal"`
		External    []string `json:"external"`
		Days        int      `json:"days"`
		PFXPassword string   `json:"pfxPassword"`
		ExportPFX   bool     `json:"exportPfx"`
	}
	if !decode(w, r, &req) {
		return
	}
	ca, _ := s.requireCAQuiet()

	var errs map[string]string
	switch req.Form {
	case "ca":
		errs = caForm{Name: req.Name, Days: req.Days}.validate()
	case "server":
		_, _, errs = serverForm{
			CN: req.CN, Internal: req.Internal, External: req.External, Days: req.Days,
		}.validate(ca)
	case "client":
		errs = clientForm{
			CN: req.CN, Org: req.Org, Days: req.Days,
			PFXPassword: req.PFXPassword, ExportPFX: req.ExportPFX,
		}.validate(ca)
	default:
		writeErr(w, http.StatusBadRequest, "form 只能是 ca、server 或 client")
		return
	}
	if errs == nil {
		errs = map[string]string{}
	}
	writeOK(w, map[string]any{"errors": errs})
}

// handleRevoke 把一张证书加入吊销名单，只改内存与状态文件。
//
// 真正生效还差两步：写出 deny.txt、重启 web_ics。界面上的提示会说明这一点。
// 指纹先按 64 位十六进制拦一道，免得把垃圾写进名单。
func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Fingerprint string `json:"fingerprint"`
		Note        string `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	fp := normFingerprint(req.Fingerprint)
	if len(fp) != 64 {
		writeErr(w, http.StatusBadRequest, "指纹必须是 64 位十六进制")
		return
	}
	s.store.Revoke(fp, strings.TrimSpace(req.Note))
	if err := s.store.Save(); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	writeOK(w, s.buildState())
}

// handleUnrevoke 把一张证书移出吊销名单。指纹不在名单里时返回 404。
func (s *Server) handleUnrevoke(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Fingerprint string `json:"fingerprint"`
	}
	if !decode(w, r, &req) {
		return
	}
	fp := normFingerprint(req.Fingerprint)
	if !s.store.Unrevoke(fp) {
		writeErr(w, http.StatusNotFound, "这枚指纹不在名单里")
		return
	}
	if err := s.store.Save(); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	writeOK(w, s.buildState())
}

// handleExportDeny 把当前吊销名单落成 <输出目录>/deny.txt。
//
// 这一步之后名单才从「程序内部状态」变成「web_ics 能读到的东西」。
// 名单为空时也写（得到一个只有注释头的文件），这样用户能确认路径写对了。
func (s *Server) handleExportDeny(w http.ResponseWriter) {
	outDir, ok := s.requireOutDir(w)
	if !ok {
		return
	}
	path := filepath.Join(outDir, "deny.txt")
	if err := certs.WriteDeny(path, s.store.DenyList()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]any{"path": path, "count": len(s.store.DenyList())})
}

// handleConf 按当前目录内容重新生成配置片段。
//
// 与 buildState 用的是同一套逻辑（confInput + ConfSnippet），区别只是这里允许
// 调用方指定服务器证书路径。界面没有这个入口，留着是为了脚本化调用时
// 能覆盖自动挑出来的那一张。
func (s *Server) handleConf(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerCertPath string `json:"serverCertPath"`
	}
	if !decode(w, r, &req) {
		return
	}
	outDir, ok := s.requireOutDir(w)
	if !ok {
		return
	}
	serverCert := req.ServerCertPath
	if serverCert == "" {
		list, _ := certs.ScanDir(outDir)
		serverCert = firstServerCert(list)
	}
	ca, _ := certs.FindCA(outDir)
	in := confInput(outDir, serverCert, ca != nil, len(s.store.DenyList()) > 0)
	writeOK(w, map[string]any{
		"snippet":  ConfSnippet(in),
		"warnings": ConfWarnings(in),
	})
}

// handleReveal 在系统的文件管理器里定位一个文件（证书库和结果卡片上的「打开所在文件夹」）。
//
// 已知限制：这里调的是 Windows 的 explorer，非 Windows 平台没有对应实现。
// 那条路径上 explorer 不存在，cmd.Start() 会失败，而失败被忽略，
// 于是界面显示成功、实际什么也没发生。启动流程本身有跨平台分支（openbrowser_*），
// 这个接口还没做；要用在非 Windows 上，照同样的方式补一对文件。
func (s *Server) handleReveal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !decode(w, r, &req) {
		return
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		writeErr(w, http.StatusBadRequest, "路径不能为空")
		return
	}
	if _, err := os.Stat(path); err != nil {
		writeErr(w, http.StatusNotFound, "文件不存在: "+path)
		return
	}
	// /select, 后面必须是反斜杠路径，所以 FromSlash 转换一次。
	cmd := exec.Command("explorer", "/select,"+filepath.FromSlash(path))
	_ = cmd.Start()
	writeOK(w, map[string]any{"path": path})
}

// requireOutDir 取出输出目录并保证可用，不可用时已写好响应并返回 false。
// 顺手 MkdirAll：用户可能填了一个还不存在的路径。
func (s *Server) requireOutDir(w http.ResponseWriter) (string, bool) {
	dir := s.store.OutDir
	if dir == "" {
		writeErr(w, http.StatusBadRequest, "还没有设置输出目录，请先在顶部填一个")
		return "", false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "输出目录不可用: "+err.Error())
		return "", false
	}
	return dir, true
}

// requireCA 加载签发要用的 CA，失败时写好响应并返回 false。
// 与 requireCAQuiet 的区别：这个报错给用户，那个（实时校验）静默降级。
func (s *Server) requireCA(w http.ResponseWriter, outDir string) (*certs.CA, bool) {
	ca, err := s.loadCA(outDir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return ca, true
}

// requireCAQuiet 是 requireCA 的静默版，供实时校验用。
// 没有 CA 不算错误（用户可能刚打开程序），只是「关于 CA 的那几条校验暂时跳过」。
func (s *Server) requireCAQuiet() (*certs.CA, bool) {
	ca, err := s.loadCA(s.store.OutDir)
	if err != nil {
		return nil, false
	}
	return ca, true
}

// loadCA 在输出目录里找 CA 并加载。
// 先 FindCA 定位再 LoadCA，是为了区分「找不到」和「找到了但有问题」两种提示。
func (s *Server) loadCA(outDir string) (*certs.CA, error) {
	if outDir == "" {
		return nil, fmt.Errorf("还没有设置输出目录，请先在顶部填一个")
	}
	info, _ := certs.FindCA(outDir)
	if info == nil {
		return nil, fmt.Errorf("还没有 CA。签发之前先在「根 CA」页签里生成或导入一张")
	}
	return certs.LoadCA(info.CertPath, info.KeyPath)
}

// writeFields 返回 400 与逐字段的错误，供界面把输入框标红。
//
// 与 writeErr 分开是因为形状不同：writeErr 只有一个 error 字符串，
// 这个是 {ok:false, error:"有几项需要修改", fields:{cn:"...", days:"..."}}。
func writeFields(w http.ResponseWriter, errs map[string]string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":     false,
		"error":  "有几项需要修改",
		"fields": errs,
	})
}

// decode 把请求体解析进 dst。
//
// 空请求体算成功。不少接口（state、export/deny、quit）没有参数，
// 界面统一发了空 body，这里不能因此报错。
// 判空用 errors.Is(err, io.EOF)，不比错误字符串，错误文本不是接口。
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer func() { _ = r.Body.Close() }()
	if r.Body == nil {
		return true
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeErr(w, http.StatusBadRequest, "请求内容解析失败: "+err.Error())
		return false
	}
	return true
}

// writeOK 返回成功响应：{"ok":true,"data":...}。
func writeOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
}

// writeErr 返回失败响应：{"ok":false,"error":"..."}，并带上 HTTP 状态码。
func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}
