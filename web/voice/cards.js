// Cards on /voice (docs/dev/ws-contract.md "Cards"): the page reports
// whether it can be seen (visibility) and shows the orchestrator's cards.
// Pure helpers, no DOM, so node --test covers them; app.js does the DOM.
import { esc, inline, renderMarkdown } from '../static/markdown.js';
export { inline, renderMarkdown };

/** The visibility report: this page is the voice screen, so it is in
 * front whenever the tab is not hidden. */
export function visibilityMessage(hidden) {
  return { type: 'visibility', visible: !hidden, voice_screen: true };
}

/** Whether the daemon serves cards (bot-ready lists the feature). */
export function cardsEnabled(features) {
  return Array.isArray(features) && features.includes('cards');
}

/** The card's body as HTML, by kind. An approval card is read-only: its
 * options are listed, never buttons. */
export function cardBodyHTML(card) {
  const name = esc(card.spoken || card.agent || '');
  switch (card.kind) {
    case 'card':
      return renderMarkdown(card.markdown);
    case 'excerpt':
      return `<pre class="code excerpt"><code>${esc(card.text || '')}</code></pre>`;
    case 'approval': {
      let h = `<p>${esc(card.question || '')}</p>`;
      if (card.command) h += `<pre class="code"><code>${esc(card.command)}</code></pre>`;
      if (card.options && card.options.length) h += '<p class="muted small">Options: ' + card.options.map((o) => esc(o.label)).join(' · ') + '</p>';
      return h + '<p class="muted small">Answer it in the question card on this page, or by voice.</p>';
    }
    case 'agent':
      return `<p>${name}'s live window: open it in the Sheepdog app or in herdr.</p>`;
    default:
      return card.markdown ? renderMarkdown(card.markdown) : '';
  }
}

/** The short label of a card's transcript row. */
export function cardLabel(card) {
  return card.title || 'Card';
}
