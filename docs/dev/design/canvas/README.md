# Design canvas export (read-only)

This folder is a read-only export of the sheepdog design canvas, the
Claude Design artifact "Sheepdog iOS — UI refresh"
(https://claude.ai/artifact/1EmnSHAtmBAxcnkENsjwMP). Do not edit these
files by hand: visual design changes only in the artifact, and this folder
is re-exported after every change.

- `canvas.json` is the canvas index: every board's frame, title, page and
  section notes. The canvas has two pages: **UI** (the iPhone and iPad app,
  with Part 3, the factory) and **Engine** (the boards of the new factory
  engine in [../../engine-v2/](../../engine-v2/design.md)).
- Each `*.dc.html` file is one board's full source. Boards whose names
  start with `Factory` are the factory's UI; boards whose names start with
  `Engine` are the engine's rule tables.
- Boards import shared components by name (`TopBar`, `AgentCard`,
  `AgentSection`, `Dock`, `ChatRow`) and rely on the canvas runtime
  (`support.js`), so they do not render as plain HTML. Read the source,
  or open the artifact.
- `png/` holds a PNG of a board, exported from the canvas (1×), and
  `boards.json` maps each board id to its title, source file and PNG.
  Workers check the UI against these pictures. A board whose `png` is
  `null` changed after the last PNG export; open the artifact for it until
  the PNGs are exported again.
- Images referenced as `/_blob/…` live in the artifact and are not
  exported.
- **Part 4b** (slice 14 of [../../engine-v2/plan.md](../../engine-v2/plan.md),
  PR 14.0) is the factory page's whole life cycle: eleven `FactoryWeb…`
  boards (`LifeHome`, `NewProject`, `StartChecks`, `Pairing`,
  `ProjectActions`, `Settings`, `ReadBack`, `Models`, `TaskLife`, `Log`,
  `Graph`), each with a `…Dark` and a `…360` copy.
  [slice14-wording.md](slice14-wording.md) is their wording table, with the
  tab order, the screen-reader labels and the open questions. Their PNGs
  were rendered from the board sources in headless Chrome, not exported
  from the canvas; the logo in them is the repository's.
