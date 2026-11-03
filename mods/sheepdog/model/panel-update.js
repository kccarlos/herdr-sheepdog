// The notice that the panel on this computer is older than the daemon's. The
// daemon replaces an unchanged panel by itself after an upgrade (herdr-app
// reloads it), so this shows only when it must not or may not: the panel was
// changed, was not installed by `sheepdog setup`, or panel.auto_update is
// off. `panel` is /api/state's `panel` (docs/dev/ws-contract.md). Pure; no
// herdr imports.

export const PANEL_UPDATE_COMMAND = "sheepdog setup";

// { title, text, command } or null.
export function panelUpdateNotice(panel) {
  if (!panel || !panel.installed || !panel.update_available) return null;
  if (!panel.modified && panel.auto_update !== false) return null;
  const why = panel.modified
    ? "This panel was changed on this computer or not installed by sheepdog, so it is left as it is."
    : "Updating the panel by itself is turned off (panel.auto_update).";
  return {
    title: "A newer panel is available",
    text: `${why} Run ${PANEL_UPDATE_COMMAND} to update it.`,
    command: PANEL_UPDATE_COMMAND,
  };
}
