// Pure keyboard shortcut mapping for /voice. Tested under node via
// shortcuts.test.mjs; imported by app.js.

import { approvalDigitOption } from './approvals.js';

export function voiceKeyAction(
  event,
  {
    inField = false,
    cardOpen = false,
    shortcutsOpen = false,
    authOpen = false,
    canStop = false,
    canMute = false,
    cards = [],
  } = {},
) {
  const modified = Boolean(event.metaKey || event.ctrlKey || event.altKey);
  if (authOpen || cardOpen) return null;
  if (shortcutsOpen) {
    if (!modified && (event.key === '?' || event.key === 'Escape')) return { type: 'close-shortcuts' };
    return null;
  }
  if (event.key === 'Escape') {
    return canStop ? { type: 'stop' } : null;
  }
  const pick = approvalDigitOption(cards, event.key, {
    inField,
    metaKey: Boolean(event.metaKey),
    ctrlKey: Boolean(event.ctrlKey),
    altKey: Boolean(event.altKey),
    dialogOpen: false,
  });
  if (pick) {
    return { type: 'answer-approval', question: pick.question, option: pick.option, preventDefault: true };
  }
  if (inField || modified) return null;
  if (event.key === '?') return { type: 'open-shortcuts' };
  if (event.key === ' ') return { type: canStop ? 'stop' : 'ignore', preventDefault: true };
  if (event.key === 'm' || event.key === 'M') return canMute ? { type: 'toggle-mute' } : null;
  return null;
}
