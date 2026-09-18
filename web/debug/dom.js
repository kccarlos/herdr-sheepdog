// DOM helpers shared by the /debug views (conversation-view.js,
// swimlane-view.js, supervisor-view.js): element creation, the time
// format of every card, the raw-JSON block every item expands to, the
// open/closed bookkeeping behind "Expand all / Collapse all", and the
// follow-scroll behind the "new messages ↓" pill. Vanilla DOM, no
// framework (decision D11).

/** Creates an element with an optional class and text. */
export function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

/** HH:MM:SS of an ISO timestamp (the raw string when unparsable). */
export function fmtTime(ts) {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return String(ts || '');
  const p = (n) => String(n).padStart(2, '0');
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

/** The raw JSON of `obj` as a <pre> (the collapsible every item ends with). */
export function jsonPre(obj, cls = 'detail') {
  return el('pre', cls, JSON.stringify(obj, null, 2));
}

/**
 * Openable tracks which collapsible keys are open: an expand-all flag
 * plus the set of keys toggled away from it, so "Expand all" and
 * "Collapse all" reset every individual toggle. `header()` builds the
 * clickable summary line with its chevron.
 */
export class Openable {
  constructor(onChange) {
    this.expandAll = false;
    this.toggled = new Set();
    this.onChange = onChange || (() => {});
  }

  isOpen(key) {
    return this.expandAll !== this.toggled.has(key);
  }

  toggle(key) {
    if (this.toggled.has(key)) this.toggled.delete(key); else this.toggled.add(key);
    this.onChange();
  }

  /** Sets `key` open or closed without a re-render (a native <details> already changed). */
  set(key, open) {
    if (this.isOpen(key) === !!open) return;
    if (this.toggled.has(key)) this.toggled.delete(key); else this.toggled.add(key);
  }

  setExpandAll(on) {
    this.expandAll = on;
    this.toggled.clear();
    this.onChange();
  }

  /**
   * A clickable summary line for `key`: chevron, then whatever `build`
   * appends. Clicks on links, buttons and inputs inside it do not toggle.
   * Returns {h, open}.
   */
  header(key, cls, build) {
    const open = this.isOpen(key);
    const h = el('div', `row-head ${cls}${open ? ' open' : ''}`);
    h.appendChild(el('span', 'chev', open ? '▾' : '▸'));
    build(h);
    h.addEventListener('click', (e) => {
      if (e.target.closest('a, button, input, select')) return;
      this.toggle(key);
    });
    return { h, open };
  }
}

/**
 * Follow keeps a scrolling container pinned to its bottom while the
 * viewer is there, and reports through `onNew(true)` when new content
 * arrived while scrolled up (the "new messages ↓" pill); `onNew(false)`
 * clears it. Call begin() before a rebuild and end() after it.
 */
export class Follow {
  constructor(root) {
    this.root = root;
    this.rendered = 0;
    this.onNew = () => {};
    root.addEventListener('scroll', () => {
      if (this.atBottom()) this.onNew(false);
    });
  }

  atBottom() {
    const r = this.root;
    return r.scrollTop + r.clientHeight >= r.scrollHeight - 40;
  }

  scrollToBottom() {
    this.root.scrollTop = this.root.scrollHeight;
    this.onNew(false);
  }

  /** Before a rebuild: whether the view was pinned (or empty). */
  begin() {
    return this.atBottom() || this.rendered === 0;
  }

  /** After a rebuild of `count` items: re-pin or show the pill. */
  end(wasAtBottom, count) {
    const grew = count > this.rendered;
    this.rendered = count;
    if (wasAtBottom) this.root.scrollTop = this.root.scrollHeight;
    else if (grew) this.onNew(true);
    if (this.atBottom()) this.onNew(false);
  }
}
