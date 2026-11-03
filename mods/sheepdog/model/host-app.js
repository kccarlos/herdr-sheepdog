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

// herdr's `exec` runs a program with the PATH of the app itself, and an app
// opened from the Dock has a short one that lacks these folders. Where the
// installer and Homebrew put the program, in this order.
export const sheepdogPaths = (home) => [
  `${String(home).replace(/\/+$/, "")}/.local/bin/sheepdog`,
  "/opt/homebrew/bin/sheepdog",
  "/usr/local/bin/sheepdog",
];

// Does a file or folder exist? true, false, or null when it cannot be told.
// herdr's `fs/promises` reads only under the home folder and rejects anything
// else ("outside every granted read root": /Applications, /opt/homebrew), so
// a rejection falls back to `test -e`, which says by its exit code: 0 yes, 1 no.
// Read-only, and never rejects.
export async function pathExists(path, { exists, exec }) {
  try {
    return Boolean(await exists(path));
  } catch {
    // outside the home folder, or unreadable: ask the system
  }
  if (!exec) return null;
  try {
    const res = await exec("test", ["-e", path]);
    return res?.code === 0 ? true : res?.code === 1 ? false : null;
  } catch {
    return null;
  }
}

// The first of those that exists, or null. Never rejects.
export async function findSheepdog({ exists, exec, home }) {
  for (const path of sheepdogPaths(home)) {
    if ((await pathExists(path, { exists, exec })) === true) return path;
  }
  return null;
}

// The program for a terminal tab (`terminal.open` argv), where nothing can be
// retried: a known install place, else the bare name for the shell's PATH.
export async function sheepdogProgram(places) {
  return (await findSheepdog(places)) ?? START_COMMAND[0];
}

const NOT_RUN = { ok: false, text: "Could not run sheepdog. Is it installed? Run setup to check." };

// The note after `sheepdog start`: it resolves to { ok, text }. Never rejects.
// `exec` is herdr's: it rejects when the program cannot start (not on its
// PATH), and then the known install places are tried, using `places` ({ exists,
// home }) when given.
export async function startDaemon(exec, places = null) {
  const [command, args] = START_COMMAND;
  let res;
  try {
    res = await exec(command, args);
  } catch {
    const path = places ? await findSheepdog({ ...places, exec }) : null;
    if (!path) return NOT_RUN;
    try {
      res = await exec(path, args);
    } catch {
      return NOT_RUN;
    }
  }
  if (res && res.code !== 0) return { ok: false, text: "Sheepdog did not start. Run setup, or run sheepdog doctor in a terminal." };
  return { ok: true, text: "Starting Sheepdog…" };
}
