// The voice controls of the panel (docs/dev/design/desktop-client.md, sections
// 2, 4 and 6.4): which of Talk, Mute, Stop and hold-to-talk work for a state of
// /api/state, what each one sends, and the controller that sends it. The
// daemon relays mute, stop and push-to-talk to whoever holds the voice (this
// Mac's Sheepdog.app or the phone), so the panel never asks who it is; the one
// command it cannot relay is "Use here", which runs `open sheepdog://talk`
// (LaunchServices starts Sheepdog.app, which then takes the voice). The
// controller asks the daemon through `http` and runs the program through
// `exec`, both passed in, and tells subscribers when something changed; it
// never draws. No herdr imports: node tests it.

import { INSTALL_COMMAND, OPEN_COMMAND, pathExists, RUNNING_COMMAND, runningFrom } from "./host-app.js";
export { INSTALL_COMMAND };
export const TALK_URL = "sheepdog://talk";

// Where Sheepdog.app is looked for, read-only: the system's and the user's.
export const appPaths = (home) => ["/Applications/Sheepdog.app", home + "/Applications/Sheepdog.app"];

// How long a note stays, how often the install is looked for again, and the
// longest a hold-to-talk stays open without a release (a release outside the
// button is never seen, so the mic is not left open).
export const NOTICE_MS = 8000;
export const CHECK_MS = 30000;
// How often "is Sheepdog.app running" is looked at, only while it matters.
export const RUN_CHECK_MS = 3000;
export const PTT_MAX_MS = 30000;

export const NO_VOICE = "No voice is connected";
const MISSING = "Sheepdog.app is not installed, so there is no voice on this Mac.";
const MISSING_HERE = "Sheepdog.app is not installed, so this Mac cannot take the voice.";

const OFF = { enabled: false };

// What the buttons do for a state. `phase` and `view` are model/status.js's
// (`view` is `status.voice`), `voice` the daemon's raw `voice`, `installed`
// true, false or null (not looked yet: taken as installed), `noVoice` a 409
// just seen. Each button is { enabled }; `talk.action` is what Talk does:
// "use-here" (nobody holds the voice), "unmute", or null (already listening).
// `useHere` says whether the separate Use here button shows; `install` is the
// command to show when the app is missing.
export function voiceControls({ phase, voice, view, installed = null, noVoice = false, platform = null }) {
  const off = { talk: { ...OFF, action: null }, mute: OFF, stop: OFF, ptt: OFF, useHere: false, useHereEnabled: false, hint: "", install: null, action: null };
  if (phase !== "ok") return off;
  if (voice?.mode === "off") return { ...off, hint: "The voice is off." };
  // Sheepdog.app and `open sheepdog://talk` are the Mac's: elsewhere there is
  // no app to be missing and nothing for Talk or Use here to start.
  const here = platform == null || platform === "macos";
  const missing = here && installed === false;
  const held = view.holder != null || Boolean(voice?.client_connected);
  if (!held) {
    // Nobody holds the voice: only the Mac's own app can start it.
    // `view.action` is the one step for this Mac (model/host-app.js).
    if (!here) return { ...off, hint: view.note };
    const action = view.action ?? null;
    if (missing) return { ...off, hint: MISSING, install: INSTALL_COMMAND, action };
    return { ...off, talk: { enabled: true, action: "use-here" }, useHere: noVoice, useHereEnabled: true, hint: action ? view.note : "", action };
  }
  const muted = Boolean(voice?.muted);
  const elsewhere = view.elsewhere;
  return {
    talk: { enabled: muted, action: muted ? "unmute" : null },
    mute: { enabled: !muted },
    stop: { enabled: Boolean(voice?.speaking) },
    ptt: { enabled: true },
    useHere: elsewhere || noVoice,
    useHereEnabled: here && !missing,
    hint: elsewhere && missing ? MISSING_HERE : "",
    install: elsewhere && missing ? INSTALL_COMMAND : null,
  };
}

// What an action sends: an `exec` of a program, or an http call `call` with
// `args`. The actions are the buttons': use-here, unmute, mute, stop,
// ptt-down and ptt-up. Anything else is not a command.
export function commandFor(action) {
  switch (action) {
    case "use-here": return { exec: ["open", [TALK_URL]] };
    case "open-app": return { exec: OPEN_COMMAND };
    case "open-setup": return { setup: true };
    case "unmute": return { call: "mute", args: [false] };
    case "mute": return { call: "mute", args: [true] };
    case "stop": return { call: "stop", args: [] };
    case "ptt-down": return { call: "ptt", args: [true] };
    case "ptt-up": return { call: "ptt", args: [false] };
    default: return null;
  }
}

// What a failed command tells the user.
function failure(err, action) {
  if (action === "open-setup") return { tone: "error", text: err.message || "Could not open the setup page.", code: "setup" };
  if (action === "use-here" || action === "open-app") return { tone: "error", text: "Sheepdog.app did not start. Install it with: " + INSTALL_COMMAND, code: "exec" };
  if (err.status === 409) return { tone: "error", text: NO_VOICE, code: "no-voice" };
  return { tone: "error", text: err.status ? err.message : "Sheepdog did not answer. Try again.", code: "http" };
}

export function createVoice({ http, exec, exists, home, newKey, openSetup = async () => ({ ok: true }), now = Date.now, refresh = async () => {}, platform = () => null }) {
  let snapshot = {
    running: null, // a process named Sheepdog exists; null until looked (model/host-app.js)
    installed: null, // Sheepdog.app is in /Applications or ~/Applications; null until looked
    busy: null, // the action on its way (mute, stop and Use here go one at a time)
    ptt: false, // the hold-to-talk button is held
    pttAt: 0,
    notice: null, // { tone: "error" | "info", text, code, at }, under the buttons
  };
  let chain = Promise.resolve(); // hold-to-talk's on and off go in order
  let lastCheck = -Infinity;
  let lastRun = -Infinity;
  let looking = false;
  let checking = false;
  const listeners = new Set();

  const set = (patch) => {
    snapshot = { ...snapshot, ...patch };
    for (const listener of listeners) listener(snapshot);
  };

  // Sends one action: mute, stop and Use here one at a time; a tap while the
  // first is on its way is dropped.
  async function send(action) {
    const command = commandFor(action);
    if (!command || snapshot.busy) return;
    set({ busy: action, notice: null });
    try {
      if (command.setup) {
        const res = await openSetup();
        if (!res.ok) throw new Error(res.error);
      } else if (command.exec) {
        const res = await exec(...command.exec);
        if (res.code !== 0) throw new Error(res.stderr);
      } else {
        await http[command.call](...command.args, newKey());
      }
      const starts = action === "use-here" || action === "open-app";
      if (starts) lastRun = -Infinity; // look again at once: the app is on its way up
      set({ busy: null, notice: starts ? { tone: "info", text: "Starting Sheepdog on this Mac…", code: "starting", at: now() } : null });
    } catch (err) {
      set({ busy: null, notice: { ...failure(err, action), at: now() } });
    }
    // A mute, a stop and a new holder all show in the state.
    await refresh();
  }

  // The hold-to-talk button: down opens the mic, up closes it. In order, so a
  // quick press never sends "off" ahead of "on".
  const hold = (on) => {
    chain = chain.then(async () => {
      try {
        await http.ptt(on, newKey());
      } catch (err) {
        set({ ptt: false, notice: { ...failure(err, "ptt"), at: now() } });
      }
    });
    return chain;
  };
  function pttDown() {
    if (snapshot.ptt) return Promise.resolve();
    set({ ptt: true, pttAt: now(), notice: null });
    return hold(true);
  }
  function pttUp() {
    if (!snapshot.ptt) return Promise.resolve();
    set({ ptt: false });
    return hold(false);
  }

  // Looks for Sheepdog.app (read-only), at most every CHECK_MS. A look that
  // fails leaves the answer as it was.
  async function check() {
    // Sheepdog.app is the Mac's: elsewhere "not installed" would be a false report.
    const os = platform();
    if (os != null && os !== "macos") return;
    if (checking || now() - lastCheck < CHECK_MS) return;
    checking = true;
    lastCheck = now();
    try {
      const found = await Promise.all(appPaths(home()).map((path) => pathExists(path, { exists, exec })));
      // One place with the app is enough; with none and a place that could not
      // be told, it stays unknown rather than "not installed".
      const installed = found.includes(true) ? true : found.includes(null) ? null : false;
      if (installed != null && installed !== snapshot.installed) set({ installed });
    } catch {
      // Unknown stays unknown: the buttons are not turned off on a guess.
    } finally {
      checking = false;
    }
  }

  // Is Sheepdog.app running? An exact-name look, at most every RUN_CHECK_MS.
  // A look that fails leaves the answer as it was.
  async function checkRunning() {
    if (looking || now() - lastRun < RUN_CHECK_MS) return;
    looking = true;
    lastRun = now();
    try {
      const running = runningFrom(await exec(...RUNNING_COMMAND));
      if (running != null && running !== snapshot.running) set({ running });
    } catch {
      // pgrep missing: unknown stays unknown.
    } finally {
      looking = false;
    }
  }

  // Drops a note that has stayed long enough, closes a hold nobody released,
  // and looks for the app again when it is time.
  // `watchRunning`: the panel is showing the state where it matters (macOS,
  // daemon up, nobody holds the voice), so the app is looked for often.
  function prune(watchRunning = false) {
    const at = now();
    if (snapshot.notice && at - snapshot.notice.at >= NOTICE_MS) set({ notice: null });
    if (snapshot.ptt && at - snapshot.pttAt >= PTT_MAX_MS) pttUp();
    check();
    if (watchRunning) checkRunning();
  }

  return {
    getState: () => snapshot,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    send,
    pttDown,
    pttUp,
    prune,
    check,
  };
}
