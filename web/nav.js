// Shared right-hand navigation sidebar, injected into every control page so the
// page-to-page links live in one consistent place instead of cluttering each top
// bar. Collapsed it shows just clickable page icons; expanded it adds the page
// names and, at the top, the remote-access (LAN) info and QR-code button.
//
// Pages opt in with a single <script src="/nav.js"></script>. The active page is
// detected from the URL. Standard library only on the server side; no framework here.
(function () {
  var PAGES = [
    { href: '/console',         icon: '🎛️', label: 'Console' },
    { href: '/admin',           icon: '🖼️', label: 'Upload / Reorder' },
    { href: '/categories',      icon: '🗂️', label: 'Manage categories' },
    { href: '/score',           icon: '🏆', label: 'Scoring' },
    { href: '/archived',        icon: '🗄️', label: 'Archived Sessions' },
    { href: '/stats',           icon: '📊', label: 'Statistics' },
    { href: '/settings',        icon: '⚙️', label: 'Settings' },
    { href: '/how-to', icon: '📖', label: 'How To' }
  ];
  var RAIL = 54, OPEN = 248, MOBILE = 760;
  var KEY = 'pjnav-expanded';

  // ---- styles -------------------------------------------------------------
  var css = `
  body{margin-right:${RAIL}px !important;transition:margin-right .18s ease}
  #toasts{right:${RAIL + 16}px !important}
  #pjnav{position:fixed;top:0;right:0;bottom:0;width:${RAIL}px;background:#16181c;
    border-left:1px solid #2c2f35;z-index:5000;display:flex;flex-direction:column;
    overflow-x:hidden;overflow-y:auto;transition:width .18s ease,transform .18s ease;
    font-family:system-ui,Segoe UI,sans-serif}
  #pjnav.expanded{width:${OPEN}px}
  #pjnav-fab{position:fixed;bottom:16px;right:16px;z-index:5001;display:none;
    width:48px;height:48px;align-items:center;justify-content:center;font-size:22px;
    background:#23262c;color:#e8e8e8;border:1px solid #3a3e45;border-radius:10px;
    cursor:pointer;box-shadow:0 3px 12px rgba(0,0,0,.5)}
  #pjnav .pjnav-toggle{flex:none;background:none;border:none;color:#cfd2d6;font-size:22px;
    line-height:1;cursor:pointer;padding:14px;width:100%;text-align:left}
  #pjnav .pjnav-toggle:hover{color:#fff;background:#1f2228}
  #pjnav .pjnav-remote{display:none;flex:none;margin:4px 12px 10px;padding:12px;
    background:#22252b;border:1px solid #2c2f35;border-radius:8px;font-size:13px;color:#c9ccd1}
  #pjnav.expanded .pjnav-remote.show{display:block}
  #pjnav .pjnav-remote .rt{color:#9aa0a6;font-size:12.5px;line-height:1.45;margin-bottom:9px}
  #pjnav .pjnav-remote a{display:block;color:#7fb2ff;text-decoration:none;font-weight:600;
    font-variant-numeric:tabular-nums;word-break:break-all;margin-bottom:10px}
  #pjnav .pjnav-remote a:hover{text-decoration:underline}
  #pjnav .pjnav-qr{width:100%;font-size:13px;padding:8px 10px;background:#2a2d33;color:#eee;
    border:1px solid #444;border-radius:6px;cursor:pointer}
  #pjnav .pjnav-qr:hover{background:#363a42}
  #pjnav .pjnav-links{flex:1 1 auto;display:flex;flex-direction:column;padding:4px 0}
  #pjnav .pjnav-links a{display:flex;align-items:center;gap:14px;padding:13px 15px;
    color:#cfd2d6;text-decoration:none;white-space:nowrap;border-left:3px solid transparent}
  #pjnav .pjnav-links a:hover{background:#1f2228;color:#fff}
  #pjnav .pjnav-links a.active{background:#1f2530;color:#fff;border-left-color:#2d6cdf}
  #pjnav .pjnav-links a .ic{font-size:20px;width:24px;text-align:center;flex:none}
  #pjnav .pjnav-links a .lbl{font-size:14px;opacity:0;transition:opacity .12s ease}
  #pjnav.expanded .pjnav-links a .lbl{opacity:1}
  #pjnav-backdrop{position:fixed;inset:0;background:rgba(0,0,0,.45);z-index:4999;display:none}
  #pjnav-backdrop.show{display:block}
  #pjnav-qrmodal{position:fixed;inset:0;background:rgba(0,0,0,.6);display:none;
    align-items:center;justify-content:center;z-index:6000}
  #pjnav-qrmodal.show{display:flex}
  #pjnav-qrmodal .box{background:#23262c;border:1px solid #3a3e45;border-radius:8px;
    padding:20px;max-width:340px;text-align:center;color:#e8e8e8;font-family:system-ui,sans-serif}
  #pjnav-qrmodal h3{margin:0 0 4px;font-size:15px}
  #pjnav-qrmodal p{margin:4px 0 12px;color:#9aa0a6;font-size:13px}
  #pjnav-qrmodal img{width:280px;height:280px;max-width:70vw;background:#fff;border-radius:6px;image-rendering:pixelated}
  #pjnav-qrmodal .u{margin:12px 0 4px;font-weight:600;font-variant-numeric:tabular-nums;word-break:break-all}
  #pjnav-qrmodal select{margin-top:10px;max-width:100%;font-size:14px;padding:7px 9px;
    background:#2a2d33;color:#eee;border:1px solid #444;border-radius:5px}
  #pjnav-qrmodal button{margin-top:14px;font-size:14px;padding:8px 14px;background:#2a2d33;
    color:#eee;border:1px solid #444;border-radius:5px;cursor:pointer}
  @media (max-width:${MOBILE}px){
    body{margin-right:0 !important}              /* no rail on phones */
    #toasts{right:auto !important;left:16px !important}  /* clear of the corner hamburger */
    /* The menu is a hidden drawer; a floating hamburger opens it full. */
    #pjnav{width:min(${OPEN}px,86vw);transform:translateX(100%);
      box-shadow:-8px 0 24px rgba(0,0,0,.5)}
    #pjnav.expanded{transform:translateX(0)}
  }`;
  var style = document.createElement('style');
  style.textContent = css;
  document.head.appendChild(style);

  // ---- markup -------------------------------------------------------------
  function el(html) { var d = document.createElement('div'); d.innerHTML = html.trim(); return d.firstChild; }

  var here = location.pathname.replace(/\/+$/, '') || '/';
  var linksHtml = PAGES.map(function (p) {
    var active = (p.href === '/' ? here === '/' : here === p.href) ? ' active' : '';
    return '<a href="' + p.href + '" class="pjnav-link' + active + '" title="' + p.label + '">' +
           '<span class="ic">' + p.icon + '</span><span class="lbl">' + p.label + '</span></a>';
  }).join('');

  var nav = el(
    '<aside id="pjnav">' +
      '<button class="pjnav-toggle" aria-label="Toggle menu">☰</button>' +
      '<div class="pjnav-remote">' +
        '<div class="rt">Share this web address or QR code to give others access to this app.</div>' +
        '<a class="pjnav-url" target="_blank" rel="noopener"></a>' +
        '<button class="pjnav-qr">📱 Show QR code</button>' +
      '</div>' +
      '<nav class="pjnav-links">' + linksHtml + '</nav>' +
    '</aside>');
  var fab = el('<button id="pjnav-fab" aria-label="Open menu">☰</button>');
  var backdrop = el('<div id="pjnav-backdrop"></div>');
  var modal = el(
    '<div id="pjnav-qrmodal"><div class="box">' +
      '<h3>Connect from another device</h3>' +
      '<p>Scan with a phone or tablet on the same network.</p>' +
      '<img alt="QR code"><div class="u"></div>' +
      '<select style="display:none"></select><br><button class="cl">Close</button>' +
    '</div></div>');
  document.body.appendChild(nav);
  document.body.appendChild(fab);
  document.body.appendChild(backdrop);
  document.body.appendChild(modal);

  var toggle = nav.querySelector('.pjnav-toggle');
  var remote = nav.querySelector('.pjnav-remote');
  var urlLink = nav.querySelector('.pjnav-url');
  var qrBtn = nav.querySelector('.pjnav-qr');
  var qrImg = modal.querySelector('img');
  var qrUrl = modal.querySelector('.u');
  var qrSel = modal.querySelector('select');

  // ---- expand / collapse --------------------------------------------------
  // Desktop: a 54px rail of icons that widens to 248px (and pushes the page over).
  // Mobile: no rail — the whole menu is hidden off-screen and a floating hamburger
  // (#pjnav-fab) opens it as a full drawer over the page, with a dimming backdrop.
  function isMobile() { return window.innerWidth <= MOBILE; }
  function setExpanded(on) {
    nav.classList.toggle('expanded', on);
    toggle.textContent = on ? '✕' : '☰';
    if (isMobile()) {
      document.body.style.marginRight = '0px';
      backdrop.classList.toggle('show', on);
      fab.style.display = on ? 'none' : 'flex';
    } else {
      document.body.style.marginRight = (on ? OPEN : RAIL) + 'px';
      backdrop.classList.remove('show');
      fab.style.display = 'none';
    }
    try { localStorage.setItem(KEY, on ? '1' : '0'); } catch (e) {}
  }
  toggle.addEventListener('click', function () { setExpanded(!nav.classList.contains('expanded')); });
  fab.addEventListener('click', function () { setExpanded(true); });
  backdrop.addEventListener('click', function () { setExpanded(false); });
  window.addEventListener('resize', function () {
    // Re-apply margins/fab/backdrop for the current width without changing whether
    // the menu is open (crossing the mobile breakpoint switches rail <-> drawer).
    setExpanded(nav.classList.contains('expanded'));
  });
  var saved = '0';
  try { saved = localStorage.getItem(KEY) || '0'; } catch (e) {}
  setExpanded(saved === '1' && !isMobile()); // phones always start closed (drawer hidden)

  // ---- QR modal -----------------------------------------------------------
  function showQR(url) { qrImg.src = '/api/qr?data=' + encodeURIComponent(url); qrUrl.textContent = url; }
  function openModal(urls) {
    if (qrSel) {
      if (urls.length > 1) {
        qrSel.innerHTML = '';
        urls.forEach(function (u) { var o = document.createElement('option'); o.value = u; o.textContent = u; qrSel.appendChild(o); });
        qrSel.style.display = '';
      } else { qrSel.style.display = 'none'; }
    }
    showQR(urls[0]);
    modal.classList.add('show');
  }
  modal.querySelector('.cl').addEventListener('click', function () { modal.classList.remove('show'); });
  modal.addEventListener('click', function (e) { if (e.target === modal) modal.classList.remove('show'); });
  qrSel.addEventListener('change', function (e) { showQR(e.target.value); });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape') modal.classList.remove('show'); });

  // ---- remote-access info (only when LAN access is on) --------------------
  fetch('/api/netinfo').then(function (r) { return r.ok ? r.json() : null; }).then(function (info) {
    var urls = (info && info.urls) || [];
    if (!urls.length) return;             // lanAccess off → no remote section
    urlLink.textContent = urls[0];
    urlLink.href = urls[0];
    remote.classList.add('show');
    qrBtn.addEventListener('click', function () { openModal(urls); });
  }).catch(function () {});
})();
