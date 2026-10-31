// The one colour the theme does not give the panel: "needs you" is the
// phone's amber, not the theme's `blocked` (red in most themes). The phone's
// accent is #D98718 on light and #F39C1F on dark; the panel tells which it is
// from the sidebar's own colour, since `theme()` has no light flag. No herdr
// imports: node tests it.

export const AMBER_LIGHT = "#D98718";
export const AMBER_DARK = "#F39C1F";

// Whether a "#rrggbb" (or "#rrggbbaa") colour is light, by its relative
// luminance. Anything else reads as dark, the common case for a dev tool.
export function isLight(color) {
  const m = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})(?:[0-9a-f]{2})?$/i.exec(String(color ?? "").trim());
  if (!m) return false;
  const [r, g, b] = [m[1], m[2], m[3]].map((h) => parseInt(h, 16) / 255);
  return 0.2126 * r + 0.7152 * g + 0.0722 * b > 0.5;
}

// The amber for a theme whose sidebar colour is `chrome`.
export const amber = (chrome) => (isLight(chrome) ? AMBER_LIGHT : AMBER_DARK);
