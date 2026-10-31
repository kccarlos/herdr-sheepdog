// The composer's rules, apart from the textarea that feeds them (a herdr
// import, so main.js owns it). What a `submit` event sends, what a failed send
// puts back, and the Idempotency-Key a send carries (docs/dev/design/
// desktop-client.md, sections 6.2 and 6.3). Pure, so node tests it. No herdr
// imports.

// What to send for a textarea's `submit` event, or null for nothing. A
// textarea inserts the Enter's newline and also fires `submit`: with shift
// that newline is wanted (null, nothing is sent), without it the value minus
// ONE trailing newline is the text. Only whitespace never sends. Typing with
// an input method fires `change` on a commit, never `submit`, so the composer
// listens to `submit` alone.
export function submission(value, { shift = false } = {}) {
  if (shift) return null;
  const text = value.replace(/\r?\n$/, "");
  return text.trim() === "" ? null : text;
}

// What the textarea holds after a send failed: the text that was in it, ahead
// of anything typed since.
export const restored = (text, current) => (current ? `${text}\n${current}` : text);

// Idempotency-Key makers: 1 to 128 visible ASCII characters (internal/web/
// idempotency.go). `random` is Math.random; the clock and a counter keep two
// keys of one run apart even if it repeats.
export function keyMaker({ now = Date.now, random = Math.random } = {}) {
  let count = 0;
  return () => `panel-${now().toString(36)}-${(count++).toString(36)}-${Math.floor(random() * 0x7fffffff).toString(36)}`;
}
