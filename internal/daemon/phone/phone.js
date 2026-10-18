// Wharfdrop phone page. Every request, response and file chunk is encrypted
// with XChaCha20-Poly1305 using the key from the QR code's URL fragment, which
// the browser never sends over the network.
import { xchacha20poly1305 } from './noble/chacha.js';

const te = new TextEncoder(), td = new TextDecoder();
const $ = s => document.querySelector(s);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const b64d = s => Uint8Array.from(atob(s.replace(/-/g, '+').replace(/_/g, '/') + '==='.slice((s.length + 3) % 4)), c => c.charCodeAt(0));
function fmt(n) { if (n == null || n < 0) return '—'; const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0; while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; } return (i ? n.toFixed(n < 10 ? 1 : 0) : n) + ' ' + u[i]; }
function fmtDur(s) { if (!isFinite(s) || s <= 0) return ''; s = Math.round(s); if (s < 60) return s + ' s'; const m = Math.round(s / 60); return m < 60 ? m + ' min' : Math.floor(m / 60) + ' h ' + (m % 60) + ' min'; }
const isImage = n => /\.(jpe?g|png|gif|webp|bmp|heic|avif)$/i.test(n);

const ICON = {
  file: '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/>',
  folder: '<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/>',
  image: '<rect width="18" height="18" x="3" y="3" rx="2"/><circle cx="9" cy="9" r="2"/><path d="m21 15-3.086-3.086a2 2 0 0 0-2.828 0L6 21"/>',
  video: '<path d="m16 13 5.223 3.482a.5.5 0 0 0 .777-.416V7.87a.5.5 0 0 0-.752-.432L16 10.5"/><rect x="2" y="6" width="14" height="12" rx="2"/>',
  up: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" x2="12" y1="3" y2="15"/>',
  down: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" x2="12" y1="15" y2="3"/>',
  check: '<path d="M20 6 9 17l-5-5"/>', back: '<path d="m15 18-6-6 6-6"/>', x: '<path d="M18 6 6 18"/><path d="m6 6 12 12"/>',
  wifi: '<path d="M12 20h.01"/><path d="M2 8.82a15 15 0 0 1 20 0"/><path d="M5 12.859a10 10 0 0 1 14 0"/><path d="M8.5 16.429a5 5 0 0 1 7 0"/>',
};
const ic = n => `<svg class="i" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${ICON[n]}</svg>`;
const kindIcon = n => isImage(n) ? 'image' : /\.(mp4|mov|mkv|webm|m4v|avi)$/i.test(n) ? 'video' : 'file';

// ---------- session ----------
let SID = '', KEY = null, REMEMBER = false;
const STORE = 'wharfdrop-phone';
function load() {
  const frag = location.hash.slice(1);
  if (frag.includes('.')) {
    const [sid, key] = frag.split('.');
    history.replaceState(null, '', location.pathname); // keep the key out of the address bar
    try { sessionStorage.setItem(STORE, JSON.stringify({ sid, key })); } catch { }
    return { sid, key, fresh: true };
  }
  for (const st of [sessionStorage, localStorage]) {
    try { const v = JSON.parse(st.getItem(STORE) || 'null'); if (v?.sid && v?.key) return v; } catch { }
  }
  return null;
}
function savedName() { try { return localStorage.getItem(STORE + '-name') || ''; } catch { return ''; } }
function defaultName() {
  const ua = navigator.userAgent;
  if (/iPhone/.test(ua)) return 'iPhone';
  if (/iPad/.test(ua)) return 'iPad';
  const m = ua.match(/Android[^;]*;\s*([^;)]+?)(?:\s+Build|\))/);
  if (m && !/^(K|wv)$/.test(m[1].trim())) return m[1].trim().slice(0, 40);
  return /Android/.test(ua) ? 'Android phone' : 'Phone';
}

// ---------- crypto + requests ----------
function seal(aad, plain) {
  const n = crypto.getRandomValues(new Uint8Array(24));
  const ct = xchacha20poly1305(KEY, n, te.encode(aad)).encrypt(plain);
  const out = new Uint8Array(24 + ct.length); out.set(n); out.set(ct, 24); return out;
}
function open(aad, msg) { return xchacha20poly1305(KEY, msg.subarray(0, 24), te.encode(aad)).decrypt(msg.subarray(24)); }

class LinkError extends Error { constructor(msg, gone, closed) { super(msg); this.gone = gone; this.closed = closed; } }
async function rpc(op, args = {}) {
  let r;
  try {
    r = await fetch('/api/rpc', { method: 'POST', headers: { 'X-Session': SID, 'Content-Type': 'application/octet-stream' }, body: seal('rpc|' + SID, te.encode(JSON.stringify({ op, t: Date.now(), ...args }))), cache: 'no-store' });
  } catch { throw new LinkError('offline', false); }
  const buf = new Uint8Array(await r.arrayBuffer());
  let data = null;
  try { data = JSON.parse(td.decode(open('rsp|' + SID, buf))); } catch { }
  if (!data) {
    const text = td.decode(buf).trim();
    if (r.status === 401 && /clock/.test(text)) throw new LinkError('Your phone\'s clock is more than 5 minutes off. Set it to automatic time and try again.', false);
    if (r.status === 401 || r.status === 404) throw new LinkError('expired', true);
    throw new LinkError(text || r.statusText, false);
  }
  if (data.closed) throw new LinkError(data.message || 'The phone link was closed.', true, data.closed);
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data;
}

// ---------- state ----------
let S = null, tab = 'send', online = false, failures = 0;
const uploads = [];  // {id,name,size,done,rate,state,err,cancel}
const saving = {};   // outbox id -> {done,size,state}
const browse = { share: null, path: '', entries: null, thumbs: {} };

function setStatus(text, on) { $('#cstatus').textContent = text; $('#cdot').classList.toggle('on', !!on); }
function banner(text) { const b = $('#banner'); b.textContent = text || ''; b.classList.toggle('on', !!text); }

function gate(title, text, retry) {
  $('#app').classList.add('hidden');
  $('#sheet').classList.remove('on'); $('#viewer').classList.remove('on');
  const g = $('#gate'); g.classList.remove('hidden');
  g.innerHTML = `<div class="logo"><svg class="i" style="width:30px;height:30px" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M8 6v12m0 0-3.5-3.5M8 18l3.5-3.5M16 18V6m0 0-3.5 3.5M16 6l3.5 3.5"/></svg></div><h1>${esc(title)}</h1><p>${esc(text)}</p>`
    + (retry ? '<p style="margin-top:18px"><button class="btn primary" id="gretry">Reconnect</button></p>' : '');
  if (retry) $('#gretry').onclick = () => location.reload();
}

// ended shows why the link is over and stops all background work. A
// remembered phone keeps its key so it can reconnect when the computer opens
// the link again; a one-time link, or a phone disconnected on the computer,
// forgets it.
let over = false;
function ended(e) {
  over = true; online = false;
  for (const u of uploads) if (u.state === 'sending' || u.state === 'queued') { u.state = 'cancelled'; u.cancel?.(); }
  const keep = REMEMBER && e.closed && e.closed !== 'forgotten';
  if (!keep) forgetLocal();
  const title = e.closed === 'forgotten' ? 'Disconnected' : e.closed ? 'Phone link closed' : 'This phone link has ended';
  const text = e.closed ? e.message + (keep ? ' When it is open again, tap Reconnect.' : ' To connect again, open Connect a phone in Wharfdrop and scan the new QR code.')
    : 'On your computer, open Wharfdrop, choose Connect a phone, and scan the new QR code.';
  gate(title, text, keep);
}

async function poll() {
  try {
    S = await rpc('state');
    failures = 0; online = true; banner('');
    setStatus('Connected · encrypted', true);
    render();
  } catch (e) {
    if (e.gone) { ended(e); return; }
    online = false; failures++;
    setStatus('Not connected', false);
    banner(e.message === 'offline' ? `Can't reach ${S?.computer || 'the computer'}. Check that both are on the same Wi-Fi and the phone link is open in Wharfdrop.` : e.message);
  }
  if (!over) setTimeout(poll, document.hidden ? 5000 : (uploads.some(u => u.state === 'sending') ? 1500 : 2000));
}

function forgetLocal() { try { localStorage.removeItem(STORE); sessionStorage.removeItem(STORE); } catch { } }

// ---------- rendering ----------
function render() {
  if (!S) return;
  $('#cname').textContent = S.computer || 'Computer';
  const n = (S.outbox || []).filter(o => saving[o.id]?.state !== 'saved').length;
  const c = $('#rcount'); c.textContent = n; c.classList.toggle('hidden', !n);
  if (tab === 'send') renderSend(); else if (tab === 'recv') renderRecv(); else renderBrowse();
}

function upRow(u) {
  const pct = u.size ? Math.min(100, u.done * 100 / u.size) : (u.state === 'done' ? 100 : 0);
  const eta = u.rate > 1 ? (u.size - u.done) / u.rate : 0;
  const sub = u.state === 'queued' ? `${fmt(u.size)} · Waiting…` : u.state === 'done' ? `Sent · ${fmt(u.size)}` : u.state === 'error' ? `<span style="color:var(--danger)">${esc(u.err)}</span>` : u.state === 'cancelled' ? 'Cancelled'
    : `${fmt(u.done)} of ${fmt(u.size)} · <span class="live">${u.rate ? fmt(u.rate) + '/s' : 'Starting…'}${eta ? ' · ' + fmtDur(eta) + ' left' : ''}</span>`;
  return `<div class="item"><span class="ficon">${ic(u.state === 'done' ? 'check' : kindIcon(u.name))}</span><div class="meta"><div class="nm">${esc(u.name)}</div><div class="sub">${sub}</div>
    ${u.state === 'sending' ? `<div class="bar"><i style="width:${pct.toFixed(1)}%"></i></div>` : ''}</div>
    ${u.state === 'sending' ? `<button class="btn ghost" data-cancel="${u.key}" aria-label="Cancel">${ic('x')}</button>` : ''}</div>`;
}

function renderSend() {
  $('#v-send').innerHTML = `<div class="card pick">
      <button class="bigbtn primary" id="pickmedia"><span>${ic('image')}</span><span><b>Photos &amp; videos</b><small>Send to ${esc(S.computer)}</small></span></button>
      <button class="bigbtn" id="pickany"><span>${ic('file')}</span><span><b>Other files</b><small>Documents, music, anything</small></span></button>
    </div>
    ${uploads.length ? `<h2>Sending</h2><div class="card list">${uploads.slice().reverse().map(upRow).join('')}</div>` : `<p class="note" style="margin:16px 6px">Files you send arrive in the Wharfdrop folder on ${esc(S.computer)}.</p>`}`;
  $('#pickmedia').onclick = () => $('#fmedia').click();
  $('#pickany').onclick = () => $('#fany').click();
}

function renderRecv() {
  const items = S.outbox || [];
  $('#v-recv').innerHTML = items.length ? `<h2>From ${esc(S.computer)}</h2><div class="card list">${items.map(o => {
    const sv = saving[o.id];
    const pct = sv && o.size ? Math.min(100, sv.done * 100 / o.size) : 0;
    const sub = sv?.state === 'saving' ? `${fmt(sv.done)} of ${fmt(o.size)}${sv.rate ? ` · <span class="live">${fmt(sv.rate)}/s</span>` : ''}` : sv?.state === 'error' ? `<span style="color:var(--danger)">${esc(sv.err)}</span>` : fmt(o.size);
    return `<div class="item"><span class="ficon">${ic(kindIcon(o.name))}</span><div class="meta"><div class="nm">${esc(o.name)}</div><div class="sub">${sub}</div>${sv?.state === 'saving' ? `<div class="bar"><i style="width:${pct.toFixed(1)}%"></i></div>` : ''}</div>
      ${sv?.state === 'saving' ? '' : `<button class="btn primary" data-save="${esc(o.id)}">${ic('down')} Save</button>`}</div>`;
  }).join('')}</div><p class="note" style="margin:12px 6px">Files over about 1 GB may be too large for your phone's browser to save in one go.</p>`
    : `<div class="empty"><div class="big">${ic('down')}</div><h3>Nothing to receive yet</h3><p>On ${esc(S.computer)}, select this phone in Wharfdrop and drop files to send them here.</p></div>`;
}

function renderBrowse() {
  const v = $('#v-browse');
  if (!browse.share) {
    const shares = S.shares || [];
    v.innerHTML = shares.length ? `<h2>Shared by ${esc(S.computer)}</h2><div class="card list">${shares.map(n => `<div class="item" data-share="${esc(n)}" style="cursor:pointer"><span class="ficon folder">${ic('folder')}</span><div class="meta"><div class="nm">${esc(n)}</div><div class="sub">Shared folder · read-only</div></div></div>`).join('')}</div>`
      : `<div class="empty"><div class="big">${ic('folder')}</div><h3>No shared folders</h3><p>On ${esc(S.computer)}, open Shared folders in Wharfdrop and share a folder with all paired devices.</p></div>`;
    return;
  }
  const crumbs = `<div class="crumbs"><button class="iconbtn" id="up" aria-label="Back">${ic('back')}</button><b>${esc(browse.path ? browse.path.split('/').pop() : browse.share)}</b></div>`;
  if (!browse.entries) { v.innerHTML = crumbs + '<div class="empty"><p>Loading…</p></div>'; return; }
  const ents = browse.entries;
  v.innerHTML = crumbs + (ents.length ? `<div class="grid">${ents.map((e, i) => {
    const t = browse.thumbs[pathOf(e)];
    const inner = e.dir ? `<div class="gthumb" style="background:var(--accent-soft);color:var(--accent)">${ic('folder')}</div>`
      : `<div class="gthumb">${t ? `<img src="${t}" alt="">` : ic(kindIcon(e.name))}</div>`;
    return `<button class="gtile" data-ent="${i}">${inner}<div class="nm">${esc(e.name)}</div></button>`;
  }).join('')}</div>` : '<div class="empty"><p>This folder is empty.</p></div>');
  $('#up').onclick = () => { if (browse.path) { browse.path = browse.path.split('/').slice(0, -1).join('/'); loadDir(); } else { browse.share = null; renderBrowse(); } };
  loadThumbs();
}
const pathOf = e => (browse.path ? browse.path + '/' : '') + e.name;

async function loadDir() {
  browse.entries = null; renderBrowse();
  try { browse.entries = (await rpc('ls', { share: browse.share, path: browse.path })).entries || []; }
  catch (e) { browse.entries = []; banner(e.message); }
  renderBrowse();
}

let thumbBusy = false;
async function loadThumbs() {
  if (thumbBusy) return; thumbBusy = true;
  try {
    for (const e of browse.entries || []) {
      const p = pathOf(e);
      if (e.dir || !isImage(e.name) || browse.thumbs[p] !== undefined) continue;
      browse.thumbs[p] = '';
      try {
        const g = await rpc('get', { kind: 'thumb', share: browse.share, path: p, thumb: 240 });
        const blob = await download(g.token, () => { });
        browse.thumbs[p] = URL.createObjectURL(new Blob([blob], { type: 'image/jpeg' }));
        if (tab === 'browse') renderBrowse();
      } catch { }
    }
  } finally { thumbBusy = false; }
}

// ---------- downloads (computer -> phone) ----------
// The stream is a sequence of [u32 length][nonce||ciphertext] frames; the last
// one is an empty frame bound to "end", so a cut-off download is detected.
async function download(token, onProgress) {
  const r = await fetch('/api/down/' + token, { cache: 'no-store' });
  if (!r.ok) throw new Error('download failed (' + r.status + ')');
  const reader = r.body.getReader();
  let buf = new Uint8Array(0), idx = 0, done = 0, ended = false;
  const parts = [];
  for (;;) {
    while (buf.length >= 4) {
      const len = new DataView(buf.buffer, buf.byteOffset, 4).getUint32(0);
      if (buf.length < 4 + len) break;
      const frame = buf.subarray(4, 4 + len);
      if (len === 40) { open('dn|' + token + '|end|' + idx, frame); ended = true; }
      else { const pt = open('dn|' + token + '|' + idx, frame); parts.push(pt); done += pt.length; idx++; onProgress(done); }
      buf = buf.slice(4 + len);
    }
    if (ended) break;
    const { value, done: eof } = await reader.read();
    if (eof) break;
    const nb = new Uint8Array(buf.length + value.length); nb.set(buf); nb.set(value, buf.length); buf = nb;
  }
  if (!ended) throw new Error('download was cut off');
  if (parts.length === 1) return parts[0];
  const out = new Blob(parts);
  return new Uint8Array(await out.arrayBuffer());
}

function saveBlob(name, data, type) {
  const url = URL.createObjectURL(new Blob([data], { type: type || 'application/octet-stream' }));
  const a = document.createElement('a'); a.href = url; a.download = name; document.body.append(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60000);
}

async function saveOffered(id) {
  const o = (S.outbox || []).find(x => x.id === id); if (!o) return;
  const sv = saving[id] = { state: 'saving', done: 0, rate: 0, t0: performance.now() };
  render();
  try {
    const g = await rpc('get', { kind: 'outbox', id });
    const data = await download(g.token, n => { sv.done = n; sv.rate = n / ((performance.now() - sv.t0) / 1000); if (tab === 'recv') renderRecv(); });
    saveBlob(o.name, data);
    sv.state = 'saved';
    S = await rpc('outbox-done', { id });
  } catch (e) { sv.state = 'error'; sv.err = e.message; }
  render();
}

async function openEntry(e) {
  const p = pathOf(e);
  if (e.dir) { browse.path = p; loadDir(); return; }
  try {
    const g = await rpc('get', { kind: 'share', share: browse.share, path: p });
    banner(`Downloading ${e.name}…`);
    const t0 = performance.now();
    const data = await download(g.token, n => banner(`Downloading ${e.name} · ${fmt(n)} of ${fmt(g.size)} · ${fmt(n / ((performance.now() - t0) / 1000))}/s`));
    banner('');
    if (isImage(e.name)) {
      const url = URL.createObjectURL(new Blob([data]));
      $('#vname').textContent = e.name; $('#vimg').src = url; $('#viewer').classList.add('on');
      $('#vsave').onclick = () => saveBlob(e.name, data);
    } else saveBlob(e.name, data);
  } catch (err) { banner(err.message); }
}

// ---------- uploads (phone -> computer) ----------
let ukey = 0;
async function sendFiles(files) {
  for (const f of files) uploads.push({ key: ++ukey, file: f, name: f.name, size: f.size, done: 0, rate: 0, state: 'queued' });
  render();
  if (sendFiles.running) return; sendFiles.running = true;
  try {
    for (const u of uploads) if (u.state === 'queued') await sendOne(u);
  } finally { sendFiles.running = false; }
}

async function sendOne(u) {
  u.state = 'sending'; u.t0 = performance.now(); render();
  const xhrs = new Set(); let cancelled = false;
  u.cancel = () => { cancelled = true; xhrs.forEach(x => x.abort()); };
  try {
    const st = await rpc('up-start', { name: u.name, size: u.size });
    const chunk = st.chunk, per = new Map(); let next = 0, failed = null;
    const report = () => { let s = 0; for (const v of per.values()) s += v; u.done = s; const dt = (performance.now() - u.t0) / 1000; u.rate = dt > 0.3 ? s / dt : 0; if (tab === 'send') renderSend(); };
    const putChunk = (off, body) => new Promise((res, rej) => {
      const x = new XMLHttpRequest(); xhrs.add(x);
      x.open('PUT', `/api/up?s=${encodeURIComponent(SID)}&id=${encodeURIComponent(st.id)}&off=${off}`);
      x.upload.onprogress = ev => { per.set(off, Math.min(ev.loaded, body.length - 40)); report(); };
      x.onload = () => { xhrs.delete(x); x.status >= 200 && x.status < 300 ? res() : rej(new Error('chunk rejected (' + x.status + ')')); };
      x.onerror = () => { xhrs.delete(x); rej(new Error('connection lost')); };
      x.onabort = () => { xhrs.delete(x); rej(new Error('cancelled')); };
      x.send(body);
    });
    const worker = async () => {
      while (!failed && !cancelled) {
        const off = next; if (off >= u.size) return; next += chunk;
        const plain = new Uint8Array(await u.file.slice(off, Math.min(off + chunk, u.size)).arrayBuffer());
        const body = seal('up|' + st.id + '|' + off, plain);
        for (let attempt = 0; ; attempt++) {
          try { await putChunk(off, body); per.set(off, plain.length); report(); break; }
          catch (e) { per.set(off, 0); if (cancelled || attempt >= 2) { failed = e; return; } await new Promise(r => setTimeout(r, 500 * (attempt + 1))); }
        }
      }
    };
    await Promise.all([worker(), worker(), worker()]);
    if (cancelled || failed) { try { await rpc('up-abort', { id: st.id }); } catch { } throw failed || new Error('cancelled'); }
    await rpc('up-finish', { id: st.id });
    u.done = u.size; u.state = 'done';
  } catch (e) { u.state = cancelled ? 'cancelled' : 'error'; u.err = e.message; }
  u.file = null; render();
}

// ---------- settings sheet ----------
function openSheet() { $('#pname').value = S?.name || savedName() || defaultName(); $('#premember').checked = REMEMBER; $('#sheet').classList.add('on'); }
async function saveSheet() {
  const name = $('#pname').value.trim() || defaultName();
  REMEMBER = $('#premember').checked;
  try {
    localStorage.setItem(STORE + '-name', name);
    if (REMEMBER) localStorage.setItem(STORE, JSON.stringify({ sid: SID, key: KEYTEXT })); else localStorage.removeItem(STORE);
  } catch { }
  try { S = await rpc('hello', { name, remember: REMEMBER }); } catch (e) { banner(e.message); }
  $('#sheet').classList.remove('on'); render();
}

// ---------- wiring ----------
let KEYTEXT = '';
document.addEventListener('click', e => {
  const t = e.target.closest('[data-tab],[data-save],[data-cancel],[data-share],[data-ent]');
  if (!t) return;
  if (t.dataset.tab) { tab = t.dataset.tab; document.querySelectorAll('.tab').forEach(b => b.classList.toggle('sel', b === t)); for (const v of ['send', 'recv', 'browse']) $('#v-' + v).classList.toggle('hidden', v !== tab); render(); }
  else if (t.dataset.save) saveOffered(t.dataset.save);
  else if (t.dataset.cancel) uploads.find(u => String(u.key) === t.dataset.cancel)?.cancel?.();
  else if (t.dataset.share) { browse.share = t.dataset.share; browse.path = ''; loadDir(); }
  else if (t.dataset.ent) openEntry(browse.entries[+t.dataset.ent]);
});
$('#fmedia').onchange = e => { sendFiles([...e.target.files]); e.target.value = ''; };
$('#fany').onchange = e => { sendFiles([...e.target.files]); e.target.value = ''; };
$('#menu').onclick = openSheet;
$('#psave').onclick = saveSheet;
$('#sheet').onclick = e => { if (e.target.id === 'sheet') $('#sheet').classList.remove('on'); };
$('#pdisconnect').onclick = async () => {
  try { await rpc('disconnect'); } catch { }
  over = true; forgetLocal(); KEY = null;
  gate('Disconnected', 'This phone is no longer connected, and its key was deleted. Scan a new QR code in Wharfdrop on your computer to connect again.');
};
$('#vclose').onclick = () => { $('#viewer').classList.remove('on'); URL.revokeObjectURL($('#vimg').src); };
// closing the page tells the computer right away instead of after a timeout
window.addEventListener('pagehide', () => {
  if (over || !KEY || !SID) return;
  try { fetch('/api/rpc?s=' + encodeURIComponent(SID), { method: 'POST', keepalive: true, body: seal('rpc|' + SID, te.encode(JSON.stringify({ op: 'bye', t: Date.now() }))) }); } catch { }
});
// a new QR link opened in this same tab only changes the #fragment
window.addEventListener('hashchange', () => { if (location.hash.includes('.')) location.reload(); });
window.addEventListener('pageshow', e => { if (e.persisted && !over) poll(); });
window.addEventListener('beforeunload', e => { if (uploads.some(u => u.state === 'sending')) { e.preventDefault(); e.returnValue = ''; } });

(async function start() {
  const s = load();
  if (!s) { gate('Scan the QR code', 'On your computer, open Wharfdrop and choose Connect a phone, then scan the QR code with this phone\'s camera.'); return; }
  try { KEY = b64d(s.key); } catch { KEY = null; }
  if (!KEY || KEY.length !== 32) { gate('This link is not valid', 'Scan the QR code shown in Wharfdrop on your computer again.'); return; }
  SID = s.sid; KEYTEXT = s.key;
  try { REMEMBER = !!JSON.parse(localStorage.getItem(STORE) || 'null'); } catch { }
  $('#gate').classList.add('hidden');
  $('#app').classList.remove('hidden');
  setStatus('Connecting…', false);
  try {
    S = await rpc('hello', { name: savedName() || defaultName(), remember: REMEMBER });
    if (S.avatar) rpc('get', { kind: 'avatar' }).then(g => download(g.token, () => { })).then(b => { const img = $('#cav'); img.src = URL.createObjectURL(new Blob([b], { type: 'image/jpeg' })); img.style.display = 'block'; }).catch(() => { });
    render();
  } catch (e) {
    if (e.gone) { ended(e); return; }
    banner(e.message === 'offline' ? 'Can\'t reach the computer. Check that both are on the same Wi-Fi and the phone link is open in Wharfdrop.' : e.message);
  }
  poll();
})();
