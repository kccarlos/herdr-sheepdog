# sheepdog brand language

*The one document every surface (GitHub, docs, iOS app, web pages) follows.*

## 1. The idea

sheepdog is a border collie wearing a headset. It sits between you and your flock of coding agents, listens to them, and talks to you. Everything in the brand comes from that one picture:

- **Attentive, not busy.** The dog is calm and looks straight at you. The UI does the same: quiet surfaces, one accent, nothing blinking unless an agent needs you.
- **Warm, not corporate.** Cream and charcoal, rounded shapes, a sticker outline. Not slate-blue dashboards, not neon "AI" gradients.
- **The headset is the accent.** Orange appears where the product listens or needs your word. It is not decoration.

Tagline (keep verbatim): **Manage your Herdr agents by voice.**

Wordmark: **sheepdog**, lowercase in prose and code; **SHEEPDOG** in all caps only in the banner/hero lockup. Never "Sheepdog for herdr" in UI chrome; "for herdr" is a subtitle, used once per surface at most.

## 2. Palette

| Token | Hex | Role |
|---|---|---|
| ink | `#1F1F22` | fur; text on light; dark surfaces are a cooler cousin (`#16181C` bg, `#1F2126` card) |
| cream | `#F5F2EA` | blaze; text on dark; light bg is a warmer cousin (`#FAF8F3`) |
| orange | `#F39C1F` | **the accent.** Buttons, the listening orb, focus rings, links on dark |
| orange-deep | `#D98718` | pressed state; iOS AccentColor in light mode |
| orange-text | `#8A5700` light / `#F5B83D` dark | orange *as words* (AA on both surfaces) |
| slate | `#8E929A` | muted text, dividers, "unknown" |

Rules:
- Text on an orange fill is **ink, never white.**
- One accent per screen. If two things are orange, one of them is wrong.
- Pure `#000`/`#FFF` never appear as surfaces; use ink/cream and their cousins.
- Agent-state colours (blocked amber, working blue, done green, idle grey, thinking purple) are **unchanged in meaning** and now identical on web and iOS. Blocked is amber on both; the web's red-for-blocked is retired. Amber ≈ accent on purpose: "blocked" means "needs you", which is what the accent means.
- Full values, light + dark, in [`web/static/tokens.css`](../../web/static/tokens.css) and [`assets/brand/colors.json`](../../assets/brand/colors.json). Those files are the source of truth, not this table.

## 3. Type

- UI: **Inter** on the web (self-hosted or system fallback), **SF Pro** on iOS. Body 16px / 1.5.
- Display (H1, hero, wordmark): a geometric rounded sans — **Outfit** on the web, `.fontDesign(.rounded)` on iOS. Titles only; never body text.
- Mono: system monospace. Code and transcripts keep their mono; don't theme them orange.
- Weights: 400 body, 500 labels, 600 headings, 700 only for the wordmark.

## 4. Shape and motion

- Radii: 8 / 14 / 22 px, pill for chips and the mic button. Cards use 14.
- The **sticker outline** (3px cream stroke outside a shape, on dark) is the signature detail. Use it on: the logo, the hero mascot, the listening orb when active, and nothing else.
- Motion: one easing (`cubic-bezier(.2,.8,.2,1)`, 200ms). The orb breathes (scale 1→1.04, 2s) only while listening. No bouncing, no confetti, no spinners where a dot will do.

## 5. The mascot

Use the mascot sparingly and consistently:

| Asset | Use for |
|---|---|
| `assets/brand/logo/sheepdog-logo-color.svg` | README header (dark mode), app About screen, web page headers, social avatar |
| `assets/brand/logo/sheepdog-logo-mono-ink.svg` / `-cream.svg` / `-currentcolor.svg` | favicon, docs sidebar, badges, SF-symbol-style template image, anywhere ≤ 32px or single-colour |
| `assets/brand/icon/sheepdog-icon.svg` + `assets/brand/icon/AppIcon-1024-dark.png` | iOS app icon (dark background is the canonical one). Also the GitHub repo social preview's left half |
| `assets/brand/illustrations/hero-banner-dark.jpeg` | README hero / repo social preview (1280×640 crop) / docs landing. Replace the current launch-poster only if the banner carries the same CTA |
| `assets/brand/illustrations/mascot-states-sheet.jpg` | reference for the four moods: listening, alert, sleeping, celebrating. Use *one* state per empty/status screen (iOS Flock view empty state = sleeping; "all agents done" = celebrating; "sheepdog is listening" = listening; "agent blocked" = alert). Crop single poses from the sheet; do not redraw them |

Never: recolour the dog, flip the headset to the right side, put text over the face, use a different dog, add a gradient.

## 6. Voice (copy)

Same voice as the current README, which is already good: short sentences, second person, no hype words. Headings lowercase. "it" for sheepdog, "your agents" for the flock. Keep the existing `→` link convention. Each feature bullet = bold hook + one plain sentence + link.

## 7. Assets

```
assets/brand/logo/           color SVG + PNG, mono (ink / cream / currentColor) SVG + PNG, transparent sticker PNG
assets/brand/icon/           tight-crop SVG, 1024 PNG on dark (canonical), orange and gray alternates
assets/brand/illustrations/  hero banner (dark, carries the tagline), mascot states sheet
assets/brand/source/         two raster sources the art was drawn from
assets/brand/preview/        logo on dark/cream, icon under the iOS corner mask
assets/brand/colors.json     colour source for iOS and anything else
web/static/tokens.css        colour source for the web pages (a test keeps both files in step)
```

Web favicons live in `web/static/` (`favicon.svg`, `favicon-32.png`, `apple-touch-icon.png`), and the pages use `web/static/logo-color.svg` and `logo-mono.svg`, copies of the brand logos.
