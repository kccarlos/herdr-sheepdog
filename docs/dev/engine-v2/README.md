# Factory engine v2 (design)

The design of the new factory engine, written in Go inside sheepdog. The
old Python engine is used only as a knowledge base and is not in this
repository's history from here on.

- [design.md](design.md): the engine. Records tied to commits, one ordered
  first-match rule table per task, transition matrices for runs and asks,
  and the decisions D1 to D16 in section 0.
- [plan.md](plan.md): how it is built, as thin vertical slices. Slice 1 is
  the walking skeleton.
- [prompts.md](prompts.md): the coordinator, worker and reviewer prompts,
  designed with the engine.
- [knowledge-base.md](knowledge-base.md): every behaviour of the old engine
  worth keeping, with where the new design covers it.
- [critique.md](critique.md): what went wrong in the old engine.
- [review-1.md](review-1.md), [review-2.md](review-2.md): the two
  independent reviews; their answers are in design.md sections 17.6 and 17.7.
- [transitions.html](transitions.html): the transition pictures. The
  drawn boards are on the Engine page of the design canvas
  ([../design/canvas/](../design/canvas/README.md)).
- [notes/](notes/table-audit.md): working notes the documents cite. The
  histories in `notes/table-audit.md` become the slices' first failing tests.
