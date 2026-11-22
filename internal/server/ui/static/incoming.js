// The Incoming page: drops anywhere on the page, the uploader, and this
// browser's uploads overlaid on the server's list.
//
// Each file goes to /api/uploads in pieces, one file at a time: start it
// (or, for a file already part sent, find where it got to), then send
// pieces from there. A piece that fails is retried after a pause that
// grows, from wherever the server says it got to, so a dropped connection
// or a laptop asleep for a while costs a piece, not the file.
//
// The server lists every unfinished upload (it can't tell whose is whose)
// and the page polls it. This page knows its own by upload id or folder
// group: it gives those its finer progress, moves them after the rest, and
// adds the ones still waiting to start. A dropped .torrent file becomes a
// download, not an upload.
(function () {
  if (window.mvdIncoming) { window.mvdIncoming.bind(); return; } // a page swap brought us back
  const PIECE = 16 << 20;      // 16 MB a piece
  const STALL = 60 * 1000;     // no progress this long: give up on the piece and retry
  const MINE = 'mvd.uploads';  // this browser's uploads, by id or group, remembered across tabs
  const queue = [];
  let current = null;          // the item uploading now

  const $ = (id) => document.getElementById(id);
  const page = () => $('incoming-page');

  // --- words, as the server's ---

  const fmtSize = (n) => n >= 2 ** 40 ? (n / 2 ** 40).toFixed(1) + ' TB'
    : n >= 1 << 30 ? (n / (1 << 30)).toFixed(1) + ' GB'
    : n >= 1 << 20 ? Math.round(n / (1 << 20)) + ' MB' : Math.ceil(n / 1024) + ' KB';
  function remaining(sec) {
    const n = (v, unit) => (v === 1 ? '1 ' + unit : v + ' ' + unit + 's') + ' remaining';
    if (sec < 90) return n(Math.max(1, Math.round(sec)), 'second');
    if (sec < 90 * 60) return n(Math.round(sec / 60), 'minute');
    if (sec < 36 * 3600) return n(Math.round(sec / 3600), 'hour');
    return n(Math.round(sec / 86400), 'day');
  }
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const refresh = () => { if (window.htmx) htmx.trigger(document.body, 'incoming-changed'); };

  // --- which uploads are this browser's ---

  function mine() {
    try { return JSON.parse(localStorage.getItem(MINE) || '[]'); } catch (e) { return []; }
  }
  function remember(key) {
    try {
      const keys = mine().filter((k) => k !== key);
      keys.push(key);
      localStorage.setItem(MINE, JSON.stringify(keys.slice(-50)));
    } catch (e) { /* private window: this tab's are still known */ }
  }

  // --- this browser's uploads on the list ---

  // rowKey is the row an item shows on: its folder's, or its own.
  const rowKey = (item) => item.group || item.id || item.local;

  // overlay puts this browser's uploads on the list as it now is: after
  // every refresh, and as they progress.
  function overlay() {
    const list = $('inc-rows');
    if (!list) return;
    const ours = [current, ...queue].filter((it) => it && !it.cancelled);
    const keys = new Set(mine());
    const done = new Set();
    let after = null; // our rows go after everyone else's uploads
    list.querySelectorAll('li[data-upload]').forEach((li) => {
      if (!keys.has(li.dataset.upload) && !ours.some((it) => rowKey(it) === li.dataset.upload)) after = li;
    });
    const place = (li) => {
      if (after) after.after(li); else list.prepend(li);
      after = li;
    };
    for (const it of ours) {
      const key = rowKey(it);
      if (done.has(key)) continue;
      done.add(key);
      let li = list.querySelector('li[data-upload="' + CSS.escape(key) + '"]');
      if (!li) li = clientRow(it, key);
      li.classList.add('mine');
      place(li);
      say(li, it);
    }
    // Ours from before (another tab, or this one before it closed): stopped.
    list.querySelectorAll('li[data-upload]').forEach((li) => {
      if (done.has(li.dataset.upload) || !keys.has(li.dataset.upload)) return;
      li.classList.add('mine');
      const line = li.querySelector('.inc-line');
      if (!line.dataset.mine) {
        line.dataset.mine = '1';
        line.textContent = line.textContent.replace('from another computer', 'from this computer') +
          (li.classList.contains('stopped') ? ': drop it again to carry on' : '');
      }
      place(li);
    });
    if (list.children.length) {
      const empty = $('inc-none');
      if (empty) empty.hidden = true;
    }
  }

  // clientRow is a row for an upload the server doesn't have yet.
  function clientRow(it, key) {
    const li = document.createElement('li');
    li.className = 'inc-row mine';
    li.dataset.upload = key;
    li.innerHTML = '<span class="inc-handle"></span><div class="inc-main"><div class="inc-name"></div>' +
      '<div class="inc-line"></div><div class="inc-bar idle"><span class="inc-track"><span style="width:0%"></span></span>' +
      '<span class="pct">0%</span></div></div><div class="inc-actions"><button class="icon-btn" type="button" ' +
      'title="Cancel"><svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" ' +
      'stroke="currentColor" stroke-width="2.2" stroke-linecap="round"/></svg></button></div>';
    li.querySelector('.inc-name').textContent = it.group ? it.name.split('/')[0] : it.name;
    const b = li.querySelector('button');
    b.setAttribute('aria-label', 'Cancel ' + li.querySelector('.inc-name').textContent);
    b.addEventListener('click', () => {
      for (const x of [current, ...queue]) if (x && rowKey(x) === key) cancel(x);
      li.remove();
    });
    return li;
  }

  // say writes an item's progress on its row.
  function say(li, it) {
    const line = li.querySelector('.inc-line');
    line.dataset.mine = '1';
    let text, pct = null;
    if (it.problem) {
      text = it.problem;
    } else if (it.group) {
      const group = [current, ...queue].filter((x) => x && x.group === it.group);
      const left = group.length, total = it.groupFiles, sent = total - left;
      text = (sent ? sent + ' of ' : '') + total + ' files from this computer';
      if (current && current.group === it.group && current.rate) {
        const bytes = group.reduce((n, x) => n + x.file.size, 0) - (current.done || 0);
        text += ' · ' + remaining(bytes / current.rate);
      } else if (!current || current.group !== it.group) {
        text += ' · waiting to upload';
      }
      pct = Math.floor(100 * (sent + (current && current.group === it.group ? (current.done || 0) / current.file.size : 0)) / total);
    } else if (it === current) {
      text = (it.done > 0 ? fmtSize(it.done) + ' of ' : '') + fmtSize(it.file.size) + ' from this computer';
      if (it.note) text += ' · ' + it.note;
      else if (it.rate) text += ' · ' + remaining((it.file.size - it.done) / it.rate);
      else text += ' · starting';
      pct = Math.floor(100 * (it.done || 0) / it.file.size);
    } else {
      text = 'Waiting to upload';
    }
    line.textContent = text;
    li.classList.toggle('problem', !!it.problem);
    const bar = li.querySelector('.inc-bar');
    if (bar) bar.hidden = pct === null;
    if (bar && pct !== null) {
      bar.classList.toggle('idle', !it.rate);
      bar.querySelector('.inc-track span').style.width = pct + '%';
      bar.querySelector('.pct').textContent = pct + '%';
    }
  }

  function update(it) {
    const list = $('inc-rows');
    const li = list && list.querySelector('li[data-upload="' + CSS.escape(rowKey(it)) + '"]');
    if (li) say(li, it); else overlay();
  }

  // note puts a line above the list, as the server's messages are.
  function note(text, bad) {
    const inc = $('incoming');
    if (!inc) return;
    const p = document.createElement('p');
    p.className = 'flash' + (bad ? ' bad' : '');
    p.setAttribute('role', 'status');
    p.textContent = text;
    inc.prepend(p);
  }

  // --- adding ---

  let localIDs = 0;

  // add takes what was dropped or chosen: each {file, path} where path is
  // the file's path in a dropped or chosen folder (starting with the
  // folder's name), or just its name. A loose .torrent file is a download.
  // A folder's files share a group, so the server holds them back until
  // all are in, then files the folder whole.
  function add(entries) {
    const p = page();
    if (!p) return;
    const torrents = entries.filter((e) => !e.path.includes('/') && /\.torrent$/i.test(e.file.name));
    entries = entries.filter((e) => !torrents.includes(e));
    if (torrents.length) {
      if ('torrents' in p.dataset) torrents.forEach((e) => addTorrent(e.file));
      else note('Downloads aren\'t set up, so .torrent files can\'t be added here.', true);
    }
    if (!entries.length) return;
    if (!('uploads' in p.dataset)) { note('Uploads are off on this server.', true); return; }
    const ok = allowed();
    const folders = new Map();
    let skipped = 0;
    for (const e of entries) {
      const parts = e.path.split('/');
      if (parts.length < 2) {
        if (ok(e.file.name)) queue.push({ file: e.file, name: e.file.name, local: 'l' + (++localIDs) });
        else skipped++;
        continue;
      }
      if (parts.some((s) => s.startsWith('.')) || !ok(e.file.name)) continue; // left behind
      if (!folders.has(parts[0])) folders.set(parts[0], []);
      folders.get(parts[0]).push(e);
    }
    for (const [, files] of folders) {
      const group = groupID(files);
      remember(group);
      for (const e of files) queue.push({ file: e.file, name: e.path, group, groupFiles: files.length });
    }
    if (skipped) note(skipped === 1 ? 'One file isn\'t a video, subtitles or an archive, so it was left out.'
      : skipped + ' files aren\'t videos, subtitles or archives, so they were left out.', true);
    overlay();
    run();
  }

  // addTorrent sends a dropped .torrent to Transmission, as the add bar
  // would, and shows the list that comes back.
  async function addTorrent(file) {
    const body = new FormData();
    body.append('torrent', file);
    try {
      const r = await fetch('/ui/downloads', { method: 'POST', body, headers: { 'HX-Request': 'true' } });
      const html = await r.text();
      if (window.htmx && $('incoming')) { htmx.swap($('incoming'), html, { swapStyle: 'outerHTML' }); return; }
    } catch (e) {
      note('Couldn\'t add ' + file.name + ': ' + (e.message || e), true);
    }
    refresh();
  }

  // allowed: what the server takes (the picker's list of extensions).
  function allowed() {
    const p = page();
    const exts = new Set(((p && p.dataset.exts) || '').split(',').filter(Boolean));
    return (name) => !exts.size || exts.has(name.slice(name.lastIndexOf('.')).toLowerCase());
  }

  // groupID is the same 16 hex digits every time the same folder is sent
  // (its files' paths, sizes and times), so sending it again finds what's
  // already in. FNV-1a, 64 bits: crypto.subtle needs https, and the router
  // is plain http.
  function groupID(files) {
    const s = files.map((e) => [e.path, e.file.size, e.file.lastModified].join('|')).sort().join('\n');
    let h = 0xcbf29ce484222325n;
    const bytes = new TextEncoder().encode(s);
    for (const b of bytes) h = BigInt.asUintN(64, (h ^ BigInt(b)) * 0x100000001b3n);
    return h.toString(16).padStart(16, '0');
  }

  // walk turns a dropped folder into its files, each with its path.
  async function walk(entry, out) {
    if (entry.isFile) {
      const file = await new Promise((ok, bad) => entry.file(ok, bad));
      out.push({ file, path: entry.fullPath.replace(/^\//, '') });
      return;
    }
    const reader = entry.createReader();
    for (;;) {
      const batch = await new Promise((ok, bad) => reader.readEntries(ok, bad)); // a batch at a time
      if (!batch.length) break;
      for (const e of batch) await walk(e, out);
    }
  }

  async function run() {
    if (current) return;
    while (queue.length) {
      current = queue.shift();
      if (!current.cancelled) {
        try {
          await upload(current);
        } catch (e) {
          const why = String(e.message || e);
          if (!current.id && !current.group) {
            // Refused before it started (no room, say): there's no row
            // to put it on, so above the list.
            note('Couldn\'t upload ' + current.name + ': ' + why, true);
            current.cancelled = true;
            const li = document.querySelector('#inc-rows li[data-upload="' + current.local + '"]');
            if (li) li.remove();
          } else {
            current.problem = why;
            update(current);
            await sleep(4000); // long enough to read, then the list has its say
          }
        }
      }
    }
    current = null;
    refresh();
  }

  async function cancel(item) {
    item.cancelled = true;
    if (item.xhr) item.xhr.abort();
    const id = item.group || item.id;
    if (id) await fetch('/api/uploads/' + id, { method: 'DELETE' }).catch(() => {});
    refresh();
  }

  // --- one file ---

  async function json(r) { try { return await r.json(); } catch (e) { return {}; } }

  async function upload(item) {
    const f = item.file;
    update(item);
    const r = await fetch('/api/uploads', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: item.name, size: f.size,
        fingerprint: [item.name, f.size, f.lastModified].join('|'),
        group: item.group, group_files: item.groupFiles }),
    });
    const u = await json(r);
    if (!r.ok) throw new Error(u.error || 'the server said no (' + r.status + ')');
    if (u.done) { refresh(); return; } // in already, from before
    const local = item.local;
    item.id = u.id;
    if (!item.group) {
      remember(u.id);
      // Its row so far was the page's own: now it's the server's id.
      const li = local && document.querySelector('#inc-rows li[data-upload="' + local + '"]');
      if (li) li.dataset.upload = u.id;
    }
    let offset = u.offset || 0;
    let wait = 1000;
    const t0 = Date.now(), from = offset;
    item.done = offset;
    while (offset < f.size && !item.cancelled) {
      const end = Math.min(offset + PIECE, f.size);
      let res;
      try {
        res = await piece(item, offset, f.slice(offset, end), (sent) => {
          const done = offset + sent, secs = (Date.now() - t0) / 1000;
          item.done = done;
          item.rate = secs > 2 ? (done - from) / secs : 0;
          item.note = '';
          update(item);
        });
      } catch (e) {
        res = null; // no answer: the connection's gone, or it stalled
      }
      if (item.cancelled) return;
      if (res && res.status === 200) {
        offset = res.body.offset;
        wait = 1000;
        if (res.body.done) break;
        continue;
      }
      if (res && res.status === 404) throw new Error('the server no longer has this upload (canceled?)');
      if (res && (res.status === 507 || res.status === 413)) throw new Error(res.body.error);
      if (res && (res.status === 409 || res.status === 400) && typeof res.body.offset === 'number') {
        offset = res.body.offset; // where it really got to
        if (res.status === 409) continue;
      }
      // Something went wrong: wait, then ask the server where it got to.
      item.rate = 0;
      item.note = 'connection lost at ' + fmtSize(offset) + '; trying again in ' + Math.round(wait / 1000) + 's';
      update(item);
      await sleep(wait);
      wait = Math.min(wait * 2, 30000);
      try {
        const q = await fetch('/api/uploads/' + item.id);
        if (q.status === 404) throw new Error('the server no longer has this upload (canceled?)');
        if (q.ok) offset = (await q.json()).offset;
      } catch (e) {
        if (String(e.message).startsWith('the server')) throw e;
      }
    }
    if (!item.cancelled) refresh(); // in the import folder now: the list shows it there
  }

  // piece sends one piece with XMLHttpRequest, for its upload progress
  // (fetch has none), and resolves with the status and the server's JSON.
  function piece(item, offset, blob, progress) {
    return new Promise((resolve, reject) => {
      const x = new XMLHttpRequest();
      item.xhr = x;
      let last = Date.now();
      const watch = setInterval(() => { if (Date.now() - last > STALL) x.abort(); }, 5000);
      const end = () => { clearInterval(watch); item.xhr = null; };
      x.open('PATCH', '/api/uploads/' + item.id);
      x.setRequestHeader('Upload-Offset', String(offset));
      x.setRequestHeader('Content-Type', 'application/offset+octet-stream');
      x.upload.onprogress = (e) => { last = Date.now(); progress(e.loaded); };
      x.onload = () => {
        end();
        let body = {};
        try { body = JSON.parse(x.responseText); } catch (e) {}
        resolve({ status: x.status, body });
      };
      x.onerror = x.onabort = x.ontimeout = () => { end(); reject(new Error('network')); };
      x.send(blob);
    });
  }

  // --- the page: pickers, and drops anywhere on it ---

  function bind() {
    const p = page();
    if (!p || p.dataset.bound) return;
    p.dataset.bound = '1';
    const pick = $('pick'), pickdir = $('pickdir');
    if (pick) {
      if ('torrents' in p.dataset) pick.accept += ',.torrent';
      pick.addEventListener('change', () => {
        add(Array.from(pick.files).map((file) => ({ file, path: file.name })));
        pick.value = '';
      });
    }
    if (pickdir) pickdir.addEventListener('change', () => {
      add(Array.from(pickdir.files).map((file) => ({ file, path: file.webkitRelativePath || file.name })));
      pickdir.value = '';
    });
    overlay();
  }

  // Files dragged in from outside.
  const outside = (e) => e.dataTransfer && Array.from(e.dataTransfer.types || []).includes('Files');
  let depth = 0;
  const veil = (on) => { const v = $('drop-veil'); if (v) v.hidden = !on; };
  document.addEventListener('dragenter', (e) => {
    if (!outside(e) || !page()) return;
    depth++;
    veil(true);
  });
  document.addEventListener('dragleave', (e) => {
    if (!outside(e) || !page()) return;
    if (--depth <= 0) { depth = 0; veil(false); }
  });
  // Dropping a file anywhere shouldn't open it in the tab; on this page it adds it.
  window.addEventListener('dragover', (e) => e.preventDefault());
  window.addEventListener('drop', async (e) => {
    e.preventDefault();
    depth = 0;
    veil(false);
    if (!outside(e) || !page()) return;
    // Folders come as entries (the items list is only good until the
    // first await, so take the entries first).
    const dt = e.dataTransfer;
    const entries = Array.from(dt.items || []).map((i) => i.webkitGetAsEntry && i.webkitGetAsEntry()).filter(Boolean);
    if (entries.length) {
      const out = [];
      for (const en of entries) await walk(en, out);
      add(out);
    } else {
      add(Array.from(dt.files).map((file) => ({ file, path: file.name })));
    }
  });

  window.addEventListener('beforeunload', (e) => {
    if (current || queue.length) { e.preventDefault(); e.returnValue = ''; }
  });

  // Each refresh of the list: this page's uploads again. A ✕ on one of
  // ours stops it here too (htmx's request is the server's half).
  document.addEventListener('htmx:afterSwap', () => { bind(); overlay(); });
  document.addEventListener('htmx:beforeRequest', (e) => {
    const key = e.detail.elt && e.detail.elt.dataset && e.detail.elt.dataset.cancelUpload;
    if (!key) return;
    for (const it of [current, ...queue]) {
      if (it && rowKey(it) === key) { it.cancelled = true; if (it.xhr) it.xhr.abort(); }
    }
  });

  // --- the download queue: drag a row up or down to reorder it ---
  // With pointer events rather than the browser's drag and drop, so the row
  // lifts out of the list whole and solid (the browser's would be a faded
  // picture of it), the list closes up behind it, a line shows where it
  // will land, and it works with a finger too (by its handle, so a finger
  // elsewhere still scrolls). On letting go, where it landed (before the
  // next download, or after the last) goes to the server, which answers
  // with the list. #incoming.dragging holds off the refresh meanwhile.

  let lift = null; // {li, id, x0, y0, dy, line, moved}
  const queueRows = () => Array.from(document.querySelectorAll('#inc-rows li[data-drag]'));

  document.addEventListener('pointerdown', (e) => {
    const li = e.target.closest && e.target.closest('#inc-rows li[data-drag]');
    if (!li || e.button !== 0 || e.target.closest('button, a, summary, input, form, .inc-facts')) return;
    if (e.pointerType !== 'mouse' && !e.target.closest('.inc-handle')) return;
    lift = { li, id: li.dataset.id, x0: e.clientX, y0: e.clientY, moved: false, pointer: e.pointerId };
  });

  function startLift(e) {
    const { li } = lift;
    const r = li.getBoundingClientRect();
    lift.moved = true;
    lift.dy = e.clientY - r.top;
    lift.next = li.nextElementSibling;
    // A line where it would land, then the row out of the list's flow.
    lift.line = document.createElement('li');
    lift.line.className = 'drop-line';
    lift.line.setAttribute('aria-hidden', 'true');
    li.after(lift.line);
    li.style.width = r.width + 'px';
    li.style.left = r.left + 'px';
    li.style.top = r.top + 'px';
    li.classList.add('lifted');
    li.setPointerCapture(lift.pointer);
    $('incoming').classList.add('dragging');
    document.body.classList.add('lifting');
  }

  document.addEventListener('pointermove', (e) => {
    if (!lift || e.pointerId !== lift.pointer) return;
    if (!lift.moved) {
      if (Math.hypot(e.clientX - lift.x0, e.clientY - lift.y0) < 5) return;
      startLift(e);
    }
    e.preventDefault();
    lift.li.style.top = (e.clientY - lift.dy) + 'px';
    // The line goes before the first other row whose middle is below the pointer.
    const others = queueRows().filter((r) => r !== lift.li);
    const over = others.find((r) => { const b = r.getBoundingClientRect(); return e.clientY < b.top + b.height / 2; });
    if (over) over.before(lift.line);
    else if (others.length) others[others.length - 1].after(lift.line);
    // Near the window's edge, scroll.
    if (e.clientY < 48) window.scrollBy(0, -12);
    else if (e.clientY > window.innerHeight - 48) window.scrollBy(0, 12);
  });

  function drop(cancel) {
    const { li, line, moved, next } = lift;
    lift = null;
    if (!moved) return;
    li.classList.remove('lifted');
    li.style.width = li.style.left = li.style.top = '';
    document.body.classList.remove('lifting');
    const inc = $('incoming');
    if (inc) inc.classList.remove('dragging');
    if (cancel) { line.remove(); return; } // (the row never left its place in the list)
    line.replaceWith(li);
    if (li.nextElementSibling === next) return; // back where it was
    const rows = queueRows(), i = rows.indexOf(li);
    const values = i + 1 < rows.length ? { before: rows[i + 1].dataset.id } : { after: rows[i - 1].dataset.id };
    if (window.htmx) htmx.ajax('POST', '/ui/downloads/' + li.dataset.id + '/move', { target: '#incoming', swap: 'outerHTML', values });
  }
  document.addEventListener('pointerup', (e) => { if (lift && e.pointerId === lift.pointer) drop(false); });
  document.addEventListener('pointercancel', (e) => { if (lift && e.pointerId === lift.pointer) drop(true); });
  document.addEventListener('keydown', (e) => { if (lift && lift.moved && e.key === 'Escape') drop(true); });

  // --- Needs you: the dialog that confirms a title ---
  // A card's title (or a search for a reference) fills the dialog; it opens
  // when it's filled. Import closes it once the server's taken the answer.
  // If the item goes meanwhile (answered at a terminal), the next refresh
  // says so in the dialog.

  const dialog = () => $('import-dialog');
  const closeDialog = () => document.querySelectorAll('dialog[open]').forEach((d) => d.close());
  document.addEventListener('htmx:afterSwap', (e) => {
    const d = dialog();
    if (!d) return;
    if (e.detail.target && e.detail.target.id === 'import-dialog-body') {
      if (!d.open) d.showModal();
      return;
    }
    const dlg = d.open && d.querySelector('.dlg[data-item]');
    if (!dlg || !$('incoming') || dlg.dataset.gone) return;
    const item = dlg.dataset.item;
    const still = Array.from(document.querySelectorAll('#incoming .need[data-item]')).some((n) => n.dataset.item === item);
    if (!still) {
      dlg.dataset.gone = '1';
      const err = dlg.querySelector('.dlg-err');
      if (err) err.textContent = 'This has been dealt with elsewhere (imported at a terminal, or moved), so there\'s nothing to import here.';
      const submit = dlg.querySelector('button[type=submit]');
      if (submit) submit.disabled = true;
    }
  });
  document.addEventListener('click', (e) => {
    if (!e.target.closest) return;
    const close = e.target.closest('[data-close]');
    if (close) { const d = close.closest('dialog'); if (d) d.close(); return; }
    // The head's "16 GB in trash" offers to empty it.
    const open = e.target.closest('[data-open]');
    const d = open && $(open.dataset.open);
    if (!d) return;
    d.querySelectorAll('.trash-size').forEach((s) => { s.textContent = open.dataset.size || ''; });
    d.showModal();
  });
  document.addEventListener('import-chosen', closeDialog);
  document.addEventListener('trash-emptied', closeDialog);

  window.mvdIncoming = { bind };
  bind();
})();
