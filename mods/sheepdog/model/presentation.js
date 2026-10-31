// How the open questions float above the composer: the cards themselves
// ("inline"), or one bar, "Codex asked · Answer", that opens them over the
// timeline ("compact"). It ports QuestionPresentation.decide
// (ios/Sources/SheepdogKit; docs/dev/design/conversation-timeline.md, "When a
// question goes compact"). The phone measures each card with SwiftUI; the mod
// runtime has no measuring call a render may use (a size is only reported to a
// handler), so `estimateHeight` guesses a card's height from its words and the
// window's width, and the budget is the share of the window's height. The rest
// is the phone's rule: a budget share with hysteresis, and a decision held
// while the user is mid-answer. The phone's largest-text rule has no
// counterpart: the panel has no text-size setting. Pure. No herdr imports.

// The share of the window the inline cards may take, and the share of that
// budget they must fit in to come back from compact (hysteresis: a size near
// the line does not flip the panel back and forth).
export const BUDGET_SHARE = 0.55;
export const RETURN_SHARE = 0.85;
// The card shows this many lines of the question, then "More" on the phone;
// the estimate counts at most this many (the panel shows the whole question
// in the sheet).
export const QUESTION_LINES = 3;
// The gap between two stacked cards.
export const SPACING = 8;

// What the cards (12.5 px text, 14 px padding) cost, in px.
const LINE = 17;
const SMALL_LINE = 15;
const CHAR = 6.8;
const CARD_CHROME = 14 * 2 + 18 + 10; // padding, the header line, the gap under it
const OPTION_CHROME = 10 * 2 + 18 + 6; // padding, the label line, the gap after the row

const lines = (text, perLine) => Math.max(1, Math.ceil(Array.from(text.trim()).length / perLine));

// A card's height at `width`, inline: the header, the question cut to
// QUESTION_LINES, the command (three lines at most) and every option with its
// description.
export function estimateHeight(question, { width }) {
  const perLine = Math.max(12, Math.floor((width - 28 - 20) / CHAR));
  const text = question.summary || question.spoken_summary || question.question || "";
  let h = CARD_CHROME + Math.min(QUESTION_LINES, lines(text, perLine)) * LINE;
  if (question.command?.trim()) h += Math.min(3, lines(question.command, perLine)) * 16 + 14 + 10;
  for (const option of question.options ?? []) {
    h += OPTION_CHROME;
    if (option.description?.trim()) h += lines(option.description, perLine) * SMALL_LINE;
  }
  return h;
}

// The stacked cards' natural height.
export const natural = (heights, spacing = SPACING) => heights.reduce((a, b) => a + b, 0) + spacing * Math.max(0, heights.length - 1);

// The presentation for `heights` (each card's estimate) in `area` (the window's
// height), given the one on screen (null before the first decision). `held`:
// the user is in the middle of it (the sheet is open, an answer is in flight):
// nothing changes under them. Not measured yet (no area): what is on screen,
// inline at first.
export function decide({ heights, spacing = SPACING, area, held = false }, current) {
  if (heights.length === 0) return current ?? "inline";
  if (held && current) return current;
  if (!(area > 0) || heights.some((h) => !(h > 0))) return current ?? "inline";
  let limit = area * BUDGET_SHARE;
  if (current === "compact") limit *= RETURN_SHARE;
  return natural(heights, spacing) <= limit ? "inline" : "compact";
}
