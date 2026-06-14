// Shared in-app dialogs that replace the browser's native alert()/confirm()/prompt()
// boxes with modals styled to match the QR-code modal in nav.js — a dark, centered
// card over a dimming backdrop. One reusable overlay is built up front and reused for
// every call, so any page can `<script src="/modal.js"></script>` and then use:
//
//   await pjAlert('Saved.')                         -> resolves when dismissed
//   if(await pjConfirm('Delete this?')) ...          -> resolves true / false
//   const v = await pjPrompt('Name:', 'default')     -> resolves string, or null if cancelled
//
// Each accepts an options object as the last argument: {title, okLabel, cancelLabel,
// danger, placeholder}. Messages keep their line breaks (white-space:pre-wrap), so the
// existing multi-line strings (with \n\n) carry over unchanged. Standard library only —
// no framework, no third-party code.
(function () {
  var css = `
  #pjmodal{position:fixed;inset:0;background:rgba(0,0,0,.6);display:none;
    align-items:center;justify-content:center;z-index:7000}
  #pjmodal.show{display:flex}
  #pjmodal .pjm-box{background:#23262c;border:1px solid #3a3e45;border-radius:8px;
    padding:20px;max-width:380px;width:calc(100vw - 40px);text-align:center;color:#e8e8e8;
    font-family:system-ui,Segoe UI,sans-serif;box-shadow:0 10px 40px rgba(0,0,0,.55)}
  #pjmodal h3{margin:0 0 8px;font-size:16px}
  #pjmodal h3:empty{display:none}
  #pjmodal .pjm-msg{margin:4px 0 14px;color:#c9ccd1;font-size:13.5px;line-height:1.5;
    white-space:pre-wrap;word-break:break-word}
  #pjmodal .pjm-input{display:none;width:100%;box-sizing:border-box;font-size:14px;
    padding:9px 11px;margin:0 0 14px;background:#2a2d33;color:#eee;border:1px solid #444;
    border-radius:5px;outline:none}
  #pjmodal .pjm-input:focus{border-color:#2d6cdf}
  #pjmodal.prompt .pjm-input{display:block}
  #pjmodal .pjm-btns{display:flex;gap:10px;justify-content:flex-end}
  #pjmodal button{font-size:14px;padding:8px 16px;background:#2a2d33;color:#eee;
    border:1px solid #444;border-radius:5px;cursor:pointer}
  #pjmodal button:hover{background:#363a42}
  #pjmodal .pjm-ok{border-color:#2d6cdf;background:#274a8a}
  #pjmodal .pjm-ok:hover{background:#2f5aa8}
  #pjmodal.danger .pjm-ok{border-color:#7a2b2b;background:#7a2b2b}
  #pjmodal.danger .pjm-ok:hover{background:#943434}
  #pjmodal .pjm-cancel{display:none}
  #pjmodal.has-cancel .pjm-cancel{display:inline-block}`;

  function build() {
    var style = document.createElement('style');
    style.textContent = css;
    document.head.appendChild(style);

    var overlay = document.createElement('div');
    overlay.id = 'pjmodal';
    overlay.innerHTML =
      '<div class="pjm-box" role="dialog" aria-modal="true">' +
        '<h3 class="pjm-title"></h3>' +
        '<div class="pjm-msg"></div>' +
        '<input class="pjm-input" type="text">' +
        '<div class="pjm-btns">' +
          '<button type="button" class="pjm-cancel"></button>' +
          '<button type="button" class="pjm-ok"></button>' +
        '</div>' +
      '</div>';
    document.body.appendChild(overlay);

    var titleEl = overlay.querySelector('.pjm-title');
    var msgEl = overlay.querySelector('.pjm-msg');
    var inputEl = overlay.querySelector('.pjm-input');
    var okBtn = overlay.querySelector('.pjm-ok');
    var cancelBtn = overlay.querySelector('.pjm-cancel');

    var active = null; // { mode, resolve }

    function finish(result) {
      if (!active) return;
      var done = active.resolve;
      active = null;
      overlay.classList.remove('show');
      done(result);
    }

    function open(mode, message, opts) {
      opts = opts || {};
      // A dialog already up: resolve it as cancelled before showing the next one.
      if (active) finish(mode === 'prompt' ? null : mode === 'confirm' ? false : undefined);
      return new Promise(function (resolve) {
        active = { mode: mode, resolve: resolve };
        titleEl.textContent = opts.title || '';
        msgEl.textContent = message == null ? '' : String(message);
        overlay.classList.toggle('prompt', mode === 'prompt');
        overlay.classList.toggle('has-cancel', mode !== 'alert');
        overlay.classList.toggle('danger', !!opts.danger);
        okBtn.textContent = opts.okLabel || (mode === 'confirm' ? 'OK' : mode === 'prompt' ? 'OK' : 'OK');
        cancelBtn.textContent = opts.cancelLabel || 'Cancel';
        if (mode === 'prompt') {
          inputEl.value = opts.value != null ? opts.value : '';
          inputEl.placeholder = opts.placeholder || '';
        }
        overlay.classList.add('show');
        // Focus the natural action: the input for prompts, otherwise the OK button.
        setTimeout(function () {
          if (mode === 'prompt') { inputEl.focus(); inputEl.select(); }
          else okBtn.focus();
        }, 0);
      });
    }

    okBtn.addEventListener('click', function () {
      if (!active) return;
      if (active.mode === 'prompt') finish(inputEl.value);
      else if (active.mode === 'confirm') finish(true);
      else finish(undefined);
    });
    cancelBtn.addEventListener('click', function () {
      finish(active && active.mode === 'prompt' ? null : false);
    });
    overlay.addEventListener('click', function (e) {
      // Click on the dimmed backdrop (not the card) dismisses as cancel.
      if (e.target === overlay) finish(active && active.mode === 'prompt' ? null : active && active.mode === 'confirm' ? false : undefined);
    });
    inputEl.addEventListener('keydown', function (e) {
      if (e.key === 'Enter') { e.preventDefault(); okBtn.click(); }
    });
    document.addEventListener('keydown', function (e) {
      if (!active) return;
      if (e.key === 'Escape') { e.preventDefault(); finish(active.mode === 'prompt' ? null : active.mode === 'confirm' ? false : undefined); }
      else if (e.key === 'Enter' && active.mode !== 'prompt') { e.preventDefault(); okBtn.click(); }
    });

    window.pjAlert = function (message, opts) { return open('alert', message, opts); };
    window.pjConfirm = function (message, opts) { return open('confirm', message, opts); };
    window.pjPrompt = function (message, value, opts) {
      opts = opts || {};
      if (value != null) opts.value = value;
      return open('prompt', message, opts);
    };
  }

  if (document.body) build();
  else document.addEventListener('DOMContentLoaded', build);
})();
