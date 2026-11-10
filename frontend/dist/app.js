/* PortMon 前端：原生 JS，无构建。
   数据流：Poller 推 ports:snapshot → 差量合并 → FLIP 重排动画。 */

const $ = (sel, root = document) => root.querySelector(sel);
const App = () => window.go && window.go.main && window.go.main.App;

const S = {
  entries: [],
  rowEls: new Map(),
  avatarCache: new Map(),
  selected: null,
  paused: false,
  query: '',
  pending: null,
  toastTimer: null,
  mode: 'all',        // all | watch
  watchPorts: [],     // 盯守项 [{port, note, notify}]：数组顺序就是界面顺序，持久化在 config.json
  dragging: false,    // 拖动排序中：这段时间冻结列表渲染，否则 250ms 快照会把拖拽的行拽回原位
  suppressClick: false, // 拖完那一下的 click 不算点击，别顺手打开详情
  sys: null,          // 整机 CPU/内存/磁盘
};

// 盯守项的几个小访问器。S.watchPorts 是唯一真相，端口顺序/备注/通知开关都从它出。
const wpPorts = () => S.watchPorts.map((w) => w.port);
const wpGet = (port) => S.watchPorts.find((w) => w.port === port);
const wpNote = (port) => (wpGet(port) || {}).note || '';
// 回读校验用：端口 + 备注 + 开关全比，少一项就会出现「保存成功但界面没变」
const listSig = (list) => (list || []).map((w) => `${w.port}:${w.note || ''}:${w.notify ? 1 : 0}`).join(',');

const PALETTE = ['#FF8A5B', '#4CC9F0', '#A78BFA', '#34D399', '#FBBF24', '#F472B6', '#60A5FA', '#FB923C'];
const LOCKED = new Set(['kernel', 'critical', 'system']);

/* ─────────── 小工具 ─────────── */

function hashStr(s) {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

// 进程头像：名字哈希 → 配色 + 表情，零素材但要每个进程都有辨识度
function avatarSVG(name) {
  if (S.avatarCache.has(name)) return S.avatarCache.get(name);
  if (name === '__free__') {
    const free = `<svg viewBox="0 0 32 32"><rect width="32" height="32" fill="#EFE9DE"/>
      <path d="M10 14c1.5-1.7 3.7-1.7 5 0M17 14c1.5-1.7 3.7-1.7 5 0" stroke="#9A9284" stroke-width="2" fill="none" stroke-linecap="round"/>
      <circle cx="16" cy="22" r="2" fill="#9A9284"/></svg>`;
    S.avatarCache.set(name, free);
    return free;
  }
  const h = hashStr(name || '?');
  const bg = PALETTE[h % PALETTE.length];
  const kind = (h >> 4) % 3;
  const dx = ((h >> 7) % 3) - 1;

  const mouths = [
    `<path d="M11 21.5c2 2.6 8 2.6 10 0" stroke="#2B2B2B" stroke-width="2" fill="none" stroke-linecap="round"/>`,
    `<circle cx="16" cy="21.8" r="2.1" fill="#2B2B2B"/>`,
    `<path d="M11.5 21.5h9" stroke="#2B2B2B" stroke-width="2" stroke-linecap="round"/>`,
  ];

  const svg = `<svg viewBox="0 0 32 32">
    <rect x="0" y="0" width="32" height="32" fill="${bg}"/>
    <circle cx="${11.6 + dx}" cy="13.8" r="3.7" fill="#FFFDF7"/>
    <circle cx="${20.4 + dx}" cy="13.8" r="3.7" fill="#FFFDF7"/>
    <circle cx="${12.3 + dx}" cy="14.5" r="1.85" fill="#2B2B2B"/>
    <circle cx="${21.1 + dx}" cy="14.5" r="1.85" fill="#2B2B2B"/>
    ${mouths[kind]}
  </svg>`;
  S.avatarCache.set(name, svg);
  return svg;
}

const ICON = {
  kill: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor"><path d="M9 4h6M12 4v9"/><circle cx="12" cy="15.5" r="5.5"/></svg>`,
  lock: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor"><rect x="5" y="10.5" width="14" height="9.5" rx="2.5"/><path d="M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5"/></svg>`,
  warn: `<svg viewBox="0 0 24 24" fill="none" stroke="#2B2B2B"><path d="M12 4l8.5 15H3.5z"/><path d="M12 10v4.5M12 17.2v.1"/></svg>`,
  shield: `<svg viewBox="0 0 24 24" fill="none" stroke="#2B2B2B"><path d="M12 3.5l7 3v5.2c0 4.4-3 7.6-7 8.8-4-1.2-7-4.4-7-8.8V6.5z"/><path d="M9 12l2.2 2.2L15.5 10"/></svg>`,
  bellOn: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor"><path d="M9.5 19.5a2.6 2.6 0 0 0 5 0"/><path d="M6.2 16.4h11.6l-1.5-2.3V10a4.4 4.4 0 1 0-8.8 0v4.1z"/></svg>`,
  bellOff: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor"><path d="M9.5 19.5a2.6 2.6 0 0 0 5 0"/><path d="M6.2 16.4h11.6l-1.5-2.3V10a4.4 4.4 0 1 0-8.8 0v4.1z"/><path d="M4.5 4.5l15 15"/></svg>`,
};

function toast(msg, kind = '', ms = 3600) {
  const el = $('#toast');
  el.textContent = msg;
  el.className = 'toast ' + kind;
  el.hidden = false;
  clearTimeout(S.toastTimer);
  S.toastTimer = setTimeout(() => { el.hidden = true; }, ms);
}

function fmtBytes(n) {
  if (!n) return '–';
  const u = ['B', 'KB', 'MB', 'GB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return v.toFixed(v < 10 && i > 0 ? 1 : 0) + ' ' + u[i];
}

function fmtTime(ms) {
  if (!ms) return '–';
  const d = new Date(ms);
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

/* ─────────── 快照 & 列表 ─────────── */

function applySnapshot(snap) {
  if (!snap) return;
  S.entries = snap.entries || [];
  S.paused = !!snap.paused;

  $('#statListen').textContent = snap.listen ?? '–';
  $('#statConns').textContent = snap.conns ?? '–';
  $('#statProcs').textContent = snap.procs ?? '–';

  // 监听数里包含了 WSL 桥接的端口，标一下以免和 netstat 对不上
  const wsl = snap.wsl || 0;
  $('#statListen').title = wsl ? `宿主 ${(snap.listen || 0) - wsl} + WSL ${wsl}` : '';
  $('#wslBadge').hidden = !wsl;
  $('#wslCount').textContent = wsl;

  renderChips();

  const btn = $('#btnPause');
  btn.classList.toggle('on', S.paused);
  btn.querySelector('span').textContent = S.paused ? '继续' : '暂停';

  render();
  if (S.selected != null && !$('#drawer').hidden) refreshDrawerPorts();
}

function matches(e) {
  if (!S.query) return true;
  const q = S.query;
  return String(e.port).includes(q)
    || String(e.pid).includes(q)
    || (e.proc || '').toLowerCase().includes(q)
    || (e.addr || '').includes(q);
}

function render() {
  // 拖动排序期间冻结列表：快照照收，但不重排 DOM，否则拖到一半行会飞回去
  if (S.dragging) return;

  const view = S.mode === 'watch' ? watchView() : S.entries.filter(matches);
  document.body.classList.toggle('has-notes', hasNoteColumn());
  renderList(view);
  $('#empty').hidden = view.length > 0;
  updateSegments();
}

// 备注列只展示。没有备注就不占列——改备注在顶部芯片上（单击 / 右键）
function hasNoteColumn() {
  return S.watchPorts.some((w) => w.note);
}

// 盯守视图：只展示指定端口，空闲的也占一行——这才是“固定展示”的意义
function watchView() {
  const out = [];
  for (const w of S.watchPorts) {
    const hits = S.entries.filter((e) => e.port === w.port);
    if (hits.length) out.push(...hits);
    else out.push(freeEntry(w.port));
  }
  return S.query ? out.filter(matches) : out;
}

function freeEntry(port) {
  return {
    key: `free/${port}`,
    port,
    proto: '—',
    addr: '—',
    remote: '',
    state: 'FREE',
    pid: 0,
    proc: '空闲',
    guard: '',
    guardMsg: '',
    conns: 0,
    source: 'free',
    detail: '',
  };
}

function updateSegments() {
  for (const b of document.querySelectorAll('.seg')) {
    b.classList.toggle('active', b.dataset.mode === S.mode);
  }
  $('#segAll').textContent = S.entries.length;
  $('#segWatch').textContent = S.watchPorts.length;
  $('#watchClear').hidden = S.watchPorts.length === 0;
}

/* ─────────── 整机指标（CPU / 内存 / 磁盘） ─────────── */

function applySys(st) {
  // at 为 0 表示采样器还没建立基准，先保持 “–”，不要假报 0%
  if (!st || !st.at) return;
  S.sys = st;
  $('#statCpu b').textContent = Math.round(st.cpu) + '%';
  $('#statMem b').textContent = Math.round(st.memPercent) + '%';
  $('#statDisk b').textContent = Math.round(st.diskPercent) + '%';

  // 鼠标还停在徽章上时，提示内容跟着刷新
  if (tipEl && tipBuilder) showTip(tipEl, tipBuilder);
}

function barRow(label, pct) {
  const v = Math.max(0, Math.min(100, pct || 0));
  return `<div class="trow"><b>${label}</b><span class="bar"><span style="width:${v}%"></span></span><i>${Math.round(v)}%</i></div>`;
}

function tipCpu() {
  const s = S.sys || {};
  return `<h4>CPU 占用</h4>${barRow('', s.cpu)}<div class="trow sub">最近 1 秒平均</div>`;
}

function tipMem() {
  const s = S.sys || {};
  const used = (s.memTotal || 0) - (s.memAvail || 0);
  return `<h4>物理内存</h4>${barRow('', s.memPercent)}`
    + `<div class="trow sub">已用 ${fmtBytes(used)} / ${fmtBytes(s.memTotal)}</div>`
    + `<div class="trow sub">可用 ${fmtBytes(s.memAvail)}</div>`;
}

function tipDisk() {
  const s = S.sys || {};
  const disks = s.disks || [];
  if (!disks.length) {
    return '<h4>磁盘</h4><div class="trow sub">没有固定磁盘</div>';
  }
  const rows = disks.map((d) => barRow(d.drive, d.percent)
    + `<div class="trow sub">${fmtBytes(d.total - d.free)} / ${fmtBytes(d.total)}</div>`).join('');
  return `<h4>磁盘合计 ${Math.round(s.diskPercent || 0)}%</h4>${rows}`;
}

let tipEl = null;
let tipBuilder = null;

function showTip(el, build) {
  tipEl = el;
  tipBuilder = build;
  const tip = $('#tip');
  tip.innerHTML = build();
  tip.hidden = false;

  const r = el.getBoundingClientRect();
  const tw = tip.offsetWidth;
  const th = tip.offsetHeight;
  const x = Math.max(8, Math.min(r.left + r.width / 2 - tw / 2, window.innerWidth - tw - 8));
  let y = r.bottom + 8;
  if (y + th > window.innerHeight - 8) y = Math.max(8, r.top - th - 8);
  tip.style.left = x + 'px';
  tip.style.top = y + 'px';
}

function hideTip() {
  tipEl = null;
  tipBuilder = null;
  $('#tip').hidden = true;
}

function buildRow(e) {
  const el = document.createElement('div');
  el.className = 'row';
  el.dataset.key = e.key;
  el.innerHTML = `
    <span class="c-accent"></span>
    <span class="c-port"><b></b><i></i></span>
    <span class="c-addr"></span>
    <span class="c-proc"><span class="avatar"></span><span class="meta"><b></b><i></i></span></span>
    <span class="c-note"></span>
    <span class="c-conns"></span>
    <span class="c-act"></span>`;

  const act = $('.c-act', el);

  const kill = document.createElement('button');
  kill.className = 'pill kill';
  kill.innerHTML = ICON.kill + '<span>结束</span>';
  kill.addEventListener('click', (ev) => {
    ev.stopPropagation();
    openConfirm(Number(el.dataset.pid));
  });

  const lock = document.createElement('span');
  lock.className = 'pill locked';
  lock.innerHTML = ICON.lock + '<span class="lbl">禁杀</span>';

  const idle = document.createElement('span');
  idle.className = 'pill free';
  idle.innerHTML = '<span>空闲</span>';

  act.append(kill, lock, idle);

  // 按住即拖：超过 5px 才算拖，否则仍然是点击
  el.addEventListener('pointerdown', onRowPointerDown);

  el.addEventListener('click', () => {
    if (S.suppressClick) return; // 刚拖完，这一下不是点击
    if (el.dataset.state === 'FREE') return;
    if (el.dataset.source === 'wsl') {
      toast(el.dataset.detail || 'WSL 内部的监听端口，请在 WSL 内处理', 'warn', 4200);
      return;
    }
    openDetail(Number(el.dataset.pid));
  });
  return el;
}

function updateRow(el, e) {
  const isFree = e.state === 'FREE';
  const isWSL = e.source === 'wsl';
  const locked = LOCKED.has(e.guard);

  el.dataset.pid = e.pid;
  el.dataset.port = e.port;
  el.dataset.state = e.state;
  el.dataset.guard = e.guard || '';
  el.dataset.source = e.source || 'win';
  el.dataset.detail = e.detail || '';

  $('.c-port b', el).textContent = e.port;
  $('.c-port i', el).textContent = e.proto;
  $('.c-addr', el).textContent = e.addr;
  $('.c-proc .avatar', el).innerHTML = avatarSVG(isFree ? '__free__' : e.proc);
  $('.c-proc .meta b', el).textContent = isFree ? '空闲' : (e.proc || ('PID ' + e.pid));
  $('.c-proc .meta i', el).textContent = isFree
    ? '没有程序在监听'
    : isWSL
      ? (e.detail || 'WSL 内部')
      : '#' + e.pid;

  const conns = $('.c-conns', el);
  conns.textContent = isFree ? '·' : (e.conns || '·');
  conns.classList.toggle('zero', isFree || !e.conns);

  el.querySelector('.pill.kill').hidden = isFree || isWSL || locked;
  const lockEl = el.querySelector('.pill.locked');
  lockEl.hidden = isFree || !(isWSL || locked);
  lockEl.querySelector('.lbl').textContent = isWSL ? 'WSL' : '禁杀';
  el.querySelector('.pill.free').hidden = !isFree;

  el.title = isFree ? '' : ((isWSL || locked) ? (e.guardMsg || '') : '');
  el.classList.toggle('active', !isFree && S.selected === e.pid);

  // 备注列：只展示，不可点。注意不能 hidden——备注格一旦不参与布局，
  // 这一行就只剩 6 个格子却占着 7 条轨道，后面所有列会整体错位。
  const note = wpNote(e.port);
  const noteEl = $('.c-note', el);
  noteEl.textContent = note;
  noteEl.title = note ? `${e.port}：${note}` : '';
}

function renderList(view) {
  const rowsEl = $('#rows');
  // 滚动位置必须自己守恒：重新插入行时浏览器会做滚动锚定，导致列表莫名上移
  const keepScroll = rowsEl.scrollTop;

  const before = new Map();
  for (const [k, el] of S.rowEls) {
    if (el.isConnected) before.set(k, el.getBoundingClientRect().top);
  }

  const wanted = new Set(view.map((e) => e.key));
  for (const [k, el] of [...S.rowEls]) {
    if (!wanted.has(k)) {
      el.remove();
      S.rowEls.delete(k);
    }
  }

  let cursor = rowsEl.firstElementChild;
  const fresh = [];
  for (const e of view) {
    let el = S.rowEls.get(e.key);
    if (!el) {
      el = buildRow(e);
      S.rowEls.set(e.key, el);
      fresh.push(el);
    }
    updateRow(el, e);

    if (cursor === el) {
      cursor = cursor.nextElementSibling;
    } else {
      rowsEl.insertBefore(el, cursor);
    }
  }

  // FLIP：位移用 transform，不碰布局属性
  for (const [k, el] of S.rowEls) {
    const prevTop = before.get(k);
    if (prevTop == null) continue;
    const dy = prevTop - el.getBoundingClientRect().top;
    if (Math.abs(dy) > 1) {
      el.animate(
        [{ transform: `translateY(${dy}px)` }, { transform: 'none' }],
        { duration: 240, easing: 'cubic-bezier(.34,1.56,.64,1)' },
      );
    }
  }

  for (const el of fresh) {
    el.classList.add('enter', 'flash');
    setTimeout(() => el.classList.remove('flash'), 1300);
  }

  rowsEl.scrollTop = keepScroll;
}

/* ─────────── 详情抽屉 ─────────── */

async function openDetail(pid) {
  if (!pid) return;
  S.selected = pid;

  const entry = S.entries.find((e) => e.pid === pid);
  $('#dName').textContent = entry ? entry.proc || ('PID ' + pid) : 'PID ' + pid;
  $('#dPid').textContent = '#' + pid;
  $('#dAvatar').innerHTML = avatarSVG(entry ? entry.proc : '?');
  $('#drawer').hidden = false;
  $('#dBody').innerHTML = '<div class="kv"><span>加载中</span><div>正在读取进程信息…</div></div>';

  for (const el of S.rowEls.values()) el.classList.toggle('active', Number(el.dataset.pid) === pid);

  const api = App();
  let d = null;
  if (api) {
    try { d = await api.GetDetail(pid); } catch (err) { d = null; }
  }
  if (!d) {
    renderDrawer({ pid, name: entry?.proc || '', accessDenied: true }, entry);
    return;
  }
  renderDrawer(d, entry);
}

function refreshDrawerPorts() {
  const box = $('#dPorts');
  if (!box) return;
  const ports = S.entries.filter((e) => e.pid === S.selected);
  box.innerHTML = ports.length
    ? ports.map((e) => `<span>${e.port}/${e.proto}</span>`).join('')
    : '<span style="opacity:.5">已释放</span>';
}

function renderDrawer(d, entry) {
  const guard = entry?.guard || '';
  const guardMsg = entry?.guardMsg || '';
  const ports = S.entries.filter((e) => e.pid === d.pid);

  const rows = [];
  if (d.accessDenied) {
    rows.push(`<div class="guardbox">${ICON.warn}<div>拿不到完整信息：这个进程属于其他用户，需要管理员权限才能看到命令行和工作目录。端口和进程名仍然准确。</div></div>`);
  }
  if (guardMsg) {
    const cls = guard === 'risky' ? 'risky' : guard === 'wsl' ? 'wsl' : '';
    const icon = guard === 'risky' || guard === 'wsl' ? ICON.warn : ICON.shield;
    rows.push(`<div class="guardbox ${cls}">${icon}<div>${escapeHTML(guardMsg)}</div></div>`);
  }

  rows.push(kv('占用端口', `<div class="ports" id="dPorts">${
    ports.length ? ports.map((p) => `<span>${p.port}/${p.proto}</span>`).join('') : '<span style="opacity:.5">已释放</span>'
  }</div>`));
  rows.push(kv('命令行', d.cmdLine || '<span style="opacity:.45">不可用</span>', 'plain'));
  rows.push(kv('可执行文件', d.exePath || '<span style="opacity:.45">不可用</span>'));
  rows.push(kv('所属用户', d.owner || '–', 'plain'));
  rows.push(kv('父进程', d.parentPid ? `${d.parentName || '未知'} (#${d.parentPid})` : '–', 'plain'));
  rows.push(kv('启动时间', fmtTime(d.startedAt), 'plain'));
  rows.push(kv('内存占用', `${fmtBytes(d.workingSet)} / 私有 ${fmtBytes(d.privateBytes)}`, 'plain'));
  rows.push(kv('会话 ID', String(d.sessionId ?? '–'), 'plain'));

  $('#dBody').innerHTML = rows.join('');

  const killBtn = $('#dKill');
  const locked = LOCKED.has(guard);
  killBtn.disabled = locked;
  killBtn.textContent = locked ? '受系统保护，不可结束' : '结束进程';
  killBtn.title = locked ? guardMsg : '';
}

function kv(label, value, cls = '') {
  return `<div class="kv"><span>${label}</span><div class="${cls}">${value}</div></div>`;
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

/* ─────────── 结束进程 ─────────── */

async function openConfirm(pid) {
  const entry = S.entries.find((e) => e.pid === pid);
  const name = entry?.proc || ('PID ' + pid);
  const guard = entry?.guard || '';

  if (LOCKED.has(guard)) {
    toast(entry?.guardMsg || '这是系统保护进程，只能查看不能结束', 'err', 5000);
    return;
  }

  const ports = S.entries.filter((e) => e.pid === pid);
  S.pending = { pid, name };

  $('#mTitle').textContent = `结束 ${name}？`;
  $('#mDesc').innerHTML = `PID <b>${pid}</b> 正在占用 ${ports.length} 个端口：` +
    ports.map((p) => `<b>${p.port}</b>`).join('、') + '。';
  $('#mCmd').hidden = true;
  $('#mCmd').textContent = '';
  $('#mOk').hidden = false;
  $('#mOk').textContent = '确认结束';
  $('#mOk').disabled = false;
  $('#mTreeWrap').hidden = false;

  const warn = $('#mWarn');
  if (entry?.guardMsg) {
    warn.hidden = false;
    warn.innerHTML = ICON.warn + `<div>${escapeHTML(entry.guardMsg)}</div>`;
  } else {
    warn.hidden = false;
    warn.innerHTML = ICON.warn + '<div>未保存的数据会直接丢失，且进程不会自己恢复。确认是你要结束的那个再点。</div>';
  }

  $('#mMascot').innerHTML = worriedMascot();
  $('#overlay').hidden = false;

  const api = App();
  if (api) {
    try {
      const d = await api.GetDetail(pid);
      if (d && d.cmdLine && S.pending?.pid === pid) {
        $('#mCmd').textContent = d.cmdLine;
        $('#mCmd').hidden = false;
      }
    } catch { /* 拿不到命令行不影响结束 */ }
  }
}

// 清空盯守列表：备注和通知开关也会丢，必须先问一句
function openClearConfirm() {
  if (!S.watchPorts.length) return;

  S.pending = { clear: true };
  $('#mTitle').textContent = `清空 ${S.watchPorts.length} 个盯守端口？`;
  $('#mDesc').innerHTML = '端口备注和「是否加入通知」的开关也会一起删掉。正在监听的端口不受影响，只是不再盯守。';
  $('#mCmd').hidden = true;
  $('#mCmd').textContent = '';
  $('#mTreeWrap').hidden = true;
  $('#mOk').hidden = false;
  $('#mOk').textContent = '确认清空';
  $('#mOk').disabled = false;

  const warn = $('#mWarn');
  warn.hidden = false;
  warn.innerHTML = ICON.warn + `<div>清掉之后要重新在输入框里添加：${S.watchPorts.map((w) => w.port).join(', ')}</div>`;

  $('#mMascot').innerHTML = worriedMascot();
  $('#overlay').hidden = false;
}

function worriedMascot() {
  return `<svg viewBox="0 0 64 64">
    <rect x="6" y="8" width="52" height="50" rx="17" fill="#FF8A5B" stroke="#2B2B2B" stroke-width="4"/>
    <circle cx="24" cy="30" r="7" fill="#FFFDF7"/><circle cx="40" cy="30" r="7" fill="#FFFDF7"/>
    <circle cx="25.5" cy="31.5" r="3.5" fill="#2B2B2B"/><circle cx="41.5" cy="31.5" r="3.5" fill="#2B2B2B"/>
    <path d="M25 46c3-3.4 11-3.4 14 0" stroke="#2B2B2B" stroke-width="4" fill="none" stroke-linecap="round"/>
  </svg>`;
}

async function doKill() {
  if (!S.pending) return;
  const api = App();
  if (!api) { toast('后端还没就绪', 'err'); return; }

  const { pid, name } = S.pending;
  const tree = $('#mTree').checked;
  $('#mOk').disabled = true;
  $('#mOk').textContent = '结束中…';

  let res;
  try {
    res = await api.Kill(pid, tree);
  } catch (err) {
    toast('调用失败：' + err, 'err');
    $('#mOk').disabled = false;
    $('#mOk').textContent = '确认结束';
    return;
  }

  if (res.code === 'denied' && res.needAdmin) {
    showElevate(res.message, pid, name, tree);
    return;
  }
  finishKill(res, name);
}

function showElevate(msg, pid, name, tree) {
  $('#mTitle').textContent = '需要管理员权限';
  $('#mDesc').innerHTML = escapeHTML(msg) + '<br>要结束系统或其他用户的进程，必须请求一次管理员授权。';
  $('#mWarn').hidden = true;
  $('#mTreeWrap').hidden = true;
  $('#mOk').hidden = false;
  $('#mOk').textContent = '以管理员身份重试';
  $('#mOk').disabled = false;
  S.pending = { pid, name, elevate: true, tree };
}

async function doElevatedKill() {
  const api = App();
  const { pid, name, tree } = S.pending;
  $('#mOk').disabled = true;
  $('#mOk').textContent = '等待授权…';

  let res;
  try {
    res = await api.KillElevated(pid, tree);
  } catch (err) {
    toast('提权调用失败：' + err, 'err');
    return;
  }
  finishKill(res, name);
}

function finishKill(res, name) {
  closeModal();
  if (res.code === 'ok') {
    if (res.freed) {
      toast(`已结束 ${name}，端口已释放`, 'ok');
    } else {
      toast(`已结束 ${name}，但端口还在：${res.message}`, 'warn', 6000);
    }
    if (S.selected && res.killed && res.killed.includes(S.selected)) closeDrawer();
  } else if (res.code === 'blocked') {
    toast(res.message || '该进程受系统保护，不可结束', 'err', 6000);
  } else if (res.code === 'gone') {
    toast('进程已经不在了，端口列表会自动刷新', 'warn');
  } else if (res.code === 'denied') {
    toast(res.message || '权限不足', 'err', 5000);
  } else {
    toast(res.message || '结束失败', 'err', 5000);
  }
}

function closeModal() {
  $('#overlay').hidden = true;
  S.pending = null;
}

function closeDrawer() {
  $('#drawer').hidden = true;
  S.selected = null;
  for (const el of S.rowEls.values()) el.classList.remove('active');
}

/* ─────────── 盯守端口 ─────────── */

// chip 元素按端口复用：整块重建会让滚动位置每次占用翻转都跳回最左
const chipEls = new Map();

function buildChip(port) {
  const chip = document.createElement('span');
  chip.className = 'chip';
  chip.dataset.port = String(port);

  const label = document.createElement('span');
  label.className = 'clabel';

  const x = document.createElement('button');
  x.textContent = '×';
  x.title = `取消盯守 ${port}`;
  x.addEventListener('click', (ev) => {
    ev.stopPropagation();
    removePort(port);
  });

  // 系统通知开关：一键切，不藏在弹框里
  const bell = document.createElement('button');
  bell.className = 'bell';
  bell.addEventListener('click', (ev) => {
    ev.stopPropagation();
    const w = wpGet(port);
    if (w) updatePort(port, { notify: !w.notify });
  });

  chip.append(label, bell, x);
  chip.addEventListener('click', () => openNotePop(port, chip));
  chip.addEventListener('contextmenu', (ev) => {
    ev.preventDefault();
    openCtxMenu(ev.clientX, ev.clientY, port);
  });
  return chip;
}

function updateChip(chip, w) {
  const used = S.entries.some((e) => e.port === w.port);
  const sig = `${used ? 1 : 0}${w.notify ? 1 : 0}|${w.note || ''}`;
  if (chip.dataset.sig === sig) return; // 250ms 一次调用，没变就不碰 DOM
  chip.dataset.sig = sig;

  chip.classList.toggle('used', used);
  chip.classList.toggle('free', !used);
  chip.classList.toggle('mute', !w.notify);
  chip.title = [
    used ? `${w.port} 正被占用` : `${w.port} 当前空闲`,
    w.note || '',
    w.notify ? '系统通知开' : '系统通知已关',
    '单击改备注 · 右键更多',
  ].filter(Boolean).join(' · ');

  const bell = chip.querySelector('button.bell');
  if (bell) {
    bell.innerHTML = w.notify ? ICON.bellOn : ICON.bellOff;
    bell.title = w.notify ? '系统通知：开（点一下关掉）' : '系统通知：关（点一下打开）';
  }

  const label = chip.querySelector('.clabel');
  label.innerHTML = `<b>${w.port}</b>` + (w.note ? `<i>${escapeHTML(w.note)}</i>` : '');
}

function renderChips() {
  const box = $('#chips');
  if (!box) return;

  const keepScroll = box.scrollLeft;
  const wanted = new Set(S.watchPorts.map((w) => w.port));

  for (const [port, chip] of [...chipEls]) {
    if (!wanted.has(port)) {
      chip.remove();
      chipEls.delete(port);
    }
  }

  let cursor = box.firstElementChild;
  for (const w of S.watchPorts) {
    let chip = chipEls.get(w.port);
    if (!chip) {
      chip = buildChip(w.port);
      chipEls.set(w.port, chip);
    }
    updateChip(chip, w);

    if (cursor === chip) cursor = cursor.nextElementSibling;
    else box.insertBefore(chip, cursor);
  }

  box.scrollLeft = keepScroll;
  renderChipBar();
}

// 横向滚动条 + “还有更多”的渐隐 + 总数徽章。全部自己算，不依赖原生滚动条
// ——Windows 开了“自动隐藏滚动条”时原生 overlay 滚动条会看不见，那条得永远在。
function renderChipBar() {
  const box = $('#chips');
  const rail = $('#chipRail');
  const thumb = $('#chipThumb');
  const badge = $('#chipCount');
  if (!box || !rail || !thumb || !badge) return;

  const over = box.scrollWidth - box.clientWidth;
  const overflow = over > 1;

  box.classList.toggle('more', overflow && box.scrollLeft < over - 1);
  box.classList.toggle('less', box.scrollLeft > 1);

  badge.hidden = !overflow;
  badge.textContent = `${S.watchPorts.length} 个`;
  badge.title = '盯守端口总数，滚轮或拖下方滚动条看全部';

  rail.hidden = !overflow;
  if (!overflow) return;

  const trackW = rail.clientWidth;
  const thumbW = Math.max(30, Math.round(trackW * box.clientWidth / box.scrollWidth));
  thumb.style.width = thumbW + 'px';
  thumb.style.left = Math.round((trackW - thumbW) * (box.scrollLeft / over)) + 'px';
}

async function addPorts(text) {
  const nums = text
    .split(/[,，\s]+/)
    .map((s) => parseInt(s, 10))
    .filter((n) => n >= 1 && n <= 65535);

  if (!nums.length) {
    toast('请输入 1-65535 的端口号，多个用逗号隔开', 'warn');
    return;
  }

  const merged = [...S.watchPorts];
  const have = new Set(wpPorts());
  for (const n of nums) {
    if (have.has(n)) continue;
    merged.push({ port: n, note: '', notify: true });
    have.add(n);
  }

  $('#watchInput').value = '';
  await savePorts(merged);
  setMode('watch');
}

async function removePort(port) {
  await savePorts(S.watchPorts.filter((w) => w.port !== port));
}

// 改一条（备注 / 通知开关），其余项顺序原样保留
async function updatePort(port, patch) {
  await savePorts(S.watchPorts.map((w) => (w.port === port ? { ...w, ...patch } : w)));
}

// 拖动排序与右键“上移/下移/置顶”共用的出口
async function movePort(port, toIndex) {
  const cur = wpGet(port);
  if (!cur) return;
  const rest = S.watchPorts.filter((w) => w.port !== port);
  const at = Math.max(0, Math.min(toIndex, rest.length));
  rest.splice(at, 0, cur);
  if (listSig(rest) === listSig(S.watchPorts)) return;
  await savePorts(rest);
}

async function savePorts(list) {
  const api = App();
  if (!api) {
    toast('后端未就绪，改动没有保存', 'err', 5000);
    return;
  }

  S.watchPorts = list;
  renderChips();
  render();

  try {
    await api.SetWatchPorts(list);
  } catch (err) {
    toast('保存盯守端口失败：' + err, 'err', 6000);
    return;
  }

  // 回读校验：写没写进去必须有确定答案，不允许静默失败
  try {
    const persisted = normalizeList(await api.GetWatchPorts());
    if (listSig(persisted) !== listSig(normalizeList(list))) {
      S.watchPorts = persisted;
      renderChips();
      render();
      toast('保存没生效，已回滚到磁盘上的配置', 'err', 6000);
    }
  } catch { /* 回读失败不影响主流程 */ }

  // 清空后还留在盯守视图会看到空列表，退回全部
  if (!S.watchPorts.length && S.mode === 'watch') S.mode = 'all';
  render();
}

function setMode(mode) {
  S.mode = mode;
  render();
}

/* ─────────── 备注编辑气泡 ─────────── */

// 不用 window.prompt：WebView2 对同步原生对话框支持不确定，样式也割裂
let notePort = 0;

function openNotePop(port, anchor) {
  const w = wpGet(port);
  if (!w || !anchor) return;
  closeCtxMenu();

  notePort = port;
  $('#npPort').textContent = port;
  $('#npInput').value = w.note || '';
  $('#npNotify').checked = !!w.notify;

  const pop = $('#notePop');
  pop.hidden = false;
  placeNotePop(anchor, pop);

  const input = $('#npInput');
  input.focus();
  input.select();
}

function placeNotePop(anchor, pop) {
  const r = anchor.getBoundingClientRect();
  const w = pop.offsetWidth;
  const h = pop.offsetHeight;

  let x = r.left;
  if (x + w > window.innerWidth - 10) x = r.right - w;
  x = Math.max(10, Math.min(x, window.innerWidth - w - 10));

  let y = r.bottom + 8;
  if (y + h > window.innerHeight - 10) y = Math.max(10, r.top - h - 8);

  pop.style.left = x + 'px';
  pop.style.top = y + 'px';
}

function closeNotePop() {
  if ($('#notePop').hidden) return;
  $('#notePop').hidden = true;
  notePort = 0;
}

async function saveNote() {
  if (!notePort) return;
  const port = notePort;
  const note = $('#npInput').value.replace(/\s+/g, ' ').trim().slice(0, 24);
  const notify = $('#npNotify').checked;
  const w = wpGet(port);
  closeNotePop();
  if (!w) return;
  if ((w.note || '') === note && !!w.notify === notify) return;
  await updatePort(port, { note, notify });
}

async function clearNote() {
  if (!notePort) return;
  const port = notePort;
  const notify = $('#npNotify').checked;
  closeNotePop();
  await updatePort(port, { note: '', notify });
}

/* ─────────── chip 右键菜单（上移 / 下移 / 备注 / 取消） ─────────── */

function openCtxMenu(x, y, port) {
  const menu = $('#ctxMenu');
  const at = S.watchPorts.findIndex((w) => w.port === port);
  menu.innerHTML = '';

  const item = (label, fn, cls = '') => {
    const b = document.createElement('button');
    b.textContent = label;
    if (cls) b.className = cls;
    b.addEventListener('click', () => {
      closeCtxMenu();
      fn();
    });
    menu.append(b);
    return b;
  };

  item('备注…', () => openNotePop(port, chipEls.get(port) || $('#chips')));
  item('上移', () => movePort(port, at - 1));
  item('下移', () => movePort(port, at + 1));
  item('置顶', () => movePort(port, 0));
  menu.append(document.createElement('hr'));
  item('取消盯守', () => removePort(port), 'danger');

  menu.hidden = false;
  const r = menu.getBoundingClientRect();
  menu.style.left = Math.max(8, Math.min(x, window.innerWidth - r.width - 8)) + 'px';
  menu.style.top = Math.max(8, Math.min(y, window.innerHeight - r.height - 8)) + 'px';
}

function closeCtxMenu() {
  $('#ctxMenu').hidden = true;
}

/* ─────────── 盯守列表拖动排序 ─────────── */

// pending = 按下但还没过阈值（这段时间算点击）；active = 真的在拖
const D = { pending: false, active: false, port: 0, startY: 0, lastY: 0, toIndex: 0, group: [], timer: 0 };

function canDrag() {
  return S.mode === 'watch' && !S.query && S.watchPorts.length > 1;
}

// 一个端口可能占多行（IPv4 + IPv6 同时监听）。排序粒度是端口，整组一起搬。
function rowGroups() {
  const out = [];
  let cur = null;
  for (const el of $('#rows').children) {
    if (!el.classList.contains('row')) continue;
    const port = Number(el.dataset.port);
    if (!cur || cur.port !== port) {
      cur = { port, top: el.offsetTop, bottom: el.offsetTop + el.offsetHeight };
      out.push(cur);
    } else {
      cur.bottom = el.offsetTop + el.offsetHeight;
    }
  }
  return out;
}

function rowsOfPort(port) {
  return [...$('#rows').children]
    .filter((el) => el.classList.contains('row') && Number(el.dataset.port) === port);
}

function onRowPointerDown(ev) {
  if (!canDrag() || ev.button !== 0) return;
  if (ev.target.closest('button, .pill')) return;
  D.pending = true;
  D.port = Number(ev.currentTarget.dataset.port);
  D.startY = ev.clientY;
  D.lastY = ev.clientY;
}

function onPointerMove(ev) {
  if (!D.pending && !D.active) return;
  if (!D.active) {
    if (Math.abs(ev.clientY - D.startY) < 5) return; // 阈值内仍是点击
    startDrag();
  }
  D.lastY = ev.clientY;
  ev.preventDefault();
  moveDrag();
}

function startDrag() {
  D.active = true;
  D.pending = false;
  S.dragging = true; // 冻结列表渲染

  D.group = rowsOfPort(D.port);
  for (const el of D.group) el.classList.add('dragging');
  D.toIndex = S.watchPorts.findIndex((w) => w.port === D.port);

  const line = document.createElement('div');
  line.className = 'dropline';
  line.id = 'dropline';
  line.style.top = '0px';
  $('#rows').append(line);
  document.body.classList.add('row-dragging');

  D.timer = setInterval(autoScroll, 40);
}

function autoScroll() {
  if (!D.active) return;
  const box = $('#rows');
  const rect = box.getBoundingClientRect();
  let d = 0;
  if (D.lastY < rect.top + 42) d = -12;
  else if (D.lastY > rect.bottom - 42) d = 12;
  if (!d) return;

  const before = box.scrollTop;
  box.scrollTop = before + d;
  if (box.scrollTop !== before) moveDrag();
}

function moveDrag() {
  const dy = D.lastY - D.startY;
  for (const el of D.group) el.style.transform = `translateY(${dy}px)`;

  const box = $('#rows');
  const rect = box.getBoundingClientRect();
  const groups = rowGroups().filter((g) => g.port !== D.port);

  // 落点只看其它组的垂直中线，拖拽组自己排除在外，坐标才稳定
  let toIndex = 0;
  groups.forEach((g, i) => {
    const mid = rect.top - box.scrollTop + g.top + (g.bottom - g.top) / 2;
    if (D.lastY > mid) toIndex = i + 1;
  });
  D.toIndex = toIndex;

  const line = $('#dropline');
  if (line) {
    let top = 0;
    if (groups.length) {
      top = toIndex === 0 ? groups[0].top - 4 : groups[toIndex - 1].bottom + 2;
    }
    line.style.top = Math.max(0, top) + 'px';
  }
}

function onPointerUp() {
  if (!D.pending && !D.active) return;
  if (!D.active) {
    D.pending = false; // 没过阈值：交给 click 打开详情
    return;
  }
  finishDrag(true);
}

function onPointerCancel() {
  if (!D.active) {
    D.pending = false;
    return;
  }
  finishDrag(false);
}

function finishDrag(commit) {
  const port = D.port;
  const toIndex = D.toIndex;

  clearInterval(D.timer);
  D.timer = 0;
  for (const el of D.group) {
    el.classList.remove('dragging');
    el.style.transform = '';
  }
  $('#dropline')?.remove();
  document.body.classList.remove('row-dragging');
  D.active = false;
  D.pending = false;
  D.group = [];
  S.dragging = false;

  // 松手那一下浏览器还会补一个 click，别让它顺手打开详情抽屉
  S.suppressClick = true;
  setTimeout(() => { S.suppressClick = false; }, 260);

  if (commit) {
    const cur = wpGet(port);
    const next = S.watchPorts.filter((w) => w.port !== port);
    if (cur) next.splice(Math.max(0, Math.min(toIndex, next.length)), 0, cur);
    if (listSig(next) !== listSig(S.watchPorts)) {
      savePorts(next); // savePorts 内部会重渲染
      return;
    }
  }
  render();
}

/* ─────────── 事件绑定 ─────────── */

function wire() {
  $('#search').addEventListener('input', (ev) => {
    S.query = ev.target.value.trim().toLowerCase();
    render();
  });

  $('#btnPause').addEventListener('click', async () => {
    const api = App();
    if (!api) return;
    const snap = await api.SetPaused(!S.paused);
    if (snap) applySnapshot(snap);
  });

  $('#btnMin').addEventListener('click', () => App()?.WindowMin());  $('#btnMax').addEventListener('click', () => App()?.WindowMax());
  $('#btnClose').addEventListener('click', () => App()?.WindowHide());

  $('#dClose').addEventListener('click', closeDrawer);
  $('#dKill').addEventListener('click', () => { if (S.selected) openConfirm(S.selected); });
  $('#dReveal').addEventListener('click', async () => {
    if (!S.selected) return;
    const p = await App()?.RevealExe(S.selected);
    if (!p) toast('拿不到可执行文件路径（可能需要管理员权限）', 'warn');
  });

  $('#mCancel').addEventListener('click', closeModal);
  $('#mOk').addEventListener('click', () => {
    if (S.pending?.clear) {
      closeModal();
      savePorts([]);
      return;
    }
    if (S.pending?.elevate) doElevatedKill();
    else doKill();
  });
  $('#overlay').addEventListener('click', (ev) => {
    if (ev.target === $('#overlay')) closeModal();
  });

  for (const b of document.querySelectorAll('.seg')) {
    b.addEventListener('click', () => setMode(b.dataset.mode));
  }

  const tips = { statCpu: tipCpu, statMem: tipMem, statDisk: tipDisk };
  for (const [id, build] of Object.entries(tips)) {
    const el = $('#' + id);
    el.addEventListener('mouseenter', () => showTip(el, build));
    el.addEventListener('mouseleave', hideTip);
  }

  $('#watchAdd').addEventListener('click', () => addPorts($('#watchInput').value));
  $('#watchClear').addEventListener('click', openClearConfirm);
  $('#watchInput').addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter') {
      ev.preventDefault();
      addPorts($('#watchInput').value);
    }
  });

  wireChips();
  wireNotePop();

  // 拖动排序的全局监听：只挂一次，靠 D.pending / D.active 判定
  window.addEventListener('pointermove', onPointerMove, { passive: false });
  window.addEventListener('pointerup', onPointerUp);
  window.addEventListener('pointercancel', onPointerCancel);

  document.addEventListener('keydown', (ev) => {
    const typing = ['INPUT', 'TEXTAREA'].includes(document.activeElement.tagName);
    if (ev.key === 'Escape') {
      // 优先级：拖拽 > 气泡/菜单 > 弹窗 > 抽屉
      if (D.active) { finishDrag(false); return; }
      if (!$('#notePop').hidden) { closeNotePop(); return; }
      if (!$('#ctxMenu').hidden) { closeCtxMenu(); return; }
      if (!$('#overlay').hidden) closeModal();
      else if (!$('#drawer').hidden) closeDrawer();
    }
    if (ev.key === '/' && !typing) {
      ev.preventDefault();
      $('#search').focus();
    }
  });

  window.addEventListener('focus', () => App()?.SetFocus(true));
  window.addEventListener('blur', () => {
    App()?.SetFocus(false);
    if (D.active) finishDrag(false); // 鼠标在窗口外松开时收不到 pointerup，别让列表冻在那儿
  });
  window.addEventListener('resize', () => { closeNotePop(); closeCtxMenu(); renderChipBar(); });
}

function wireChips() {
  const box = $('#chips');
  box.addEventListener('scroll', renderChipBar, { passive: true });

  // 竖滚轮转横向：滚动条只有 6px 高，拖它不如直接滚
  box.addEventListener('wheel', (ev) => {
    if (box.scrollWidth - box.clientWidth <= 1) return;
    if (Math.abs(ev.deltaY) <= Math.abs(ev.deltaX)) return; // 已经是横向手势，交给浏览器
    box.scrollLeft += ev.deltaY;
    ev.preventDefault();
  }, { passive: false });

  wireChipRail();
}

function wireChipRail() {
  const rail = $('#chipRail');
  const box = $('#chips');
  let grab = null; // { x, left, thumbW }

  const metrics = () => {
    const over = box.scrollWidth - box.clientWidth;
    const thumbW = Math.max(30, Math.round(rail.clientWidth * box.clientWidth / box.scrollWidth));
    return { over, thumbW, maxLeft: rail.clientWidth - thumbW };
  };

  rail.addEventListener('pointerdown', (ev) => {
    const { over, thumbW, maxLeft } = metrics();
    if (over <= 0) return;
    ev.preventDefault();

    if (ev.target.classList.contains('thumb')) {
      grab = { x: ev.clientX, left: box.scrollLeft, thumbW };
      rail.classList.add('dragging');
      return;
    }

    // 点轨道直接跳一半，跟系统滚动条的手感一致
    const half = Math.max(0, Math.min(1, (ev.clientX - rail.getBoundingClientRect().left - thumbW / 2) / maxLeft));
    box.scrollLeft = half * over;
    renderChipBar();
  });

  window.addEventListener('pointermove', (ev) => {
    if (!grab) return;
    const { over, maxLeft } = metrics();
    const dx = ev.clientX - grab.x;
    box.scrollLeft = grab.left + (maxLeft > 0 ? (dx / maxLeft) * over : 0);
    renderChipBar();
  });

  window.addEventListener('pointerup', () => {
    if (!grab) return;
    grab = null;
    rail.classList.remove('dragging');
  });
}

function wireNotePop() {
  const pop = $('#notePop');

  $('#npSave').addEventListener('click', saveNote);
  $('#npClear').addEventListener('click', clearNote);

  $('#npInput').addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter') {
      ev.preventDefault();
      saveNote();
    }
  });

  // 点外面关掉。mousedown 而不是 click：否则点按钮时会先关掉再触发不到
  document.addEventListener('mousedown', (ev) => {
    if (!pop.hidden && !pop.contains(ev.target)) closeNotePop();
    if (!$('#ctxMenu').hidden && !$('#ctxMenu').contains(ev.target)) closeCtxMenu();
  }, true);
}

/* ─────────── 启动 ─────────── */

// 后端返回什么形状都归一成 {port, note, notify}，否则后面到处判 undefined
function normalizeList(list) {
  return (list || [])
    .map((w) => ({
      port: Number(w && w.port !== undefined ? w.port : w),
      note: (w && w.note) || '',
      notify: !(w && w.notify === false),
    }))
    .filter((w) => Number.isFinite(w.port) && w.port >= 1 && w.port <= 65535);
}

async function boot() {
  wire();

  const api = App();
  if (!api) {
    toast('后端未就绪，请重新启动 PortMon', 'err', 8000);
    return;
  }

  try {
    S.watchPorts = normalizeList(await api.GetWatchPorts());
  } catch {
    S.watchPorts = [];
  }
  // 已经配置了盯守端口就直接进盯守视图，这是用户最关心的那几个
  S.mode = S.watchPorts.length ? 'watch' : 'all';
  renderChips();

  try {
    applySys(await api.GetSysStats());
  } catch { /* 首屏拿不到就等事件 */ }

  try {
    applySnapshot(await api.GetSnapshot());
  } catch (err) {
    toast('读取端口失败：' + err, 'err');
  }

  if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn('ports:snapshot', applySnapshot);
    window.runtime.EventsOn('ports:error', (msg) => toast('采集异常：' + msg, 'err'));
    window.runtime.EventsOn('sys:stats', applySys);
  } else {
    // 事件通道不可用时退化为轮询，保证界面不会停住
    setInterval(async () => applySnapshot(await api.GetSnapshot()), 800);
    setInterval(async () => applySys(await api.GetSysStats()), 1000);
  }

  App()?.SetFocus(document.hasFocus());
}

document.addEventListener('DOMContentLoaded', boot);
