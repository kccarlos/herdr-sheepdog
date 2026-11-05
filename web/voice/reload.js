// Build-change reload for /voice: tracks the daemon version reported by
// /api/state and /ws bot-ready, and reloads the page once the daemon has
// restarted onto a different build and the page is idle (not in a live audio
// session, no approval tap in flight, not mid-typing, no modal card open).
// Any unsent typed utterance draft is preserved across the reload in
// sessionStorage with a 60 s TTL. Pure logic for node --test.

export const TYPING_SETTLE_MS = 3000;
export const DRAFT_TTL_MS = 60_000;
export const RELOAD_STORAGE_KEY = 'sheepdog.voice.reload';

export function mayReload({ userDisconnected, approvalsBusy, sinceTypedMs, dialogOpen }) {
  return Boolean(
    userDisconnected &&
    !approvalsBusy &&
    sinceTypedMs >= TYPING_SETTLE_MS &&
    !dialogOpen,
  );
}

export function saveReloadDraft(storage, draft, now = Date.now) {
  const text = typeof draft === 'string' ? draft : '';
  if (!text || !storage) return;
  try {
    storage.setItem(RELOAD_STORAGE_KEY, JSON.stringify({ draft: text, at: now() }));
  } catch (_) {
    /* private window: reload still picks up the new build */
  }
}

export function takeReloadDraft(storage, now = Date.now) {
  if (!storage) return '';
  try {
    const raw = storage.getItem(RELOAD_STORAGE_KEY);
    storage.removeItem(RELOAD_STORAGE_KEY);
    if (!raw) return '';
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed.draft !== 'string' || typeof parsed.at !== 'number') return '';
    if (now() - parsed.at > DRAFT_TTL_MS) return '';
    return parsed.draft;
  } catch (_) {
    return '';
  }
}

export function createBuildReloader({
  getIdleState,
  getDraft = () => '',
  storage,
  reload,
  now = Date.now,
  onPendingChange = () => {},
}) {
  let bootVersion = '';
  let pendingVersion = '';
  let reloaded = false;

  function doReload() {
    reloaded = true;
    saveReloadDraft(storage, getDraft(), now);
    reload();
  }

  function check() {
    if (!pendingVersion || reloaded) return false;
    if (!mayReload(getIdleState())) return false;
    doReload();
    return true;
  }

  function noteVersion(version) {
    const v = typeof version === 'string' ? version.trim() : '';
    if (!v) return false;
    if (!bootVersion) {
      bootVersion = v;
      return false;
    }
    if (v === bootVersion) return false;
    if (pendingVersion !== v) {
      pendingVersion = v;
      onPendingChange(v);
    }
    check();
    return true;
  }

  return {
    noteVersion,
    check,
    reloadNow: doReload,
    pending: () => pendingVersion,
  };
}
