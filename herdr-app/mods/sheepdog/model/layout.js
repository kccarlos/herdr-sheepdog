// How the one block draws itself: the full panel, or the compact rail
// (docs/dev/design/desktop-client.md, section 6.1). Docked in the sidebar the
// block IS the panel; only a sidebar too narrow for it shows the rail, and the
// window overlay is the same panel (it is also what opens when the mod is not
// docked). Pure, so node tests it. No herdr imports.

// A block narrower than this draws the compact rail, like the shipped
// sidebars' compact column.
export const COMPACT_BELOW = 160;

// The width a block is taken to have until the first size report.
export const DOCKED_WIDTH = 340;

// "rail" or "panel" for a block of `width` px. An overlay is always the panel.
export const layoutMode = ({ width, overlay }) => (!overlay && width < COMPACT_BELOW ? "rail" : "panel");

// Whether the panel offers to be docked: it is a window, and not the one the
// docked rail opened (`docked` is the prop that opening sets).
export const needsDock = ({ overlay, docked }) => Boolean(overlay) && !docked;
