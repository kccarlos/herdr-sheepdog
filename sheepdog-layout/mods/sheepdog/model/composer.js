// The composer's rules, apart from the textarea that feeds them (a herdr
// import, so main.js owns it). What a `submit` event sends, what a failed send
// puts back, how unsent drafts persist across mod reloads, and the
// Idempotency-Key a send carries (docs/dev/design/desktop-client.md, sections
// 6.2 and 6.3). Pure, so node tests it. No herdr imports.

export const DRAFT_KEY = "sheepdog.draft";
export const DRAFT_TTL_MS = 60 * 60 * 1000;

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

// What the textarea holds after a send is moved back for editing: the text that
// was in it, ahead of anything typed since.
export const restored = (text, current) => (current ? `${text}\n${current}` : text);

// Serializes an unsent composer draft for herdr-app's shared state, or returns
// null when empty/blank so shared state clears cleanly.
export function serializeDraft(text, now = Date.now()) {
  if (typeof text !== "string" || text.trim() === "") return null;
  return { text, at: now };
}

// Restores an unsent composer draft from herdr-app's shared state if fresh.
export function readDraft(raw, now = Date.now()) {
  let obj = raw;
  if (typeof obj === "string") {
    try {
      obj = JSON.parse(obj);
    } catch {
      return "";
    }
  }
  if (!obj || typeof obj !== "object") return "";
  const { text, at } = obj;
  if (typeof text !== "string" || text.trim() === "" || typeof at !== "number") return "";
  const age = now - at;
  if (age >= DRAFT_TTL_MS || age < -DRAFT_TTL_MS) return "";
  return text;
}

// Idempotency-Key makers: 1 to 128 visible ASCII characters (internal/web/
// idempotency.go). `random` is Math.random; the clock and a counter keep two
// keys of one run apart even if it repeats.
export function keyMaker({ now = Date.now, random = Math.random } = {}) {
  let count = 0;
  return () => `panel-${now().toString(36)}-${(count++).toString(36)}-${Math.floor(random() * 0x7fffffff).toString(36)}`;
}
