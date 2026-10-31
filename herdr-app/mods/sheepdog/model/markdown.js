// The markdown an orchestrator card carries (`markdown` of a `card` item's
// card, docs/dev/ws-contract.md "Cards"), split into blocks the way the phone
// does: paragraphs, bullet and numbered lists, fenced code, GitHub-style
// tables, headings as bold text. It ports MarkdownRenderer.blocks
// (ios/Sources/SheepdogKit). Inline styles are not drawn: `plainInline` keeps
// the words and drops the markers. Pure, so node tests it. No herdr imports.

const trimmed = (s) => s.trim();

// `| a | b |` to ["a", "b"]; `\|` is a pipe inside a cell.
export function cells(row) {
  let line = trimmed(row);
  if (line.startsWith("|")) line = line.slice(1);
  if (line.endsWith("|") && !line.endsWith("\\|")) line = line.slice(0, -1);
  const out = [];
  let cell = "";
  let escaped = false;
  for (const ch of line) {
    if (escaped) {
      if (ch !== "|") cell += "\\";
      cell += ch;
      escaped = false;
    } else if (ch === "\\") {
      escaped = true;
    } else if (ch === "|") {
      out.push(trimmed(cell));
      cell = "";
    } else {
      cell += ch;
    }
  }
  if (escaped) cell += "\\";
  out.push(trimmed(cell));
  return out;
}

// `|---|:---:|--:|` to the columns' alignments; null if it is not one.
export function delimiterRow(line) {
  const t = trimmed(line);
  if (!t.includes("-") || !(t.includes("|") || /^[-: ]*$/.test(t))) return null;
  const out = [];
  for (const cell of cells(t)) {
    if (!cell || !/^[-:]+$/.test(cell) || !cell.includes("-")) return null;
    const lead = cell.startsWith(":");
    const trail = cell.endsWith(":");
    out.push(lead && trail ? "center" : trail ? "trailing" : "leading");
  }
  return out.length ? out : null;
}

function heading(t) {
  const hashes = /^#*/.exec(t)[0].length;
  if (hashes < 1 || hashes > 6 || t[hashes] !== " ") return null;
  const text = trimmed(trimmed(t.slice(hashes)).replace(/#+$/, ""));
  return text || null;
}

function listItem(line) {
  const indent = /^ */.exec(line)[0].length;
  const body = line.slice(indent);
  const depth = Math.floor(indent / 2);
  if ("-*+".includes(body[0] ?? "x") && body[1] === " ") {
    const text = trimmed(body.slice(2));
    if (!text) return null;
    if (text.startsWith("[ ] ")) return { marker: "☐", depth, text: text.slice(4) };
    if (text.toLowerCase().startsWith("[x] ")) return { marker: "☑", depth, text: text.slice(4) };
    return { marker: "•", depth, text };
  }
  const digits = /^\d*/.exec(body)[0];
  if (!digits || digits.length > 4) return null;
  const after = body.slice(digits.length);
  if (!(after[0] === "." || after[0] === ")") || after[1] !== " ") return null;
  const text = trimmed(after.slice(2));
  return text ? { marker: `${digits}.`, depth, text } : null;
}

// The blocks of `text`: { type: "paragraph" | "heading" | "code", text },
// { type: "list", items: [{ marker, depth, text }] } and
// { type: "table", header, alignments, rows }. Lines stay as written inside a
// paragraph (a worker's line breaks are usually meant).
export function blocks(text) {
  const out = [];
  let paragraph = [];
  let list = [];
  let code = null;
  const flushParagraph = () => {
    if (paragraph.length) out.push({ type: "paragraph", text: paragraph.join("\n") });
    paragraph = [];
  };
  const flushList = () => {
    if (list.length) out.push({ type: "list", items: list });
    list = [];
  };
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  let index = 0;
  while (index < lines.length) {
    const line = lines[index++];
    const t = trimmed(line);
    const fence = t.startsWith("```") || t.startsWith("~~~");
    if (code) {
      if (fence) {
        out.push({ type: "code", text: code.join("\n") });
        code = null;
      } else {
        code.push(line);
      }
      continue;
    }
    if (fence) {
      flushParagraph();
      flushList();
      code = [];
      continue;
    }
    if (!t) {
      flushParagraph();
      flushList();
      continue;
    }
    // A header row followed by a delimiter row starts a table.
    const alignments = t.includes("|") && index < lines.length ? delimiterRow(lines[index]) : null;
    if (alignments && cells(t).length === alignments.length) {
      const header = cells(t);
      flushParagraph();
      flushList();
      index++;
      const rows = [];
      while (index < lines.length) {
        const row = trimmed(lines[index]);
        if (!row || !row.includes("|")) break;
        const c = cells(row);
        while (c.length < header.length) c.push("");
        rows.push(c.slice(0, header.length));
        index++;
      }
      out.push({ type: "table", header, alignments, rows });
      continue;
    }
    const h = heading(t);
    if (h) {
      flushParagraph();
      flushList();
      out.push({ type: "heading", text: h });
      continue;
    }
    const item = listItem(line);
    if (item) {
      flushParagraph();
      list.push(item);
      continue;
    }
    if (list.length && line.startsWith("  ")) {
      // A continuation line of the last item.
      list[list.length - 1].text += ` ${t}`;
      continue;
    }
    flushList();
    paragraph.push(t);
  }
  if (code) out.push({ type: "code", text: code.join("\n") });
  flushParagraph();
  flushList();
  return out;
}

// The words without bold, italic, strikethrough, `code` and link markers.
export function plainInline(text) {
  return text
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/(\*\*|__)(.+?)\1/g, "$2")
    .replace(/~~(.+?)~~/g, "$1")
    .replace(/(^|[^*\w])\*(?!\s)(.+?)\*(?!\w)/g, "$1$2")
    .replace(/`([^`]*)`/g, "$1");
}
