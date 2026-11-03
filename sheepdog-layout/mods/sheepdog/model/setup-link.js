// The daemon's setup page, opened in the user's browser: the same page
// `sheepdog setup` and `sheepdog open setup` open. The daemon treats this
// computer's loopback address as the local device, so the URL carries no
// token. No herdr imports: `exec` is passed in.

export const SETUP_PATH = "/setup";

export function setupUrl(base) {
  return String(base).replace(/\/+$/, "") + SETUP_PATH;
}

// The program that opens a URL in the default browser, per herdr's
// `window().platform` ("macos", "linux", "windows"); anything else is Linux.
export function openCommand(platform, url) {
  if (platform === "macos") return ["open", [url]];
  if (platform === "windows") return ["cmd", ["/c", "start", "", url]];
  return ["xdg-open", [url]];
}

// Opens the setup page. Resolves to { ok: true } or { ok: false, error }, the
// error saying where to go by hand; it never rejects.
export async function openSetup({ exec, platform, base }) {
  const url = setupUrl(base);
  const failed = { ok: false, error: `Could not open a browser. Open ${url} yourself, or run: sheepdog open setup` };
  try {
    const [command, args] = openCommand(platform, url);
    const res = await exec(command, args);
    return res && res.code !== 0 ? failed : { ok: true };
  } catch {
    return failed;
  }
}
