# Design canvas export (read-only)

This folder is a read-only export of the sheepdog design canvas, the
Claude Design artifact "Sheepdog iOS — UI refresh". Do not edit these
files by hand: visual design changes only in the artifact, and this folder
is re-exported after every change.

- `canvas.json` is the canvas index: every board's frame, title and
  section notes.
- Each `*.dc.html` file is one board's full source. Boards whose names
  start with `Factory` are Part 3, the factory design (draft, under
  independent review).
- Boards import shared components by name (`TopBar`, `AgentCard`,
  `AgentSection`, `Dock`, `ChatRow`) and rely on the canvas runtime
  (`support.js`), so they do not render as plain HTML. Read the source,
  or open the artifact.
- Images referenced as `/_blob/…` live in the artifact and are not
  exported. PNGs of the boards are not in this export yet.
