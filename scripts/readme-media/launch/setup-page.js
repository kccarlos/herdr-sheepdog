// Runs in the setup page before its own scripts (cdprec -init-js). install-gif.sh
// replaces __REAL_KEY__ and __QR_SVG__ (both throwaway: the key from the
// environment, written to a 0600 file that is deleted after the take).
//
//  - The page shows a placeholder key; the real key goes to the daemon in
//    its place, so the validation and the save are real and the key never
//    reaches a frame.
//  - Any address or throwaway path the page shows becomes the example
//    address 192.168.1.20 (port 7777) or "~"; the pairing QR is a dummy
//    one for that address and a made-up token.
//  - __demo() is the take: type the key, save, checks, phone, talk.
(() => {
  const SHOWN = 'AIzaSy-paste-your-key-here-0000';
  const REAL = '__REAL_KEY__';
  const QR = 'data:image/svg+xml;base64,__QR_SVG__';
  const realFetch = window.fetch.bind(window);
  window.fetch = (url, opts) => {
    if (opts && typeof opts.body === 'string' && opts.body.includes(SHOWN)) {
      opts = Object.assign({}, opts, { body: opts.body.split(SHOWN).join(REAL) });
    }
    return realFetch(url, opts);
  };

  const ip = /\b(?:\d{1,3}\.){3}\d{1,3}\b(?::\d+)?/g;
  const clean = (t) => t
    .replace(ip, (m) => (m.startsWith('127.0.0.1') ? m : '192.168.1.20:7777'))
    .replace(/\/(?:private\/)?(?:tmp|var\/folders)\/[^\s,;)]*/g, '~');
  const sweep = () => {
    const w = document.createTreeWalker(document.body || document.documentElement, NodeFilter.SHOW_TEXT);
    for (let n = w.nextNode(); n; n = w.nextNode()) {
      const c = clean(n.nodeValue);
      if (c !== n.nodeValue) n.nodeValue = c;
    }
    const qr = document.getElementById('qr');
    if (qr && qr.getAttribute('src') && qr.getAttribute('src') !== QR) qr.setAttribute('src', QR);
  };
  new MutationObserver(sweep).observe(document, { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ['src'] });

  const $ = (id) => document.getElementById(id);
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const until = async (f, ms = 30000) => { const e = Date.now() + ms; while (Date.now() < e && !f()) await sleep(100); };
  const show = (id) => { window.scrollTo(0, $(id).getBoundingClientRect().top + window.scrollY - 14); };

  window.__demo = async () => {
    $('service-on').checked = false; // never offered here: no login service in a recording
    await sleep(700);
    const input = $('gemini-key');
    input.focus();
    for (const ch of SHOWN) {
      input.value += ch;
      input.dispatchEvent(new Event('input', { bubbles: true }));
      await sleep(45);
    }
    await until(() => /Valid/.test($('gemini-result').textContent));
    await sleep(1000);
    $('save-models').click();
    await until(() => /^Saved/.test($('models-msg').textContent) && !$('save-models').disabled);
    await until(() => /good|fix/.test($('checks-state').textContent));
    show('step-checks');
    await sleep(1700);
    $('phone-enable').click();
    await until(() => !$('phone-on').classList.contains('hidden') && $('qr').complete && $('qr').naturalWidth > 0);
    show('step-phone');
    await sleep(2400);
    show('step-talk');
    await sleep(1800);
    window.__done = true;
  };
})();
