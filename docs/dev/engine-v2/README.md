# Factory engine v2

The Go factory engine inside sheepdog (`internal/factory`). Version 1
(slices 1–8), `agy`, O32 (no quick-reply on factory ask pushes), slice 9
(`/factory` web board), slice 11 (plan review, #800, #803, #807), slice 12
(the retro round, #780, #784, #794), and slice 14 (the factory's whole life
on `/factory`, #801–#819) are merged on `main`; slice 10 (engine-run
evidence) is parked; the next step is a docs pass then `v0.1.31`, followed
by slice 13 (coordinator rotation and journal). The old Python
engine is used only as a historical knowledge base and is not in this
repository's history on `main`.

- [design.md](design.md): the engine. Records tied to commits, one ordered
  first-match rule table per task, transition matrices for runs and asks,
  and the decisions D1 to D17 in section 0.
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
