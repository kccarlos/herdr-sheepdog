// Pure helper for deriving a short single-line title from a project goal.
export function goalTitle(goal, fallback = '') {
  const first = String(goal ?? '').split(/\r?\n/).map((l) => l.trim()).find(Boolean);
  if (!first) return fallback;
  const clean = first.replace(/^#{1,6}(?:\s+|$)/, '').replace(/\*\*/g, '').trim();
  if (!clean) return fallback;
  const chars = [...clean];
  if (chars.length <= 120) return clean;
  let cut = 120;
  for (let i = 120; i > 0; i--) {
    if (/\s/.test(chars[i])) {
      cut = i;
      break;
    }
  }
  return `${chars.slice(0, cut).join('').trimEnd()}…`;
}
