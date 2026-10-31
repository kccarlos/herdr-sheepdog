// The conversation the panel knows about: items with server ids, from
// /api/conversation pages, kept sorted by id with each id once. It ports the
// paging half of ConversationHistory (ios/Sources/SheepdogKit); live events
// are not folded here: the store asks the daemon for the newest page instead,
// so the daemon's own merging rules (internal/web/conversation.go) stay the
// only ones. Pure: every function returns a new history. No herdr imports.

// How many items are kept in memory; older pages past this are dropped from
// the top again (the phone's `memoryCapacity`).
export const MEMORY_CAPACITY = 2000;

export const emptyHistory = () => ({ items: [], nextBefore: null, synced: false });

// Each id once (the last copy wins), oldest first.
function normalise(items) {
  const byId = new Map();
  for (const item of items) byId.set(item.id, item);
  return [...byId.values()].sort((a, b) => a.id - b.id);
}

// The newest page (no `before`). The server is the record: every item in the
// range the page covers is replaced by the server's, and a local item in that
// range the server does not have is dropped. Items newer than the page (they
// arrived while it was in flight) are kept, and so are older ones (earlier
// pages) unless the server said it has nothing older. `requestedAt` (event-id
// microseconds) bounds an empty page: with no items to say where the server's
// record ends, it ends when the request was sent. Unlike the phone's, the
// "load older" cursor stays where earlier pages left it when older items are
// kept, so a refresh never sends the panel back over pages it already holds.
export function applyNewest(history, page, requestedAt = 0) {
  const items = page.items ?? [];
  const serverIds = new Set(items.map((i) => i.id));
  const lower = page.next_before == null ? -Infinity : (items[0]?.id ?? -Infinity);
  const upper = items.length ? items[items.length - 1].id : requestedAt;
  const kept = history.items.filter((i) => !serverIds.has(i.id) && !(i.id >= lower && i.id <= upper));
  const holdsOlder = kept.some((i) => i.id < (items[0]?.id ?? requestedAt));
  const nextBefore = holdsOlder && page.next_before != null ? history.nextBefore : (page.next_before ?? null);
  return trimFront({ items: normalise([...kept, ...items]), nextBefore, synced: true });
}

// An older page (`before` set): merged by id, the server's copy wins.
export function applyOlder(history, page) {
  let items = normalise([...history.items, ...(page.items ?? [])]);
  // Paging back far enough is a deliberate act; keep the old end and let the
  // newest page bring the rest back.
  if (items.length > MEMORY_CAPACITY) items = items.slice(0, MEMORY_CAPACITY);
  return { items, nextBefore: page.next_before ?? null, synced: true };
}

function trimFront(history) {
  if (history.items.length <= MEMORY_CAPACITY) return history;
  const items = history.items.slice(history.items.length - MEMORY_CAPACITY);
  return { ...history, items, nextBefore: items[0].id };
}

export const newestId = (history) => history.items[history.items.length - 1]?.id ?? 0;
export const canLoadOlder = (history) => history.nextBefore != null;
