// Import / Export wizard for the operator console. One button ("⇄ Import / Export")
// opens this modal, which has two tabs:
//
//   Export  — a filterable, sortable, paginated list of every session (live and
//             archived). "Add →" drops a session into a basket; Export downloads the
//             basket as a .pjs (one session) or .pjss (several). Each row also has a
//             quick per-row export.
//   Import  — choose one or more .pjs/.pjss files; a preview table lists the sessions
//             inside. Tick which to import and (optionally) one to make the active
//             session, then Import. Imported sessions always get fresh IDs; the table
//             then shows the old→new mapping.
//
// Styled to match the app's dark modal cards. Standard library only on the server; no
// framework here. Exposes window.openPortationWizard(); the console wires the button
// and provides window.pjSelectSession(id) for "make active".
(function () {
  var Z = 6800;
  var css = `
  #pjio{position:fixed;inset:0;background:rgba(0,0,0,.6);display:none;align-items:center;justify-content:center;z-index:${Z};
    font-family:system-ui,Segoe UI,sans-serif}
  #pjio.show{display:flex}
  #pjio .shell{background:#23262c;border:1px solid #3a3e45;border-radius:10px;color:#e8e8e8;
    width:min(960px,96vw);max-height:92vh;display:flex;flex-direction:column;box-shadow:0 12px 48px rgba(0,0,0,.6)}
  #pjio .top{display:flex;align-items:center;gap:14px;padding:14px 18px;border-bottom:1px solid #2c2f35}
  #pjio .top h2{margin:0;font-size:16px;flex:none}
  #pjio .tabs{display:flex;gap:6px}
  #pjio .tab{padding:7px 14px;background:#2a2d33;border:1px solid #444;border-radius:6px;cursor:pointer;font-size:14px;color:#cfd2d6}
  #pjio .tab.on{background:#274a8a;border-color:#2d6cdf;color:#fff}
  #pjio .x{margin-left:auto;background:none;border:none;color:#9aa0a6;font-size:22px;cursor:pointer;line-height:1}
  #pjio .x:hover{color:#fff}
  #pjio .body{padding:16px 18px;overflow:auto}
  #pjio .filters{display:flex;flex-wrap:wrap;gap:10px;align-items:end;margin-bottom:12px}
  #pjio .filters .f{display:flex;flex-direction:column;gap:4px}
  #pjio .filters label{font-size:12px;color:#9aa0a6}
  #pjio input,#pjio select{font-size:14px;padding:7px 9px;background:#2a2d33;color:#eee;border:1px solid #444;border-radius:5px;box-sizing:border-box}
  #pjio button{cursor:pointer}
  #pjio .tablewrap{border:1px solid #2c2f35;border-radius:8px;overflow:auto;max-height:38vh}
  #pjio table{width:100%;border-collapse:collapse;font-size:13.5px}
  #pjio th,#pjio td{padding:8px 10px;text-align:left;border-bottom:1px solid #2c2f35;white-space:nowrap;vertical-align:middle}
  #pjio thead th{position:sticky;top:0;background:#1f2228;color:#9aa0a6;font-weight:500;z-index:1}
  #pjio th.sortable{cursor:pointer;user-select:none}
  #pjio th.sortable:hover{color:#fff}
  #pjio td.desc,#pjio th.desc{max-width:260px;overflow:hidden;text-overflow:ellipsis}
  #pjio tr.empty td{color:#777;text-align:center;padding:18px}
  #pjio .pill{font-size:11.5px;padding:2px 8px;border-radius:10px}
  #pjio .pill.arch{background:#5a4a1e;color:#ffe9b0}
  #pjio .pill.live{background:#1e4a2e;color:#bfe9c9}
  #pjio .mini{font-size:12.5px;padding:5px 9px;background:#2a2d33;color:#eee;border:1px solid #444;border-radius:5px}
  #pjio .mini:hover{background:#363a42}
  #pjio .mini.primary{background:#274a8a;border-color:#2d6cdf}
  #pjio .mini.primary:hover{background:#2f5aa8}
  #pjio .pager{display:flex;align-items:center;gap:12px;margin-top:10px;color:#9aa0a6;font-size:13px}
  #pjio .basket{margin-top:16px}
  #pjio .basket h3{margin:0 0 8px;font-size:14px;color:#cfd2d6}
  #pjio .foot{display:flex;gap:10px;justify-content:flex-end;align-items:center;padding:14px 18px;border-top:1px solid #2c2f35}
  #pjio .foot .note{margin-right:auto;color:#9aa0a6;font-size:13px}
  #pjio .foot button{font-size:14px;padding:8px 16px;background:#2a2d33;color:#eee;border:1px solid #444;border-radius:6px}
  #pjio .foot button:hover{background:#363a42}
  #pjio .foot button.primary{background:#274a8a;border-color:#2d6cdf}
  #pjio .foot button.primary:hover{background:#2f5aa8}
  #pjio .foot button[disabled]{opacity:.5;cursor:default}
  #pjio .drop{border:1.5px dashed #3a3e45;border-radius:8px;padding:22px;text-align:center;color:#9aa0a6;background:#1f2228;cursor:pointer}
  #pjio .drop:hover{border-color:#2d6cdf;color:#cfd2d6}
  #pjio .err{color:#e0a0a0;font-size:13px;margin-top:8px;white-space:pre-wrap}
  #pjio .newid{color:#7fd29a;font-variant-numeric:tabular-nums}
  @media (max-width:760px){ #pjio th,#pjio td{white-space:normal} #pjio .tablewrap{max-height:34vh} }`;

  var modal = null, els = {};
  // Export state.
  var sort = { key: 'date', dir: 'desc' }, page = 1, basket = new Map(), pageInfo = { total: 0, pageSize: 50 };
  var searchTimer = null;
  // Import state.
  var importToken = null, importRows = [], importDone = false;

  function h(html) { var d = document.createElement('div'); d.innerHTML = html.trim(); return d.firstChild; }
  function trunc(s, n) { s = s || ''; return s.length > n ? s.slice(0, n - 1) + '…' : s; }

  function build() {
    var style = document.createElement('style'); style.textContent = css; document.head.appendChild(style);
    modal = h(
      '<div id="pjio"><div class="shell">' +
        '<div class="top"><h2>Import / Export sessions</h2>' +
          '<div class="tabs"><button class="tab on" data-tab="export">Export</button>' +
          '<button class="tab" data-tab="import">Import</button></div>' +
          '<button class="x" title="Close">✕</button></div>' +
        '<div class="body"></div>' +
        '<div class="foot"></div>' +
      '</div></div>');
    document.body.appendChild(modal);
    els.body = modal.querySelector('.body');
    els.foot = modal.querySelector('.foot');
    modal.querySelector('.x').addEventListener('click', close);
    modal.addEventListener('click', function (e) { if (e.target === modal) close(); });
    modal.querySelectorAll('.tab').forEach(function (t) {
      t.addEventListener('click', function () { showTab(t.dataset.tab); });
    });
    document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && modal.classList.contains('show')) close(); });
  }

  function close() { modal.classList.remove('show'); }

  function showTab(name) {
    modal.querySelectorAll('.tab').forEach(function (t) { t.classList.toggle('on', t.dataset.tab === name); });
    if (name === 'export') renderExport(); else renderImport();
  }

  function download(url) {
    var a = document.createElement('a'); a.href = url; a.download = '';
    document.body.appendChild(a); a.click(); a.remove();
  }

  // ---- EXPORT --------------------------------------------------------------
  function renderExport() {
    els.body.innerHTML =
      '<div class="filters">' +
        '<div class="f"><label>Show</label><select id="pjio-arch"><option value="all">All sessions</option>' +
          '<option value="no">Active only</option><option value="yes">Archived only</option></select></div>' +
        '<div class="f"><label>Photographer</label><input id="pjio-photog" type="search" placeholder="name contains…"></div>' +
        '<div class="f"><label>Photo title</label><input id="pjio-title" type="search" placeholder="title contains…"></div>' +
        '<div class="f"><label>From</label><input id="pjio-from" type="date"></div>' +
        '<div class="f"><label>To</label><input id="pjio-to" type="date"></div>' +
      '</div>' +
      '<div class="tablewrap"><table><thead><tr>' +
        '<th class="sortable" data-k="id">Id</th>' +
        '<th class="sortable" data-k="date">Date</th>' +
        '<th class="desc">Description</th>' +
        '<th>Status</th>' +
        '<th class="sortable" data-k="created">Created</th>' +
        '<th>Photos</th><th></th>' +
      '</tr></thead><tbody id="pjio-src"></tbody></table></div>' +
      '<div class="pager"><button class="mini" id="pjio-prev">‹ Prev</button>' +
        '<span id="pjio-pageinfo"></span><button class="mini" id="pjio-next">Next ›</button></div>' +
      '<div class="basket"><h3>Selected for export (<span id="pjio-bn">0</span>)</h3>' +
        '<div class="tablewrap" style="max-height:22vh"><table><thead><tr>' +
          '<th>Id</th><th>Date</th><th class="desc">Description</th><th>Status</th><th></th>' +
        '</tr></thead><tbody id="pjio-basket"></tbody></table></div></div>';

    els.foot.innerHTML =
      '<span class="note" id="pjio-foonote"></span>' +
      '<button id="pjio-cancel">Cancel</button>' +
      '<button class="primary" id="pjio-export">Export</button>';

    var id = function (x) { return document.getElementById(x); };
    id('pjio-arch').value = curArch();
    id('pjio-cancel').onclick = close;
    id('pjio-export').onclick = exportBasket;
    id('pjio-prev').onclick = function () { if (page > 1) { page--; loadSource(); } };
    id('pjio-next').onclick = function () {
      if (page * pageInfo.pageSize < pageInfo.total) { page++; loadSource(); }
    };
    ['pjio-arch'].forEach(function (k) { id(k).onchange = function () { page = 1; loadSource(); }; });
    ['pjio-photog', 'pjio-title', 'pjio-from', 'pjio-to'].forEach(function (k) {
      id(k).oninput = function () { clearTimeout(searchTimer); searchTimer = setTimeout(function () { page = 1; loadSource(); }, 300); };
    });
    els.body.querySelectorAll('th.sortable').forEach(function (th) {
      th.onclick = function () {
        var k = th.dataset.k;
        if (sort.key === k) sort.dir = sort.dir === 'asc' ? 'desc' : 'asc';
        else { sort.key = k; sort.dir = 'asc'; }
        loadSource();
      };
    });
    renderBasket();
    loadSource();
  }

  function curArch() { var e = document.getElementById('pjio-arch'); return e ? e.value : 'all'; }

  function loadSource() {
    var id = function (x) { return document.getElementById(x); };
    var qs = new URLSearchParams({
      archived: id('pjio-arch').value, photographer: id('pjio-photog').value.trim(),
      title: id('pjio-title').value.trim(), from: id('pjio-from').value, to: id('pjio-to').value,
      sort: sort.key, dir: sort.dir, page: page
    });
    fetch('/api/sessions/all?' + qs).then(function (r) { return r.json(); }).then(function (data) {
      pageInfo = { total: data.total || 0, pageSize: data.pageSize || 50 };
      renderSource(data.sessions || []);
      var first = (data.total === 0) ? 0 : (page - 1) * pageInfo.pageSize + 1;
      var last = Math.min(page * pageInfo.pageSize, data.total);
      id('pjio-pageinfo').textContent = data.total ? (first + '–' + last + ' of ' + data.total) : 'No sessions';
      id('pjio-prev').disabled = page <= 1;
      id('pjio-next').disabled = last >= data.total;
    });
  }

  function statusPill(archived) {
    return archived ? '<span class="pill arch">Archived</span>' : '<span class="pill live">Active</span>';
  }

  function renderSource(rows) {
    var tb = document.getElementById('pjio-src'); tb.innerHTML = '';
    if (!rows.length) { tb.appendChild(h('<tr class="empty"><td colspan="7">No sessions match your filters.</td></tr>')); return; }
    rows.forEach(function (s) {
      var tr = document.createElement('tr');
      tr.appendChild(cell('#' + s.id));
      tr.appendChild(cell(s.date));
      var d = cell(trunc(s.description, 40)); d.className = 'desc'; if (s.description) d.title = s.description; tr.appendChild(d);
      tr.appendChild(cellHTML(statusPill(s.archived)));
      tr.appendChild(cell((s.created || '').slice(0, 10)));
      tr.appendChild(cell(String(s.photoCount)));
      var td = document.createElement('td');
      var add = mk('button', 'mini', basket.has(s.id) ? 'Added ✓' : 'Add →'); add.disabled = basket.has(s.id);
      add.onclick = function () { basket.set(s.id, s); add.textContent = 'Added ✓'; add.disabled = true; renderBasket(); };
      var exp = mk('button', 'mini', '⤓ .pjs'); exp.title = 'Export just this session';
      exp.onclick = function () { download('/api/export?ids=' + encodeURIComponent(s.id)); };
      td.appendChild(add); td.appendChild(document.createTextNode(' ')); td.appendChild(exp);
      tr.appendChild(td);
      tb.appendChild(tr);
    });
  }

  function renderBasket() {
    var tb = document.getElementById('pjio-basket'); if (!tb) return;
    tb.innerHTML = '';
    document.getElementById('pjio-bn').textContent = basket.size;
    document.getElementById('pjio-export').textContent = basket.size > 1 ? ('Export ' + basket.size + ' (.pjss)') : 'Export (.pjs)';
    document.getElementById('pjio-export').disabled = basket.size === 0;
    if (!basket.size) { tb.appendChild(h('<tr class="empty"><td colspan="5">Nothing selected yet — use “Add →” above.</td></tr>')); return; }
    basket.forEach(function (s, id) {
      var tr = document.createElement('tr');
      tr.appendChild(cell('#' + id));
      tr.appendChild(cell(s.date));
      var d = cell(trunc(s.description, 40)); d.className = 'desc'; if (s.description) d.title = s.description; tr.appendChild(d);
      tr.appendChild(cellHTML(statusPill(s.archived)));
      var td = document.createElement('td');
      var rm = mk('button', 'mini', 'Remove');
      rm.onclick = function () { basket.delete(id); renderBasket(); renderSourceAddState(id); };
      td.appendChild(rm); tr.appendChild(td);
      tb.appendChild(tr);
    });
  }

  // Re-enable a source row's "Add →" button after it's removed from the basket.
  function renderSourceAddState(id) {
    var tb = document.getElementById('pjio-src'); if (!tb) return;
    Array.prototype.forEach.call(tb.querySelectorAll('button.mini'), function (b) {
      if (b.textContent === 'Added ✓' && b.parentElement && b.parentElement.parentElement &&
          b.parentElement.parentElement.firstChild && b.parentElement.parentElement.firstChild.textContent === '#' + id) {
        b.textContent = 'Add →'; b.disabled = false;
      }
    });
  }

  function exportBasket() {
    if (!basket.size) return;
    var ids = Array.from(basket.keys());
    download('/api/export?ids=' + ids.map(encodeURIComponent).join(','));
    if (window.toast) window.toast('Exported ' + ids.length + ' session' + (ids.length > 1 ? 's' : ''));
  }

  // ---- IMPORT --------------------------------------------------------------
  function renderImport() {
    importToken = null; importRows = []; importDone = false;
    els.body.innerHTML =
      '<div class="drop" id="pjio-drop">📦 Choose .pjs / .pjss file(s) to import<br>' +
        '<span style="font-size:12.5px">or drag them here</span></div>' +
      '<input id="pjio-file" type="file" accept=".pjs,.pjss" multiple style="display:none">' +
      '<div id="pjio-importbox"></div>' +
      '<div class="err" id="pjio-importerr"></div>';
    els.foot.innerHTML =
      '<span class="note" id="pjio-foonote"></span>' +
      '<button id="pjio-icancel">Cancel</button>' +
      '<button class="primary" id="pjio-commit" disabled>Import</button>';
    var id = function (x) { return document.getElementById(x); };
    id('pjio-icancel').onclick = close;
    id('pjio-commit').onclick = commitImport;
    id('pjio-drop').onclick = function () { id('pjio-file').click(); };
    id('pjio-file').onchange = function () { if (this.files.length) preview(this.files); };
    var drop = id('pjio-drop');
    drop.addEventListener('dragover', function (e) { e.preventDefault(); drop.style.borderColor = '#2d6cdf'; });
    drop.addEventListener('dragleave', function () { drop.style.borderColor = ''; });
    drop.addEventListener('drop', function (e) {
      e.preventDefault(); drop.style.borderColor = '';
      if (e.dataTransfer.files.length) preview(e.dataTransfer.files);
    });
  }

  function preview(files) {
    var fd = new FormData();
    for (var i = 0; i < files.length; i++) fd.append('files', files[i]);
    document.getElementById('pjio-importerr').textContent = '';
    document.getElementById('pjio-importbox').innerHTML = '<p style="color:#9aa0a6">Reading…</p>';
    fetch('/api/import/preview', { method: 'POST', body: fd }).then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t); });
      return r.json();
    }).then(function (data) {
      importToken = data.token; importRows = data.sessions || []; importDone = false;
      renderPreview(data.errors || []);
    }).catch(function (e) {
      document.getElementById('pjio-importbox').innerHTML = '';
      document.getElementById('pjio-importerr').textContent = String(e.message || e);
    });
  }

  function renderPreview(errors) {
    var box = document.getElementById('pjio-importbox');
    if (!importRows.length) { box.innerHTML = ''; return; }
    var only = importRows.length === 1;
    var html = '<div class="tablewrap" style="margin-top:14px;max-height:50vh"><table><thead><tr>' +
      '<th>Old Id</th><th>New Id</th><th>Date</th><th class="desc">Description</th><th>Status</th>' +
      '<th>Import</th><th>Make active</th></tr></thead><tbody>';
    importRows.forEach(function (s, i) {
      html += '<tr data-i="' + i + '">' +
        '<td>#' + esc(s.origId) + '</td>' +
        '<td class="newid" data-new>assigned on import</td>' +
        '<td>' + esc(s.date) + '</td>' +
        '<td class="desc" title="' + esc(s.description || '') + '">' + esc(trunc(s.description, 40)) + '</td>' +
        '<td>' + statusPill(s.archived) + '</td>' +
        '<td><input type="checkbox" class="imp" checked></td>' +
        '<td>' + (s.archived ? '<span class="muted" style="color:#777">—</span>' : '<input type="checkbox" class="act"' + (only ? ' checked' : '') + '>') + '</td>' +
      '</tr>';
    });
    html += '</tbody></table></div>';
    box.innerHTML = html;
    if (errors.length) document.getElementById('pjio-importerr').textContent = errors.join('\n');

    // "Make active" is at most one across all rows.
    box.querySelectorAll('input.act').forEach(function (cb) {
      cb.addEventListener('change', function () {
        if (cb.checked) box.querySelectorAll('input.act').forEach(function (o) { if (o !== cb) o.checked = false; });
      });
    });
    box.querySelectorAll('input.imp').forEach(function (cb) { cb.addEventListener('change', updateCommit); });
    updateCommit();
  }

  function updateCommit() {
    var box = document.getElementById('pjio-importbox');
    var n = box ? box.querySelectorAll('input.imp:checked').length : 0;
    var btn = document.getElementById('pjio-commit');
    btn.disabled = n === 0 || importDone;
    btn.textContent = importDone ? 'Imported ✓' : (n ? ('Import ' + n) : 'Import');
  }

  function commitImport() {
    var box = document.getElementById('pjio-importbox');
    var rowEls = Array.prototype.slice.call(box.querySelectorAll('tr[data-i]'));
    var sel = [], chosen = [], activeIdx = -1;
    rowEls.forEach(function (tr) {
      var i = +tr.dataset.i;
      if (tr.querySelector('input.imp').checked) {
        sel.push({ file: importRows[i].file, origId: importRows[i].origId });
        chosen.push(tr);
        var act = tr.querySelector('input.act');
        if (act && act.checked) activeIdx = chosen.length - 1;
      }
    });
    if (!sel.length) return;
    document.getElementById('pjio-commit').disabled = true;
    fetch('/api/import/commit', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token: importToken, select: sel })
    }).then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t); });
      return r.json();
    }).then(function (data) {
      var map = data.imported || [];
      map.forEach(function (m, k) {
        var tr = chosen[k]; if (!tr) return;
        var cell = tr.querySelector('[data-new]'); if (cell) cell.textContent = '#' + m.newId;
      });
      importDone = true; updateCommit();
      if (activeIdx >= 0 && map[activeIdx] && window.pjSelectSession) window.pjSelectSession(map[activeIdx].newId);
      if (window.toast) window.toast('Imported ' + map.length + ' session' + (map.length > 1 ? 's' : ''));
    }).catch(function (e) {
      document.getElementById('pjio-importerr').textContent = String(e.message || e);
      updateCommit();
    });
  }

  // ---- small DOM helpers ---------------------------------------------------
  function cell(text) { var td = document.createElement('td'); td.textContent = text; return td; }
  function cellHTML(html) { var td = document.createElement('td'); td.innerHTML = html; return td; }
  function mk(tag, cls, text) { var e = document.createElement(tag); e.className = cls; e.textContent = text; return e; }
  function esc(s) { return String(s == null ? '' : s).replace(/[&<>"]/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]; }); }

  window.openPortationWizard = function () {
    if (!modal) build();
    basket.clear();
    modal.classList.add('show');
    showTab('export');
  };
})();
