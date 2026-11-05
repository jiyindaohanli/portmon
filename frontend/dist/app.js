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
  watchPorts: [],     // 盯守端口，持久化在 config.json
};

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
  const view = S.mode === 'watch' ? watchView() : S.entries.filter(matches);
  renderList(view);
  $('#empty').hidden = view.length > 0;
  updateSegments();
}

// 盯守视图：只展示指定端口，空闲的也占一行——这才是“固定展示”的意义
function watchView() {
  const out = [];
  for (const port of S.watchPorts) {
    const hits = S.entries.filter((e) => e.port === port);
    if (hits.length) out.push(...hits);
    else out.push(freeEntry(port));
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

function buildRow(e) {
  const el = document.createElement('div');
  el.className = 'row';
  el.dataset.key = e.key;
  el.innerHTML = `
    <span class="c-accent"></span>
    <span class="c-port"><b></b><i></i></span>
    <span class="c-addr"></span>
    <span class="c-proc"><span class="avatar"></span><span class="meta"><b></b><i></i></span></span>
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

  el.addEventListener('click', () => {
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

let chipSig = '';

function renderChips() {
  const box = $('#chips');
  if (!box) return;

  // 每 250ms 一次快照，签名不变就不重建 DOM（端口占用状态变化时才重建）
  const sig = S.watchPorts.map((p) => p + (S.entries.some((e) => e.port === p) ? '1' : '0')).join(',');
  if (sig === chipSig) return;
  chipSig = sig;
  box.innerHTML = '';

  for (const port of S.watchPorts) {
    const used = S.entries.some((e) => e.port === port);
    const chip = document.createElement('span');
    chip.className = 'chip ' + (used ? 'used' : 'free');
    chip.title = used ? `${port} 正被占用` : `${port} 当前空闲`;

    const label = document.createElement('span');
    label.textContent = port;

    const x = document.createElement('button');
    x.textContent = '×';
    x.title = `取消盯守 ${port}`;
    x.addEventListener('click', () => removePort(port));

    chip.append(label, x);
    box.append(chip);
  }
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
  for (const n of nums) if (!merged.includes(n)) merged.push(n);

  $('#watchInput').value = '';
  await savePorts(merged);
  setMode('watch');
}

async function removePort(port) {
  await savePorts(S.watchPorts.filter((p) => p !== port));
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
    const persisted = (await api.GetWatchPorts()) || [];
    if (persisted.join(',') !== list.join(',')) {
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
    if (S.pending?.elevate) doElevatedKill();
    else doKill();
  });
  $('#overlay').addEventListener('click', (ev) => {
    if (ev.target === $('#overlay')) closeModal();
  });

  for (const b of document.querySelectorAll('.seg')) {
    b.addEventListener('click', () => setMode(b.dataset.mode));
  }

  $('#watchAdd').addEventListener('click', () => addPorts($('#watchInput').value));
  $('#watchClear').addEventListener('click', () => savePorts([]));
  $('#watchInput').addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter') {
      ev.preventDefault();
      addPorts($('#watchInput').value);
    }
  });

  document.addEventListener('keydown', (ev) => {
    const typing = ['INPUT', 'TEXTAREA'].includes(document.activeElement.tagName);
    if (ev.key === 'Escape') {
      if (!$('#overlay').hidden) closeModal();
      else if (!$('#drawer').hidden) closeDrawer();
    }
    if (ev.key === '/' && !typing) {
      ev.preventDefault();
      $('#search').focus();
    }
  });

  window.addEventListener('focus', () => App()?.SetFocus(true));
  window.addEventListener('blur', () => App()?.SetFocus(false));
}

/* ─────────── 启动 ─────────── */

async function boot() {
  wire();

  const api = App();
  if (!api) {
    toast('后端未就绪，请重新启动 PortMon', 'err', 8000);
    return;
  }

  try {
    S.watchPorts = (await api.GetWatchPorts()) || [];
  } catch {
    S.watchPorts = [];
  }
  // 已经配置了盯守端口就直接进盯守视图，这是用户最关心的那几个
  S.mode = S.watchPorts.length ? 'watch' : 'all';
  renderChips();

  try {
    applySnapshot(await api.GetSnapshot());
  } catch (err) {
    toast('读取端口失败：' + err, 'err');
  }

  if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn('ports:snapshot', applySnapshot);
    window.runtime.EventsOn('ports:error', (msg) => toast('采集异常：' + msg, 'err'));
  } else {
    // 事件通道不可用时退化为轮询，保证界面不会停住
    setInterval(async () => applySnapshot(await api.GetSnapshot()), 800);
  }

  App()?.SetFocus(document.hasFocus());
}

document.addEventListener('DOMContentLoaded', boot);
