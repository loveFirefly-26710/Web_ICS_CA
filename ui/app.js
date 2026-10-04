'use strict';

/* web-ics-ca 界面的全部前端逻辑（无框架、无构建步骤，直接跑）。
 *
 * 与服务端的分工：
 *   - 校验规则不在这一层。输入框一变就调 /api/validate 问服务端，
 *     规则只有 internal/validate 那一份，这里只负责把错误显示出来。
 *   - 字段说明也不在这一层。启动时拉一次 /api/spec，填进每个输入框下面的帮助文本。
 *   - 状态以服务端返回的 state 为准。每次改动接口都会把最新 state 一并回传，
 *     直接赋值给全局 state 再整体重绘，不做局部更新。
 *
 * 重绘入口是 renderState()：它按 state 刷新概览卡片、CA 卡片、证书库、吊销名单、
 * 配置片段。页面切换（go）与数据渲染是分开的两件事。
 */


/* ── 小工具 ───────────────────────────────────────────────── */

// $ / $$ 是 querySelector / querySelectorAll 的简写；$$ 返回真数组，方便用 forEach。
const $  = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

// spec 是 /api/spec 返回的字段说明表；state 是 /api/state 返回的当前状态，
// 也是 renderState() 的唯一数据来源。
let spec = [];
let state = null;


// api 是所有接口调用的统一入口。
//
// body 为 undefined 时发 GET，否则发 POST JSON。服务端约定：成功是
// {ok:true,data:...}，失败是 {ok:false,error:"...",fields?:{...}}。
// 失败时抛一个 Error，并把逐字段的错误挂在 err.fields 上。
// 调用方看到 fields 就调 showErrors()，没有就弹一条 toast。
async function api(path, body) {
  const res = await fetch('/api/' + path, {
    method: body === undefined ? 'GET' : 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let json;
  try {
    json = await res.json();
  } catch (e) {
    // 服务被关掉时 fetch 会拿到空响应，这里给一句人话而不是 JSON 解析错误。
    throw new Error('服务没有返回可解析的内容（HTTP ' + res.status + '）');
  }
  if (!json.ok) {
    const err = new Error(json.error || '请求失败');
    err.fields = json.fields || null;
    throw err;
  }
  return json.data;
}

// esc 转义 HTML 特殊字符。所有拼进 innerHTML 的用户数据（证书名、路径、备注）
// 都必须先过它。证书名是用户随手填的，里面完全可能有尖括号。
function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

// lines 把多行文本框的内容切成非空行数组，供地址类字段用。
function lines(value) {
  return String(value || '').split('\n').map(s => s.trim()).filter(Boolean);
}

// debounce 防抖：输入停下来 ms 毫秒后才真正调用。实时校验用它，避免每敲一个字都发请求。
function debounce(fn, ms) {
  let t;
  return function (...args) {
    clearTimeout(t);
    t = setTimeout(() => fn.apply(this, args), ms);
  };
}

// toast 在右下角弹一条浮动提示。kind 为 'bad' 时是错误样式，且停留更久（9 秒）。
function toast(title, detail, kind) {
  const el = document.createElement('div');
  el.className = 'toast ' + (kind || 'ok');
  el.innerHTML = '<b>' + esc(title) + '</b>' + (detail ? '<span>' + esc(detail) + '</span>' : '');
  $('#toasts').appendChild(el);
  setTimeout(() => el.remove(), kind === 'bad' ? 9000 : 5000);
}

// copy 复制文本到剪贴板。浏览器可能因权限拒绝，所以失败要单独提示。
async function copy(text, what) {
  try {
    await navigator.clipboard.writeText(text);
    toast('已复制', what || '');
  } catch (e) {
    toast('复制失败', '浏览器不允许访问剪贴板，请手动选中复制', 'bad');
  }
}

// fmtDate 把 ISO 时间串截成 YYYY-MM-DD。空值显示为破折号。
function fmtDate(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  const p = n => String(n).padStart(2, '0');
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate());
}

// daysText 把剩余天数说成人话。负数表示已经过期。
function daysText(n) {
  if (n < 0) return '已过期 ' + Math.abs(n) + ' 天';
  if (n === 0) return '今天到期';
  return '还剩 ' + n + ' 天';
}


/* ── 字段说明（来自 /api/spec）────────────────────────────── */

// renderSpec 把服务端给的字段说明填进页面上每个 .help[data-help] 元素。
//
// data-help 的值就是 FieldSpec.Key（如 server_cn）。对不上的元素会被跳过，
// 页面上就什么也不显示，也不会报错，属于静默失效，加字段时两边都要改。
function renderSpec() {
  const byKey = {};
  spec.forEach(f => { byKey[f.key] = f; });
  $$('.help[data-help]').forEach(el => {
    const f = byKey[el.dataset.help];
    if (!f) return;
    const rules = f.rules.map(r => '<li>' + esc(r) + '</li>').join('');
    el.innerHTML =
      '<span class="purpose">' + esc(f.purpose) + '</span>' +
      '<span class="format">格式：' + esc(f.format) + '　示例：<code>' + esc(f.example) + '</code></span>' +
      '<details><summary>校验规则</summary><ul>' + rules + '</ul></details>';
  });
}


/* ── 字段级错误的显示 ─────────────────────────────────────── */

// clearErrors 清掉某一页上所有的错误提示与标红。
function clearErrors(page) {
  $$('.err', page).forEach(el => { el.classList.remove('show'); el.textContent = ''; });
  $$('[data-field]', page).forEach(el => el.classList.remove('bad'));
}

// showErrors 按字段名把错误写到对应的提示框，并给输入框加 .bad。
//
// 字段名（cn / days / internal ...）由服务端给出，必须与页面上的
// data-err="..." 一致；对不上的错误会静默丢掉，所以两边要一起改。
function showErrors(page, errors) {
  clearErrors(page);
  Object.keys(errors || {}).forEach(key => {
    const box = $('.err[data-err="' + key + '"]', page);
    if (box) { box.textContent = errors[key]; box.classList.add('show'); }
    const input = $('[data-field="' + key + '"]', page);
    if (input) input.classList.add('bad');
  });
}


/* ── 页面切换 ─────────────────────────────────────────────── */

// go 切换到某个页签：点亮对应的导航项、显示对应的 .page。
//
// 每次切页都把滚动位置归零并把焦点交给内容区。浏览器把 PageDown 这类按键
// 发给「焦点所在元素的最近可滚祖先」，焦点停在 body 或侧栏按钮上时，
// 按键找不到主内容区，现象就是「按了没反应」。
// preventScroll 是为了不让聚焦动作把刚归零的滚动位置又带跑。
function go(page) {
  $$('.nav-item').forEach(b => b.classList.toggle('active', b.dataset.page === page));
  $$('.page').forEach(p => p.classList.toggle('active', p.id === 'page-' + page));
  $('#content').scrollTop = 0;
  $('#content').focus({ preventScroll: true });
}


/* ── 主渲染 ───────────────────────────────────────────────── */

// renderState 按当前 state 重绘整页。所有数据变更之后都调它。
function renderState() {
  const s = state;
  if (!s) return;

  $('#version').textContent = 'v' + (s.version || 'dev');
  const outInput = $('#outDir');
  // 不要覆盖用户正在输入的内容：只有在输入框没有焦点时才回填。
  if (document.activeElement !== outInput) outInput.value = s.outDir || '';

  const caRow = s.ca;
  const servers = s.certs.filter(c => c.kind === 'server');
  const clients = s.certs.filter(c => c.kind === 'client');
  const others = s.certs.filter(c => c.kind !== 'server' && c.kind !== 'client');

  // 顶栏那一排状态胶囊。
  const bits = [];
  if (!s.outDir) bits.push('<span class="pill warn">未设置输出目录</span>');
  if (caRow) bits.push('<span class="pill ca">CA：' + esc(caRow.cn) + '</span>');
  else bits.push('<span class="pill warn">尚无 CA</span>');
  bits.push('<span class="pill">证书 ' + s.certs.length + ' 张</span>');
  if (s.deny.length) bits.push('<span class="pill bad">已吊销 ' + s.deny.length + '</span>');
  $('#topStatus').innerHTML = bits.join('');

  $('#badge-store').textContent = s.certs.length;
  $('#badge-deny').textContent = s.deny.length;

  // 概览页的四张统计卡。
  statCard('#stat-ca', caRow ? caRow.cn : '未建立',
    caRow ? (caRow.expired ? 'bad' : 'ok') : 'warn',
    caRow ? (caRow.expired ? '已过期' : daysText(caRow.daysLeft)) : '先建一张根 CA');
  statCard('#stat-server', String(servers.length), servers.length ? 'ok' : 'warn',
    servers.length ? '最近到期 ' + fmtDate(soonest(servers).notAfter) : '还没有服务器证书');
  statCard('#stat-client', String(clients.length), clients.length ? 'ok' : 'warn',
    clients.length ? '最近到期 ' + fmtDate(soonest(clients).notAfter) : '还没有客户端证书');
  statCard('#stat-deny', String(s.deny.length), s.deny.length ? 'bad' : 'ok',
    s.deny.length ? '名单已生效，需重启服务' : '没有吊销任何证书');

  // 概览页的四步流程条：按「这一步有没有做完」点亮。
  $$('.flow-step').forEach(el => {
    const need = el.dataset.need;
    let done = false;
    if (need === 'ca') done = !!caRow;
    if (need === 'server') done = servers.length > 0;
    if (need === 'client') done = clients.length > 0;
    if (need === 'conf') done = !!caRow && servers.length > 0;
    el.classList.toggle('done', done);
  });

  renderNextStep(s, caRow, servers, clients);
  renderCA(caRow);
  renderStore(s, servers, clients, others);
  renderDeny(s);
  renderConf(s);
}

// soonest 返回一组证书里最早到期的那张。用来在统计卡上显示「最近到期」。
function soonest(list) {
  return list.slice().sort((a, b) => new Date(a.notAfter) - new Date(b.notAfter))[0];
}

// statCard 更新一张统计卡：数值、语义色（ok/warn/bad）、下面那行说明。
function statCard(sel, value, tone, note) {
  const el = $(sel);
  $('.stat-value', el).textContent = value;
  $('.stat-value', el).className = 'stat-value ' + tone;
  $('.stat-note', el).textContent = note;
}

// renderNextStep 生成概览页「下一步」卡片。
//
// 主提示只出一条：按「没目录 → 没 CA → 没服务器证书 → 没客户端证书 → 去贴配置」
// 的顺序取第一个未完成的。后面再附上到期相关的提醒（已过期 / 30 天内到期），
// 这两类可以和主提示同时出现。
function renderNextStep(s, caRow, servers, clients) {
  const box = $('#nextStep');
  const steps = [];
  if (!s.outDir) {
    steps.push(['先在顶部填一个证书输出目录', '建议单独建一个目录，不要和文档库混在一起——这里面有私钥。', 'ca']);
  } else if (!caRow) {
    steps.push(['去「根 CA」生成一张', '整套体系只有这一张签发者。它的私钥不要放到服务器上。', 'ca']);
  } else if (!servers.length) {
    steps.push(['签一张服务器证书', '把浏览器实际访问用的内网、外网地址填进 SAN，否则浏览器不认这张证书。', 'server']);
  } else if (!clients.length) {
    steps.push(['给要访问的设备签客户端证书', '签的时候勾上导出 .pfx，Windows 上双击就能装进证书库。', 'client']);
  } else {
    steps.push(['去「配置对接」把片段贴进 web_ics.conf', '然后在服务器上放证书、在每台设备上装 CA 与 .pfx。', 'conf']);
  }

  const expired = s.certs.filter(c => c.expired);
  const soon = s.certs.filter(c => !c.expired && c.daysLeft <= 30);
  if (expired.length) {
    steps.push(['有 ' + expired.length + ' 张证书已经过期', expired.map(c => c.cn).join('、'), 'store']);
  }
  if (soon.length) {
    steps.push(['有 ' + soon.length + ' 张证书 30 天内到期', soon.map(c => c.cn + '（' + daysText(c.daysLeft) + '）').join('、'), 'store']);
  }

  box.innerHTML = steps.map(([title, note, page]) =>
    '<div class="callout"><b>' + esc(title) + '</b><p>' + esc(note) +
    '　<a href="#" data-goto="' + page + '" style="color:var(--accent-deep)">前往 →</a></p></div>'
  ).join('');
}

// renderCA 生成「根 CA」页顶部的当前 CA 卡片；没有 CA 时显示空状态。
function renderCA(caRow) {
  const box = $('#caCurrent');
  if (!caRow) {
    box.innerHTML = '<h2>当前 CA</h2><div class="empty">还没有 CA。用下面的「新建 CA」生成一张，' +
      '或者把别处已有的 CA 导入进来。</div>';
    return;
  }
  const tone = caRow.expired ? 'bad' : 'ok';
  box.innerHTML =
    '<h2>当前 CA</h2>' +
    '<div class="kv">' +
    kv('名称', caRow.cn) +
    kv('有效期', fmtDate(caRow.notBefore) + ' 至 ' + fmtDate(caRow.notAfter) +
       ' <span class="pill ' + tone + '">' + daysText(caRow.daysLeft) + '</span>', true) +
    kv('序列号', caRow.serial, true) +
    kv('SHA-256', '<span class="mono">' + esc(caRow.fingerprint) + '</span>', true) +
    kv('证书', '<span class="path">' + esc(caRow.certPath) + '</span>', true) +
    kv('私钥', '<span class="path">' + esc(caRow.keyPath) + '</span>', true) +
    '</div>' +
    '<div class="actions" style="margin-top:14px">' +
    '<button class="btn tiny" data-copy="' + esc(caRow.fingerprint) + '">复制指纹</button>' +
    '<button class="btn tiny" data-reveal="' + esc(caRow.certPath) + '">打开所在文件夹</button>' +
    '</div>';
}

// kv 生成一行「标签 / 值」。raw 为真时 value 已是 HTML（调用方自己保证转义过），
// 为假时对 value 做转义。
function kv(label, value, raw) {
  return '<dt>' + esc(label) + '</dt><dd>' + (raw ? value : esc(value)) + '</dd>';
}

// renderStore 生成「证书库」页的表格。
//
// CA 也列进来（放在最前面），这样筛选时能在同一张表里找到所有证书。
// 筛选按名称、指纹、SAN 地址做子串匹配，不区分大小写。
function renderStore(s, servers, clients, others) {
  const filter = $('#storeFilter').value.trim().toLowerCase();
  const all = [].concat(s.ca ? [s.ca] : [], servers, clients, others);
  const rows = all.filter(c => {
    if (!filter) return true;
    return (c.cn + ' ' + c.fingerprint + ' ' + (c.dnsNames || []).join(' ') + ' ' + (c.ips || []).join(' '))
      .toLowerCase().indexOf(filter) >= 0;
  });

  if (!rows.length) {
    $('#storeTable').innerHTML = '<div class="empty">' +
      (s.outDir ? '这个目录里还没有证书。' : '先设置输出目录。') + '</div>';
    return;
  }

  const body = rows.map(c => {
    const hosts = (c.dnsNames || []).concat(c.ips || []);
    // 一行里只显示一个状态胶囊，优先级：已吊销 > 已过期 > 30 天内到期。
    const pills = [];
    pills.push('<span class="pill ' + c.kind + '">' + esc(c.label || '') + '</span>');
    if (c.revoked) pills.push('<span class="pill bad">已吊销</span>');
    else if (c.expired) pills.push('<span class="pill bad">已过期</span>');
    else if (c.daysLeft <= 30) pills.push('<span class="pill warn">' + c.daysLeft + ' 天</span>');
    return '<tr>' +
      '<td>' + pills.join(' ') + '</td>' +
      '<td>' + esc(c.cn) + (c.org ? '<div class="path">' + esc(c.org) + '</div>' : '') + '</td>' +
      '<td><span class="hosts">' + (hosts.length ? hosts.map(esc).join('<br>') : '—') + '</span></td>' +
      '<td>' + fmtDate(c.notAfter) + '<div class="path">' + daysText(c.daysLeft) + '</div></td>' +
      // 指纹只显示前 16 位，完整值靠「指纹」按钮复制。
      '<td><span class="path">' + esc(c.fingerprint.slice(0, 16)) + '…</span>' +
        (c.pfxPath ? '<div class="path">有 .pfx</div>' : '') + '</td>' +
      '<td>' +
        '<button class="btn tiny" data-copy="' + esc(c.fingerprint) + '">指纹</button> ' +
        '<button class="btn tiny" data-reveal="' + esc(c.certPath) + '">文件夹</button> ' +
        // CA 不能被吊销：吊销自己的签发者没有意义。
        (c.kind !== 'ca'
          ? (c.revoked
            ? '<button class="btn tiny" data-unrevoke="' + esc(c.fingerprint) + '">取消吊销</button>'
            : '<button class="btn tiny danger" data-revoke="' + esc(c.fingerprint) + '" data-cn="' + esc(c.cn) + '">吊销</button>')
          : '') +
      '</td></tr>';
  }).join('');

  $('#storeTable').innerHTML =
    '<table><thead><tr><th>角色</th><th>名称</th><th>覆盖地址（SAN）</th><th>到期</th><th>指纹</th><th>操作</th></tr></thead>' +
    '<tbody>' + body + '</tbody></table>';
}

// renderDeny 生成「吊销名单」页的列表，并显示 deny.txt 会被写到哪儿。
//
// 分隔符按输出目录里有没有反斜杠来选：Windows 目录用反斜杠，其余用正斜杠，
// 免得在 Linux 上显示成 .../certs\deny.txt。
function renderDeny(s) {
  const sep = s.outDir.includes('\\') ? '\\' : '/';
  $('#denyPath').textContent = s.outDir ? ('写到 ' + s.outDir + sep + 'deny.txt') : '';
  if (!s.deny.length) {
    $('#denyList').innerHTML = '<div class="empty">名单是空的，所有签出的证书都能用。</div>';
    return;
  }
  $('#denyList').innerHTML = s.deny.map(e =>
    '<div class="deny-row">' +
    '<span class="fp">sha256:' + esc(e.fingerprint) + '</span>' +
    '<span class="note">' + (e.note ? esc(e.note) : '<span style="color:var(--muted)">无备注</span>') + '</span>' +
    '<button class="btn tiny" data-unrevoke="' + esc(e.fingerprint) + '">移除</button>' +
    '</div>'
  ).join('');
}

// renderConf 填「配置对接」页的片段与警告。内容完全由服务端生成（ConfSnippet），
// 这一层只负责贴进 DOM。
function renderConf(s) {
  $('#confSnippet').textContent = s.conf.snippet || '';
  const w = s.conf.warnings || [];
  $('#confWarnings').innerHTML = w.length
    ? '<div class="warnbar"><b>还差几步</b><ul>' + w.map(x => '<li>' + esc(x) + '</li>').join('') + '</ul></div>'
    : '';
}


// refresh 重新拉一次状态并重绘。出错时只弹提示，保留上一次的画面。
async function refresh() {
  try {
    state = await api('state');
    renderState();
  } catch (e) {
    toast('读取状态失败', e.message, 'bad');
  }
}


/* ── 表单取值与实时校验 ───────────────────────────────────── */

// 三个表单取值函数：把 DOM 里的输入收集成服务端期望的形状。
// form 字段告诉服务端用哪套规则。

const serverForm = () => ({
  form: 'server',
  cn: $('#srvCN').value.trim(),
  internal: lines($('#srvInternal').value),
  external: lines($('#srvExternal').value),
  days: Number($('#srvDays').value) || 0,
});

const clientForm = () => ({
  form: 'client',
  cn: $('#cliCN').value.trim(),
  org: $('#cliOrg').value.trim(),
  days: Number($('#cliDays').value) || 0,
  pfxPassword: $('#cliPass').value,
  exportPfx: $('#cliPfx').checked,
});

const caForm = () => ({
  form: 'ca',
  name: $('#caName').value.trim(),
  days: Number($('#caDays').value) || 0,
});

// liveValidate 调一次 /api/validate 并显示结果。
//
// 校验接口本身出错时静默忽略（空 catch）：这只是输入过程中的辅助提示，
// 弹一堆 toast 反而碍事，真正提交时服务端还会再校验一遍。
async function liveValidate(pageId, getForm) {
  const page = $('#page-' + pageId);
  try {
    const data = await api('validate', getForm());
    showErrors(page, data.errors);
  } catch (e) {
  }
}

// 三个页签各自的防抖校验器，绑在输入事件上。
const vServer = debounce(() => liveValidate('server', serverForm), 300);
const vClient = debounce(() => liveValidate('client', clientForm), 300);
const vCA     = debounce(() => liveValidate('ca', caForm), 300);


/* ── 签发结果卡片 ─────────────────────────────────────────── */

// renderResult 把一次成功签发的结果渲染成卡片（去掉 .hidden 让它出现），
// 并滚动到可见位置。
function renderResult(boxSel, title, info) {
  const box = $(boxSel);
  const hosts = (info.dnsNames || []).concat(info.ips || []);
  box.classList.remove('hidden');
  box.innerHTML =
    '<div class="result-head"><span class="tick">✓</span>' + esc(title) + '</div>' +
    '<div class="kv">' +
    kv('名称', info.cn) +
    (hosts.length ? kv('覆盖地址', esc(hosts.join('、')), true) : '') +
    kv('有效期', fmtDate(info.notBefore) + ' 至 ' + fmtDate(info.notAfter), true) +
    kv('SHA-256', '<span class="mono">' + esc(info.fingerprint) + '</span>', true) +
    kv('证书', '<span class="path">' + esc(info.certPath) + '</span>', true) +
    kv('私钥', '<span class="path">' + esc(info.keyPath) + '</span>', true) +
    (info.pfxPath ? kv('证书包', '<span class="path">' + esc(info.pfxPath) + '</span>', true) : '') +
    '</div>' +
    '<div class="actions" style="margin-top:14px">' +
    '<button class="btn tiny" data-reveal="' + esc(info.certPath) + '">打开所在文件夹</button>' +
    '<button class="btn tiny" data-copy="' + esc(info.fingerprint) + '">复制指纹</button>' +
    '</div>';
  box.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
}

// busy 把按钮切成「处理中…」并禁用，避免重复提交。
// 原文案存在 dataset.label 里，恢复时取回。
function busy(btn, on) {
  btn.disabled = on;
  if (on) { btn.dataset.label = btn.textContent; btn.textContent = '处理中…'; }
  else if (btn.dataset.label) { btn.textContent = btn.dataset.label; }
}


/* ── 事件绑定 ─────────────────────────────────────────────── */

// bind 把界面上所有交互一次性绑好。启动时调一次。
function bind() {
  // 导航切换，以及内容区里「前往 →」这类跨页链接（用事件委托，因为内容是动态生成的）。
  $('#nav').addEventListener('click', e => {
    const btn = e.target.closest('.nav-item');
    if (btn) go(btn.dataset.page);
  });
  $('#content').addEventListener('click', e => {
    const a = e.target.closest('[data-goto]');
    if (a) { e.preventDefault(); go(a.dataset.goto); }
  });

  // 顶栏：应用输出目录。回车等同于点「应用」。
  $('#saveOutDir').addEventListener('click', async () => {
    const btn = $('#saveOutDir');
    busy(btn, true);
    try {
      state = await api('settings', { outDir: $('#outDir').value.trim() });
      renderState();
      toast('输出目录已应用', state.outDir);
    } catch (e) {
      toast('设置失败', e.message, 'bad');
    } finally {
      busy(btn, false);
    }
  });
  $('#outDir').addEventListener('keydown', e => { if (e.key === 'Enter') $('#saveOutDir').click(); });

  // 根 CA 页的两个页签。
  $('#caTabs').addEventListener('click', e => {
    const tab = e.target.closest('.tab');
    if (!tab) return;
    $$('.tab', $('#caTabs')).forEach(t => t.classList.toggle('active', t === tab));
    $$('.tabpane').forEach(p => p.classList.toggle('hidden', p.dataset.pane !== tab.dataset.tab));
  });

  // 实时校验：每个输入框的 input 事件都接到对应页签的防抖校验器上。
  ['#srvCN', '#srvInternal', '#srvExternal', '#srvDays'].forEach(s => $(s).addEventListener('input', vServer));
  ['#cliCN', '#cliOrg', '#cliDays', '#cliPass'].forEach(s => $(s).addEventListener('input', vClient));
  // 「导出 .pfx」开关：控制密码框的显隐，并重新校验（密码的必填与否跟着它变）。
  $('#cliPfx').addEventListener('change', () => {
    $('#pfxPassField').style.display = $('#cliPfx').checked ? '' : 'none';
    vClient();
  });
  ['#caName', '#caDays'].forEach(s => $(s).addEventListener('input', vCA));

  // 新建 CA。
  $('#createCA').addEventListener('click', async () => {
    const btn = $('#createCA');
    const page = $('#page-ca');
    busy(btn, true);
    try {
      const data = await api('ca', { action: 'create', name: caForm().name, days: caForm().days });
      state = data.state;
      renderState();
      clearErrors(page);
      toast('CA 已生成', data.ca.cn);
    } catch (e) {
      if (e.fields) showErrors(page, e.fields);
      toast('生成失败', e.message, 'bad');
    } finally {
      busy(btn, false);
    }
  });

  // 导入已有的 CA。
  $('#importCA').addEventListener('click', async () => {
    const btn = $('#importCA');
    busy(btn, true);
    try {
      const data = await api('ca', {
        action: 'import',
        certPath: $('#impCert').value.trim(),
        keyPath: $('#impKey').value.trim(),
      });
      state = data.state;
      renderState();
      toast('CA 已导入', data.ca.cn);
    } catch (e) {
      toast('导入失败', e.message, 'bad');
    } finally {
      busy(btn, false);
    }
  });

  // 签服务器证书。
  $('#issueServer').addEventListener('click', async () => {
    const btn = $('#issueServer');
    const page = $('#page-server');
    busy(btn, true);
    try {
      const f = serverForm();
      const data = await api('issue/server', {
        cn: f.cn, internal: f.internal, external: f.external, days: f.days,
      });
      state = data.state;
      renderState();
      clearErrors(page);
      renderResult('#serverResult', '服务器证书已签发', data.cert);
      toast('已签发', data.cert.certPath);
    } catch (e) {
      if (e.fields) showErrors(page, e.fields);
      toast('签发失败', e.message, 'bad');
    } finally {
      busy(btn, false);
    }
  });

  // 签客户端证书（可能同时导出 .pfx）。
  $('#issueClient').addEventListener('click', async () => {
    const btn = $('#issueClient');
    const page = $('#page-client');
    busy(btn, true);
    try {
      const f = clientForm();
      const data = await api('issue/client', {
        cn: f.cn, org: f.org, days: f.days,
        pfxPassword: f.pfxPassword, exportPfx: f.exportPfx,
      });
      state = data.state;
      renderState();
      clearErrors(page);
      renderResult('#clientResult', '客户端证书已签发', data.cert);
      toast('已签发', data.cert.certPath);
    } catch (e) {
      if (e.fields) showErrors(page, e.fields);
      toast('签发失败', e.message, 'bad');
    } finally {
      busy(btn, false);
    }
  });

  // 证书库的筛选框：只重绘，不发请求（数据已经在本地 state 里）。
  $('#storeFilter').addEventListener('input', () => renderState());

  // 写出 deny.txt。
  $('#exportDeny').addEventListener('click', async () => {
    try {
      const data = await api('export/deny', {});
      toast('名单已写出', data.path + '（' + data.count + ' 条）');
    } catch (e) {
      toast('写出失败', e.message, 'bad');
    }
  });

  $('#copyConf').addEventListener('click', () => copy($('#confSnippet').textContent, '贴进 web_ics.conf 即可'));

  $('#openOutDir').addEventListener('click', () => {
    if (!state || !state.outDir) { toast('还没有设置输出目录', '', 'bad'); return; }
    api('reveal', { path: state.outDir }).catch(e => toast('打不开', e.message, 'bad'));
  });

  // 证书库里那些动态生成的按钮（复制指纹 / 打开文件夹 / 吊销 / 取消吊销）
  // 用事件委托统一处理：它们随表格重绘，绑不到具体元素上。
  document.addEventListener('click', async e => {
    const copyBtn = e.target.closest('[data-copy]');
    if (copyBtn) { copy(copyBtn.dataset.copy, 'SHA-256 指纹'); return; }

    const revealBtn = e.target.closest('[data-reveal]');
    if (revealBtn) {
      api('reveal', { path: revealBtn.dataset.reveal })
        .catch(err => toast('打不开', err.message, 'bad'));
      return;
    }

    const revokeBtn = e.target.closest('[data-revoke]');
    if (revokeBtn) {
      const cn = revokeBtn.dataset.cn || '';
      // 用 prompt 而不是自建弹层：备注是可选的一次性输入，不值得为它写一个组件。
      const note = window.prompt('吊销「' + cn + '」\n备注（可选，只进日志与名单）:', cn);
      if (note === null) return;
      try {
        state = await api('revoke', { fingerprint: revokeBtn.dataset.revoke, note: note.trim() });
        renderState();
        toast('已加入吊销名单', '记得去「吊销名单」写出 deny.txt 并重启 web_ics');
      } catch (err) {
        toast('吊销失败', err.message, 'bad');
      }
      return;
    }

    const unrevokeBtn = e.target.closest('[data-unrevoke]');
    if (unrevokeBtn) {
      try {
        state = await api('unrevoke', { fingerprint: unrevokeBtn.dataset.unrevoke });
        renderState();
        toast('已移出吊销名单', '同样需要重新写出 deny.txt');
      } catch (err) {
        toast('操作失败', err.message, 'bad');
      }
    }
  });

  // 退出程序：服务收到 /api/quit 后会关闭自己，进程随之退出。
  // 请求可能因为服务先关而失败，那种情况同样算退出成功，所以忽略错误。
  $('#quit').addEventListener('click', async () => {
    if (!window.confirm('退出后这个界面就不可用了，需要重新启动程序。确定退出？')) return;
    try { await api('quit', {}); } catch (e) {  }
    document.body.innerHTML =
      '<div style="display:grid;place-items:center;height:100vh;font:15px/1.8 system-ui,\'Microsoft YaHei\',sans-serif;color:#1d2b28">' +
      '<div style="text-align:center"><h1 style="font-size:18px;margin:0 0 6px">程序已退出</h1>' +
      '<p style="margin:0;color:#6f8a83">这个窗口可以直接关掉了。证书文件都在输出目录里。</p></div></div>';
  });
}


/* ── 启动 ─────────────────────────────────────────────────── */

// 顺序：先绑事件（免得用户抢先操作时没反应）、再按当前开关状态摆好密码框、
// 拉字段说明、拉状态、最后落到概览页。
(async function main() {
  bind();
  $('#pfxPassField').style.display = $('#cliPfx').checked ? '' : 'none';
  try {
    const data = await api('spec');
    spec = data.fields || [];
    renderSpec();
  } catch (e) {
    toast('读取字段说明失败', e.message, 'bad');
  }
  await refresh();
  go('overview');
})();
