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

// The timeline keeps the majority of a short panel. The rest is shared by the
// header, the top stack and the composer; the top stack scrolls when its
// natural height does not fit.
export const MIN_TIMELINE_SHARE = 0.55;
export const COMPACT_BANNER_HEIGHT = 40;
export const COMPACT_QUESTION_HEIGHT = 44;

// "rail" or "panel" for a block of `width` px. An overlay is always the panel.
export const layoutMode = ({ width, overlay }) => (!overlay && width < COMPACT_BELOW ? "rail" : "panel");

// Whether the panel offers to be docked: it is a window, and not the one the
// docked rail opened (`docked` is the prop that opening sets).
export const needsDock = ({ overlay, docked }) => Boolean(overlay) && !docked;

const px = (value) => (Number.isFinite(Number(value)) ? Math.max(0, Number(value)) : 0);

// The vertical budget above the timeline. Each banner gets a full or one-line
// height, the question area feeds model/presentation.js's compact rule, and
// the top details scroll if even their compact form does not fit. The
// composer is outside that scroll, so it stays visible.
export function panelBudget({
  height,
  header = 0,
  banners = [],
  voice = 0,
  question = 0,
  composer = 0,
  minTimelineShare = MIN_TIMELINE_SHARE,
} = {}) {
  const total = px(height);
  const timelineMinHeight = Math.ceil(total * Math.min(1, Math.max(0, minTimelineShare)));
  const detailsLimit = Math.max(0, total - timelineMinHeight - px(header) - px(composer));
  const fullBanners = banners.map(px);
  const naturalBanners = fullBanners.reduce((sum, value) => sum + value, 0);
  const fullDetails = naturalBanners + px(voice) + px(question);
  const compactBanners = fullBanners.some((value) => value > COMPACT_BANNER_HEIGHT) && fullDetails > detailsLimit;
  const shownBanners = compactBanners ? fullBanners.map((value) => Math.min(value, COMPACT_BANNER_HEIGHT)) : fullBanners;
  const shownDetails = shownBanners.reduce((sum, value) => sum + value, 0) + px(voice) + px(question);
  return {
    compactBanners,
    detailsLimit,
    detailsHeight: Math.min(detailsLimit, shownDetails),
    detailsScroll: shownDetails > detailsLimit,
    questionArea: Math.max(0, detailsLimit - shownBanners.reduce((sum, value) => sum + value, 0) - px(voice)),
    timelineMinHeight,
  };
}
