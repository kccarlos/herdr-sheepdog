// What the panel says when the daemon is up but nobody holds the voice: on a
// Mac that is almost always Sheepdog.app (the menu-bar client) not being open,
// so the words depend on whether it is installed and running. Pure; the
// controller in model/voice.js looks the two facts up (read-only: `exists`
// and `pgrep -x Sheepdog`, an exact name, never a pattern). No herdr imports.

export const APP_NAME = "Sheepdog";
export const APP_PROCESS = "Sheepdog";
// The cask is in the project's tap (docs/desktop.md).
export const INSTALL_COMMAND = "brew install --cask kccarlos/tap/sheepdog-app";

// The look: pgrep -x exits 0 when a process has exactly that name, 1 when none
// does; anything else is "could not tell" (null).
export const RUNNING_COMMAND = ["pgrep", ["-x", APP_PROCESS]];
export const runningFrom = (res) => (res?.code === 0 ? true : res?.code === 1 ? false : null);

// Opens the app by its name (LaunchServices finds it in either Applications).
export const OPEN_COMMAND = ["open", ["-a", APP_NAME]];

// The view for "daemon up, no voice client". `platform` is herdr's
// `window().platform` ("macos", "linux", "windows"; undefined when unknown);
// `installed` and `running` are true, false or null (not looked yet).
// `action` is { id, label } of the button, `install` a command to show,
// `noHolder` marks the view for the caller.
export function noHolderView(host = {}) {
  return { ...pick(host), noHolder: true };
}

function pick({ platform, installed = null, running = null }) {
  const generic = { label: "Ready", note: "Open Sheepdog on your phone or computer to talk.", tone: "dim", action: null, install: null };
  if (platform == null) return generic;
  if (platform !== "macos") return { ...generic, note: "Open Sheepdog on your phone to talk." };
  if (running === true) {
    return { label: "Connecting to Sheepdog.app", note: "Sheepdog.app is open and should connect in a moment. If not, check its menu-bar icon.", tone: "working", action: null, install: null };
  }
  if (installed === false) {
    return { label: "Sheepdog.app isn't installed", note: "Install it to talk from this Mac, or use your phone.", tone: "dim", action: { id: "open-setup", label: "Open Setup" }, install: INSTALL_COMMAND };
  }
  if (installed === true && running === false) {
    return { label: "Sheepdog.app isn't running", note: "Open it to talk from this Mac, or use your phone.", tone: "dim", action: { id: "open-app", label: "Open Sheepdog.app" }, install: null };
  }
  return { ...generic, note: "Open Sheepdog.app on this Mac or your phone to talk." };
}

// What the "down" panel offers when the daemon does not answer. Start runs
// `sheepdog start`; Run setup opens `sheepdog setup` in a tab.
export const DOWN_ACTIONS = [
  { id: "start", label: "Start Sheepdog" },
  { id: "setup", label: "Run setup" },
];
export const START_COMMAND = ["sheepdog", ["start"]];

// The note after `sheepdog start`: it resolves to { ok, text }. Never rejects.
export async function startDaemon(exec) {
  try {
    const res = await exec(...START_COMMAND);
    if (res && res.code !== 0) return { ok: false, text: "Sheepdog did not start. Run setup, or run sheepdog doctor in a terminal." };
    return { ok: true, text: "Starting Sheepdog…" };
  } catch {
    return { ok: false, text: "Could not run sheepdog. Is it installed? Run setup to check." };
  }
}
