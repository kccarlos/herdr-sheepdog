// Echo-cancellation check for /voice (docs/ws-contract.md `ping` `aec`).
// Pure, so node can test it without a browser.

/** The hint the page shows when the browser's echo cancellation is off. */
export const AEC_HINT = "Use headphones or the app: this browser's echo cancellation is off";

/**
 * isMobile reports whether the user agent is a phone or tablet browser:
 * Android, iPhone/iPad/iPod, or iPadOS's desktop-looking Safari
 * ("Macintosh" with a touch screen).
 * @param {string} ua navigator.userAgent
 * @param {number} [touchPoints] navigator.maxTouchPoints
 */
export function isMobile(ua, touchPoints = 0) {
  if (/Android|iPhone|iPad|iPod|Mobile/i.test(ua || '')) return true;
  return /Macintosh/.test(ua || '') && touchPoints > 1;
}

/**
 * aecStatus reads a microphone track's settings: 'true' or 'false' when
 * the browser reports `echoCancellation`, 'unknown' when it does not
 * (older Safari reports nothing).
 * @param {MediaTrackSettings|undefined|null} settings track.getSettings()
 * @returns {'true'|'false'|'unknown'}
 */
export function aecStatus(settings) {
  const v = settings ? settings.echoCancellation : undefined;
  if (v === true) return 'true';
  if (v === false) return 'false';
  return 'unknown'; // absent, or a string ("all"/"remote-only") — not a plain yes
}

/**
 * aecWarn decides whether to show AEC_HINT: always when echo cancellation
 * is off (the model hears itself and interrupts itself), and when it is
 * unknown on a mobile browser, where it has not been verified
 * (docs/research/voice-frameworks-2026-09-24.md P2). Desktop browsers
 * that do not say are left alone: desktop Chrome cancels echo.
 * @param {'true'|'false'|'unknown'} aec
 * @param {boolean} mobile
 */
export function aecWarn(aec, mobile) {
  return aec === 'false' || (aec === 'unknown' && mobile);
}
