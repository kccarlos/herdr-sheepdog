// Cards on /voice (docs/dev/ws-contract.md "Cards"): the page reports
// whether it can be seen (visibility) and shows the orchestrator's cards.
// Pure helpers, no DOM, so node --test covers them; app.js does the DOM.

/** The visibility report: this page is the voice screen, so it is in
 * front whenever the tab is not hidden. */
export function visibilityMessage(hidden) {
  return { type: 'visibility', visible: !hidden, voice_screen: true };
}

/** Whether the daemon serves cards (bot-ready lists the feature). */
export function cardsEnabled(features) {
  return Array.isArray(features) && features.includes('cards');
}

const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

/** Inline markdown: code, bold, italic and links (http/https only), on
 * escaped text. */
export function inline(text) {
  const parts = String(text).split(/(`[^`]+`)/);
  return parts.map((p) => {
    if (p.length > 1 && p.startsWith('`') && p.endsWith('`')) return `<code>${esc(p.slice(1, -1))}</code>`;
    return esc(p)
      .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
      .replace(/(^|[^*])\*([^*\s][^*]*)\*/g, '$1<em>$2</em>')
      .replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
  }).join('');
}

const cells = (line) => line.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map((c) => c.trim());
const isRule = (line) => /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(line);

/** A small, safe markdown renderer (everything is escaped first):
 * headings, paragraphs, bullet and numbered lists, tables, code fences
 * and quotes. Returns HTML. */
export function renderMarkdown(md) {
  const lines = String(md || '').replace(/\r\n/g, '\n').split('\n');
  const out = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (/^\s*```/.test(line)) {
      const lang = line.trim().slice(3).trim();
      const body = [];
      i++;
      while (i < lines.length && !/^\s*```/.test(lines[i])) body.push(lines[i++]);
      i++;
      out.push(`<pre class="code${lang ? ' lang-' + esc(lang) : ''}"><code>${esc(body.join('\n'))}</code></pre>`);
      continue;
    }
    if (line.includes('|') && i + 1 < lines.length && isRule(lines[i + 1])) {
      const head = cells(line);
      i += 2;
      const rows = [];
      while (i < lines.length && lines[i].includes('|') && lines[i].trim() !== '') rows.push(cells(lines[i++]));
      out.push('<div class="table-wrap"><table><thead><tr>' + head.map((h) => `<th>${inline(h)}</th>`).join('') +
        '</tr></thead><tbody>' + rows.map((r) => '<tr>' + r.map((c) => `<td>${inline(c)}</td>`).join('') + '</tr>').join('') +
        '</tbody></table></div>');
      continue;
    }
    let m = /^(#{1,4})\s+(.*)$/.exec(line);
    if (m) {
      out.push(`<h${m[1].length + 2}>${inline(m[2])}</h${m[1].length + 2}>`);
      i++;
      continue;
    }
    if (/^\s*([-*+]|\d+[.)])\s+/.test(line)) {
      const ordered = /^\s*\d/.test(line);
      const items = [];
      while (i < lines.length && /^\s*([-*+]|\d+[.)])\s+/.test(lines[i])) {
        items.push(lines[i].replace(/^\s*([-*+]|\d+[.)])\s+/, ''));
        i++;
      }
      const tag = ordered ? 'ol' : 'ul';
      out.push(`<${tag}>` + items.map((t) => `<li>${inline(t)}</li>`).join('') + `</${tag}>`);
      continue;
    }
    if (/^\s*>/.test(line)) {
      const q = [];
      while (i < lines.length && /^\s*>/.test(lines[i])) q.push(lines[i++].replace(/^\s*>\s?/, ''));
      out.push(`<blockquote>${inline(q.join(' '))}</blockquote>`);
      continue;
    }
    if (line.trim() === '') {
      i++;
      continue;
    }
    const para = [];
    while (i < lines.length && lines[i].trim() !== '' && !/^\s*(```|#{1,4}\s|[-*+]\s|\d+[.)]\s|>)/.test(lines[i]) &&
      !(lines[i].includes('|') && i + 1 < lines.length && isRule(lines[i + 1]))) para.push(lines[i++]);
    out.push(`<p>${inline(para.join(' '))}</p>`);
  }
  return out.join('\n');
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
      return h + '<p class="muted small">Answer it by voice, or in the app.</p>';
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
