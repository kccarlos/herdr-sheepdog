# Slice 14 wording table (PR 14.0)

Every label, read-back question, option, refusal and empty-state sentence
that slice 14 writes on `/factory`, with the tab order and the
screen-reader label of each new control. The code PRs 14.1–14.10 copy these
strings; a test's expected string is the **Text** column
([plan.md](../../engine-v2/plan.md), slice 14, UX line 7).

- The boards are the `FactoryWeb…` boards of Part 4b on the design canvas
  ([README.md](README.md), [boards.json](boards.json)). Board and table
  were printed from one list of strings, so they agree.
- Words are the [glossary](../../glossary.md)'s. On screen the owner is
  "you". No rule id and no setting key with underscores appears, except
  `rule N22` inside the task view's details fold.
- **Not in this table:** start check names, details and fixes. The page
  shows them as `startcheck.go` writes them. The check text on the boards
  is sample text.
- A sentence marked "the daemon's sentence" is written by a Go PR; the
  page shows what the daemon sends. Numbers, names and model names in a
  sentence are examples: "review rounds 3 (now 2)" stands for any row.
- A setting's name on screen is its key with spaces for underscores
  (`review_rounds` → "review rounds"). A models row is `<role> models`.
- Headings are lowercase; buttons start with a capital (one exception: the
  header's `needs you: N` button, see `ni.btn`).


## Projects and the way in

| Id | Kind | Text | Where and when |
|---|---|---|---|
| `proj.h` | heading | projects | the projects card (exists) |
| `proj.new` | button | New project | in the projects card, and in the empty page |
| `st.running` | state word | running | projects list and the project bar |
| `st.stopped` | state word | stopped |  |
| `st.finished` | state word | finished |  |
| `empty.h` | heading | no project yet | main column when there is no project (replaces "nothing running") |
| `empty.p` | empty state | A project is one folder with a git repository and one goal. The coordinator plans the tasks, workers build them, reviewers check them, and you are asked only for decisions. |  |
| `proj.none` | empty state | None yet. | projects card with no project (exists) |

## New project form

| Id | Kind | Text | Where and when |
|---|---|---|---|
| `np.h` | heading | new project | h1 of the form |
| `np.folder` | label | folder |  |
| `np.folder.hint` | hint | A folder on this computer that is a git repository with a GitHub remote. |  |
| `np.folder.ph` | placeholder | /path/to/your/repo |  |
| `np.sugg` | label | folders sheepdog knows | above the suggestions |
| `np.sugg.git` | mark | git repository | on a suggestion |
| `np.sugg.notgit` | mark | not a git repository | on a suggestion |
| `np.sugg.none` | empty state | No folders to suggest: herdr cannot be reached. Type the folder's full path. | GET /api/spaces answers 502 |
| `np.notgit` | refusal | This folder is not a git repository. Pick another folder, or make it one. | under the folder field, when the picked suggestion says so; a typed path is judged by the start checks |
| `np.goal` | label | goal |  |
| `np.goal.hint` | hint | What you want built, in your own words. The coordinator turns it into tasks. |  |
| `np.goal.empty` | refusal | Write a goal first. | under the goal field, on start with an empty goal; focus moves to the field |
| `np.models` | legend | models | the group of the four models rows |
| `np.models.hint` | hint | The CLI, model and effort each role uses. In order: the engine uses the first one it can start. A retro reviewer looks at a task whose reviews keep failing. | under the legend, in the form and above the models rows of the panel |
| `np.r.row` | refusal | worker models: a model name cannot hold a comma | the daemon's sentence: check and start answer a refused settings pair as {name, message}; the page shows the message under the row with that name. At a first start the daemon refuses here what Settings.With can judge: the form of a value and whether the engine can launch that CLI for the role (for example codex as retro). Installed and signed in come from the start checks, and a model the account does not offer is found when an agent starts |
| `np.check` | button | Check this folder |  |
| `np.start` | button | Start the project and its agents | the one filled button of the form |
| `np.start.off` | hint | Start turns on when no check fails. | beside the disabled start button |
| `np.quota` | hint | Starting runs agents on your model accounts. They use your quota. | under the buttons |
| `np.starting` | status | Starting… | button text while the start is sent |
| `np.started` | status | Started. The coordinator is planning the tasks. | on the board the page shows after the start |

## Start checks

| Id | Kind | Text | Where and when |
|---|---|---|---|
| `ck.h` | heading | start checks |  |
| `ck.intro` | hint | Every check must pass before a project can start. A warning does not stop it. | before the first run of the checks |
| `ck.running` | status | Checking the folder… |  |
| `ck.pass` | mark | pass | icon: tick |
| `ck.warn` | mark | warning | icon: triangle |
| `ck.fail` | mark | fail | icon: cross |
| `ck.skip` | mark | skipped | icon: dash |
| `ck.fix` | label | Fix: | before a fix that is not a command |
| `ck.fix.cmd` | label | Fix, in a terminal: | before a fix that starts "run:". The block holds the text after "run:" up to the first " (" or ", then "; the rest of the fix is plain text under the block. A page rule (14.5), not a rewording of the checks |
| `ck.copy` | button | Copy |  |
| `ck.copied` | status | Copied | replaces Copy for 2 s; announced |
| `ck.sum.pass` | status | All 7 checks pass. | the number is the count |
| `ck.sum.warn` | status | 6 pass, 1 warning. You can start. |  |
| `ck.sum.fail` | refusal | 2 checks failed. Fix them, then check again. | "1 check failed. Fix it, then check again." for one |
| `ck.models` | hint | Is a failed check for a CLI you do not use? Change the models above, then check again. | under the summary in the new-project form, when a check failed |
| `ck.again` | button | Check again | the Check this folder button, once the checks have run |
| `ck.cant` | error | Can't run the start checks: sheepdog did not answer in time. Check that sheepdog is running, then check again. | the check route fails or times out |
| `ck.sample` | note | Check names, details and fixes are shown as the start checks write them. | not on the page: a note on the boards |

## Pairing at the first write

| Id | Kind | Text | Where and when |
|---|---|---|---|
| `pr.h` | heading | pair this browser | the box appears beside the control that needed it |
| `pr.p` | sentence | Changes from this page need a paired browser, once. On this Mac, open the Sheepdog menu-bar app (its icon is in the menu bar), choose Pair browser…, and type the code it shows. | replaces PAIR_HINT in asks.js (14.2) |
| `pr.kept` | sentence | What you just asked for is kept. It is sent as soon as this browser is paired. |  |
| `pr.code` | label | pairing code |  |
| `pr.code.ph` | placeholder | XXXX-XXXX |  |
| `pr.pair` | button | Pair |  |
| `pr.busy` | status | Pairing… |  |
| `pr.help` | summary | No Sheepdog menu-bar app? | a fold under the form, always there |
| `pr.help.p` | sentence | The code comes from the Sheepdog menu-bar app. It is not installed on this Mac, or it is not open. | inside the fold |
| `pr.help.link` | link | Download the Sheepdog menu-bar app | to the latest release page |
| `pr.help.then` | sentence | Open it, then choose Pair browser… from its icon in the menu bar. |  |
| `pr.empty` | refusal | Enter the pairing code from the Sheepdog menu-bar app. | Pair with an empty field (exists) |
| `pr.wrong` | refusal | That code did not work. It was mistyped, already used, or older than 5 minutes. Choose Pair browser… again for a new code. | the page's sentence for a 410, written in asks.js by 14.2; the daemon's own sentence stays |
| `pr.many` | refusal | Too many attempts from this address. Wait a few minutes and try again. | the daemon's sentence for 429, unchanged |
| `pr.done` | status | Paired. Sending what you asked for… | then the box closes and the action goes on |

## Project actions: stop, resume, new goal, reopen

| Id | Kind | Text | Where and when |
|---|---|---|---|
| `ac.region` | screen-reader text | This project | label of the project bar |
| `ac.stop` | button | Stop | running; finished while an agent is still running |
| `ac.newgoal` | button | New goal | running and stopped |
| `ac.resume` | button | Resume | stopped |
| `ac.reopen` | button | Reopen with a new goal | finished |
| `ac.stopped.p` | sentence | Stopped. No agent is running. Resume goes on with the same goal. |  |
| `ac.fin.p` | sentence | Finished. To go on, reopen it with a new goal. |  |
| `ac.fin.live` | sentence | Finished. An agent is still running. | finished and live |
| `stop.q` | question | Stop this project? | the confirm, in place under the buttons |
| `stop.what` | sentence | Its running agents end now. Resume goes on with the same goal. | running |
| `stop.what.fin` | sentence | Its last running agent ends now. The project stays finished. | finished and live |
| `stop.yes` | button | Stop the project | danger outline |
| `stop.no` | button | Keep it running | focus lands here when the confirm opens |
| `stop.busy` | status | Stopping… |  |
| `ng.h` | label | new goal | the field of the new-goal form |
| `ng.hint` | hint | The folder and the settings stay as they are. The coordinator plans the new goal. |  |
| `ng.hint.fin` | hint | The folder and the settings stay as they are. To change a model first, use settings below. | finished |
| `ng.go.running` | button | Give it this goal | running |
| `ng.go.stopped` | button | Start with this goal | stopped |
| `ng.go.fin` | button | Reopen with this goal | finished |
| `ng.cancel` | button | Cancel |  |
| `ng.same` | refusal | This project is finished. Give it a new goal. | the daemon's sentence (14.4), under the field, when the stored goal is typed again |
| `ac.refused` | refusal | Not started: a start check failed. Fix it, then press Resume again. | under the button that was pressed, and it names that button: "…then press New goal again.", "…then press Reopen again." Every check follows, drawn as in the new-project form, without the form's own summary line |
| `ac.refused.set` | refusal | Not started: a setting is not allowed. Change it in settings below. | a start refused because a stored value is not allowed. The page never shows the key: the settings row carries the range |
| `ac.refused.models` | hint | Is the failed check for a CLI you do not use? Change the models in settings below. | under the failed checks of a refused start |
| `ac.maxrun` | refusal | Change max running in the settings panel on the factory page. | the daemon's sentence (14.4) for a start that carries a different value; reaches the page only through a terminal start, listed so the words are fixed |

## Settings panel

| Id | Kind | Text | Where and when |
|---|---|---|---|
| `se.h` | heading | settings |  |
| `se.sub` | sentence | For this project only. A change is read back to you and saved when you confirm. | under the heading |
| `se.meta` | row text | default 2 · allowed 1–5 | under each value; "default not set" for an empty default |
| `se.change` | button | Change | shown on a row only while its field differs from the stored value; screen-reader label "Change review rounds" |
| `se.default` | button | Back to the default | only on a changed row; screen-reader label "Set review rounds back to the default, 2" |
| `se.chg` | mark | changed | value differs from the default |
| `se.bad` | mark | not allowed | a stored value outside its range |
| `se.unused` | mark | not used |  |
| `se.all` | summary | all settings (38) | the fold under the top rows; the number is the count of rows inside |
| `se.notset` | value word | not set | an empty choice or text value, and "default not set" |
| `se.loading` | status | Loading the settings… |  |
| `se.cant` | error | Can't load the settings. Trying again. | followed by the daemon's sentence when there is one |
| `se.cant.rest` | sentence | The rest of the page still works. | under the cannot-load line |
| `se.bad.p` | refusal | The stored value 0 is not allowed (allowed 1–5). Change it before the next start. | under a row marked not allowed |
| `se.unused.p` | row text | not used while reviewer models are set | the row's note, as the daemon writes it (14.1) |
| `se.r.range` | refusal | review rounds 0: allowed 1–5 | the daemon's sentence (14.1); the field's own min and max stop most of these first |
| `se.r.other` | refusal | review rounds 6 does not fit: max rounds is 5 (allowed 6–10). Raise max rounds first. | the daemon's sentence (14.1) |
| `se.r.same` | refusal | review rounds is already 3 | the daemon's sentence (14.2) |
| `se.r.word` | refusal | review rounds "eleven": allowed 1–5 | a value that is not a number |
| `se.sending` | status | Asking… | button text while the proposal is sent |
| `rb.q` | read-back question | This project: review rounds 3 (now 2). Save? | written by the engine (SE1); in the row and on the card |
| `rb.q.models` | read-back question | This project: worker models agy/gemini-4-neon/medium then claude/sonnet/medium (now claude/sonnet/medium). Save? | a models row; candidates joined by "then" |
| `rb.q.default` | read-back question | This project: review rounds 2 (now 3). Save? | back to the default: the same sentence as any change |
| `rb.save` | option | Save | the recommended option: a bold outline and the tag; screen-reader label "Save (Recommended)", as asks.js writes it today |
| `rb.rec` | tag | Recommended | the tag and its case are the shipped ones; only the look changes |
| `rb.keep` | option | Keep current |  |
| `rb.wait` | row text | Waiting for your answer. The same question is under asks for you, on your phone and by voice. | in the row, above the two buttons |
| `rb.saved` | status | Saved. The engine uses it from now on. | in the row for 5 s; announced |
| `rb.saved.models` | status | Saved. The next agent started for this role uses it. Running agents keep their model. |  |
| `rb.kept` | status | Kept at 2. Nothing changed. | after Keep current |
| `rb.moved` | refusal | Not saved: another setting changed after you asked. | followed by the daemon's refusal for the row; the question stays open |
| `rb.cancelled` | status | Not saved: the project finished before you answered, so this change was cancelled. You can make it again. | the ask was cancelled with the others when the project finished |
| `rb.card.meta` | card text | setting · the engine asks | the card's top line under asks for you |

## Models per role

| Id | Kind | Text | Where and when |
|---|---|---|---|
| `mo.edit` | button | Edit | on a models row; screen-reader label "Edit worker models" |
| `mo.first` | row text | first choice | the first candidate; then "second choice", "third choice" |
| `mo.cli` | label | CLI |  |
| `mo.model` | label | model |  |
| `mo.effort` | label | effort |  |
| `mo.up` | button | Move up | not on the first candidate; screen-reader label "Move agy gemini-4-neon up" |
| `mo.remove` | button | Remove | not when one candidate is left; screen-reader label "Remove claude sonnet" |
| `mo.add` | button | Add a model |  |
| `mo.default` | button | Back to the default | fills the editor with the default list; nothing is sent until Change |
| `mo.change` | button | Change | sends the list; the read-back follows in the row |
| `mo.cancel` | button | Cancel |  |
| `mo.done` | button | Done | in the new-project form, in place of Change: keeps the list for the start; nothing is sent |
| `mo.r.install` | refusal | agy is not installed | launch check at propose (14.3): the start check's own detail for that CLI, followed by its fix drawn as in the start checks card |
| `mo.r.signin` | refusal | codex is not signed in | the start check's own detail, followed by its fix in a command block |
| `mo.r.offer` | refusal | agy does not offer gemini-4-neo. It offers: gemini-4-carbon, gemini-4-neon, gemini-4-hydrogen, gemini-3.8-flash. | the list is the account's |
| `mo.unknown` | hint | sheepdog cannot list the models claude offers. This name is checked when an agent starts. | under the model field when the list is unknown; never a refusal |
| `mo.suggest` | hint | Model names agy offers on this account: gemini-4-carbon · gemini-4-neon · gemini-4-hydrogen · gemini-3.8-flash. The field also takes a typed name. | under the candidates, when the account's list is known; the names are also the field's suggestions |
| `mo.r.retro` | refusal | the engine cannot launch codex as retro yet | the daemon's sentence (14.3); the editor does not offer that CLI for the role |
| `mo.r.cli` | refusal | unknown cli "claud" (allowed claude, codex, agy) | the daemon's sentence; the editor offers only allowed CLIs |
| `mo.r.empty` | refusal | Type a model name. | Change with an empty model field |
| `mo.r.comma` | refusal | worker models: a model name cannot hold a comma | the daemon's sentence, the same in the panel and in the form |

## Task view

| Id | Kind | Text | Where and when |
|---|---|---|---|
| `tk.brief` | heading | brief | the brief card (exists) |
| `tk.details` | summary | details | a fold at the end of the verdict card; holds the rule id, the run and the layer as secondary text |
| `tk.rule` | row text | rule N22 | inside the fold only |
| `tk.steps` | heading | steps | the named steps card in the task view |
| `tk.log` | heading | log for this task | replaces 'recent log' |
| `tk.log.empty` | empty state | Nothing logged for this task yet. |  |

## Step bar

| Id | Kind | Text | Where and when |
|---|---|---|---|
| `sp.planned` | step | planned |  |
| `sp.impl` | step | implementing |  |
| `sp.review` | step | review |  |
| `sp.retro` | step | retro | only when a retro round exists or runs |
| `sp.ci` | step | CI |  |
| `sp.merged` | step | merged |  |
| `sp.planrev` | step | plan review | a real step (11.1 is merged, #800): shown only while `plan_review_rounds` is above 0, read from the row that holds the task (14.8). Where it sits is drawn; its words are slice 11's |
| `sp.done` | state word | done | icon: tick |
| `sp.now` | state word | now | icon: filled dot |
| `sp.todo` | state word | to come | icon: empty ring |
| `sp.skipped` | state word | skipped | icon: dash in a dashed ring |
| `sp.t.review` | step text | review round 2; needs 2 passing, at most 5 | the review step's text, from both settings |
| `sp.t.needs` | step text | needs 2 passing, at most 5 | on review while it is still to come |
| `sp.t.rounds` | step text | 2 rounds done | on review when it is done; "1 round done" for one |
| `sp.t.back` | step text | sent back from review round 1; 1 round done | on implementing, when review asked for changes |
| `sp.t.skip` | step text | skipped by your override | on review |
| `sp.t.override` | step text | Merging now, by your override | on CI; the stage's why line (exists) |
| `sp.t.retro` | step text | a retro reviewer is looking at why review keeps failing | on retro while it runs |
| `sp.end.dropped` | step | dropped | the last step of a task that was dropped, in the step state "ended" (a fifth state 14.8 adds beside done, now, todo, skipped); text "ended without merging" |
| `sp.end.closed` | step | closed | the last step when the PR was closed; text "ended without merging: its PR was closed" |
| `sp.t.ended` | step text | ended without merging |  |
| `sp.t.closed` | step text | ended without merging: its PR was closed |  |
| `sp.parked` | step text | parked here, waiting on the coordinator | on the step where a parked task stopped; the step is in the state "now", with no look of its own |
| `sp.bar.back` | bar text | implementing, sent back from review round 1 | beside the compact bar: the step, then its short reason |
| `sp.bar.skip` | bar text | CI, review skipped by your override |  |
| `sp.bar.dropped` | bar text | dropped, ended without merging |  |
| `sp.aria` | screen-reader text | step 3 of 5, review: review round 2; needs 2 passing, at most 5 | the compact bar's label; it is joined to the row's label |

## Log

| Id | Kind | Text | Where and when |
|---|---|---|---|
| `lg.h` | heading | log |  |
| `lg.task` | label | task | the filter; first option "all tasks" |
| `lg.all` | option | all tasks |  |
| `lg.obs` | label | show routine checks | the checkbox that asks for observations too |
| `lg.merged` | mark | merged | icon: merge |
| `lg.needs` | mark | held up | icon: triangle. The task stopped short and the coordinator decides; the word must not read as a question to you |
| `lg.asked` | mark | asked you | icon: speech bubble |
| `lg.yours` | mark | you | icon: person; the line's 'by' also reads you, not owner |
| `lg.today` | day heading | Today, Tuesday 24 November | the browser's date; "Yesterday, …" and then the weekday and date alone |
| `lg.empty` | empty state | Nothing logged yet. | (exists) |
| `lg.onlyobs` | empty state | Nothing but routine checks yet. | followed by the button below |
| `lg.showthem` | button | Show them | turns the checkbox on |
| `lg.count` | row text | The newest 100 lines. | under the list when it is full |
| `lg.cant` | error | Can't load the log. Trying again. |  |

## Task graph

| Id | Kind | Text | Where and when |
|---|---|---|---|
| `gr.group` | screen-reader text | show tasks as | label of the list / graph switch |
| `gr.list` | option | list |  |
| `gr.graph` | option | graph |  |
| `gr.box` | screen-reader text | task graph: 20 tasks in 5 layers; scrolls sideways | label of the graph's own box, which takes keyboard focus when it scrolls |
| `gr.node` | screen-reader text | T7, Search box on the notes page, working, after T3 and T4 | a node's label |
| `gr.scroll` | hint | The graph is wider than the window. Scroll it sideways, or switch to the list. | above the box, whenever it scrolls |

## Project overview and the needs-you indicator (14.10)

| Id | Kind | Text | Where and when |
|---|---|---|---|
| `ov.h` | heading | projects | h1 of the overview: what /factory opens when there are two or more projects and none is chosen |
| `ov.needs` | mark | needs you: 2 | on a project card, only when the project has open asks for you; amber outline, the number is the count |
| `ov.progress` | card text | 3 of 6 done | done tasks of all tasks |
| `ov.notasks` | card text | no tasks yet | a project the coordinator has not planned yet |
| `ov.name` | card text | notes | the project's name: the last part of its folder's path (the project's id when it has no folder); in mono above the goal on overview cards, above the goal on the project board (two or more projects), and before the goal in the needs-you list |
| `ov.card` | screen-reader text | notes, A notes page with search, running, needs you: 2, 2 of 8 done | a card's label; the whole card is one link into the project |
| `ov.err` | mark | can't read project | a project whose stored state the daemon cannot read; icon: triangle; danger colour. The card shows the project id, this mark, the hint and the daemon's error; no state word and no progress; it is not a link; it sorts last |
| `ov.err.hint` | sentence | Its state file could not be read. | under the mark; the daemon's error sentence follows as secondary text |
| `ov.order` | behaviour | (no words) | cards in this order: projects that need you, then running, stopped, finished, then those that cannot be read; inside a group as the daemon lists them |
| `ov.all` | link | all projects | only when there are two or more projects. On a project's board: the back link, in place of the aside's projects card. On the new-project form: in place of 'board' (never two back links). The task view keeps 'board', which returns to its project |
| `ni.btn` | button | needs you: 3 | in the header on every page, counting every project; not shown when the count is 0, nor before the first load. Lowercase on purpose (it sits beside "sheepdog factory · live" and matches the card mark): the one exception to buttons starting with a capital. Screen-reader labels "needs you: 3 asks in 2 projects", "needs you: 2 asks in 1 project", "needs you: 1 ask in 1 project"; aria-expanded. While the link is lost it stays, with the last known asks |
| `ni.h` | heading | needs you | heading of the list the button opens. The list is part of the page, directly under the header: it pushes the page down and covers nothing |
| `ni.item` | link | Delete the old notes table in the migration? | one link per open ask, under its project's heading (name · goal). Activating it closes the list and opens that project with the ask's card scrolled into view and focused; when that project's board is already shown (not its task view or the new-project form), the page does not reload: the list closes and the card is scrolled to and focused |
| `ni.inproj` | card text | 2 open | inside a project the asks-for-you card counts and lists only that project (exists; now per project) |
| `ni.none` | empty state | No open asks right now. | the asks card of a project with none (exists), even while the header's count shows another project's |

## The app (canvas Part 3)

Strings the iPhone and iPad app shows that `/factory` does not have. The
boards are the Part 3 `Factory…` boards (`FactoryHead`, `FactoryProject`,
`FactoryNewProject`, `FactoryModels`, `FactoryWindow`, `FactoryStates`).
Every other string on those boards is a string of the tables above. The app
uses the web's words with the app's casing for screen titles and section
labels ("Settings", "Tasks", "Asks for you"); state and stage words stay
lowercase.

| Id | Kind | Text | Where and when |
|---|---|---|---|
| `app.head.kind` | mark | factory | after the project's name on its head in Agents |
| `app.sec.stopped` | section label | Stopped | Agents, between Working and Done, only while a project is stopped |
| `app.head.stuck` | heading | The notes coordinator is stuck on a prompt | on the head under Needs you; the coordinator's own dialog follows |
| `app.head.stuck.no` | hint | To say what to do instead, use the box on the notes page. The coordinator gets it when it is next free. | under the dialog's "No" option |
| `app.new` | menu item | New factory project | the + menu |
| `app.tell` | label | Tell the notes factory | the text box on the project page |
| `app.tell.ph` | placeholder | An instruction, in your own words | the text box; never the only label |
| `app.tell.note` | hint | Delivered when the coordinator is next free. To answer an ask, use its card. | under the box, running |
| `app.tell.ask` | hint | A question? Ask by voice, or ask Sheepdog in Conversation. | under the box, every state |
| `app.tell.told` | status | Told the notes factory. | after Send; the voice's sentence (6.1) |
| `app.tell.stopped` | status | notes is stopped. It gets this when you resume it. | the voice's sentence (6.1) |
| `app.tell.coord` | status | The notes coordinator is not running. It gets this when it is back. | the voice's sentence (6.1) |
| `app.tell.fin` | refusal | notes is finished. Give it a new goal. | the daemon's sentence (6.2) |
| `app.tell.long` | refusal | Too long. Keep it under 1,000 characters. | with the counter `app.tell.count` (6.2) |
| `app.tell.count` | counter | 1,012 / 1,000 | beside the box, near the cap |
| `app.tell.gone` | status | This project is gone. | on Agents, after the project page closes (6.2) |
| `app.pair.stop` | refusal | Not stopped: Work Mac no longer knows this phone. Pair this phone again in Settings ▸ Work Mac, then press Stop again. | a project action without a paired-device identity; the computer's own name |
| `app.pair.say` | refusal | Not sent: Work Mac no longer knows this phone. Pair this phone again in Settings ▸ Work Mac, then send again. | the text box without a paired-device identity |
| `app.coord.btn` | button | Coordinator | opens the read-only window; shown only while a coordinator runs |
| `app.coord.off` | heading | The coordinator is not running: claude coordinator is signed out | the alert's cause as the daemon writes it; without an alert the heading ends at "not running" and no fix follows |
| `app.coord.fix` | sentence | Fix: sign in to claude at this Mac (Work Mac). Running tasks go on. | the alert's fix |
| `app.win.note` | note | View only. To tell the coordinator something, use the box on the notes page. An ask is answered on its card. | the coordinator's window |
| `app.win.updated` | status | Updated 2 s ago | the window's footer |
| `app.task.last` | heading | Last decision | the task view: the last coordinator or owner decision and its why |
| `app.run` | row text | worker · codex · 31 min | one run line under a task: role, CLI, time since it started |
| `app.np.folders` | label | folders sheepdog knows on Work Mac | the folder list in New project |
| `app.np.known` | mark | already a project: open it to give it a new goal | a folder that is already a project; not pickable |
| `app.np.none` | empty state | No folders to pick: herdr cannot be reached on Work Mac. Open herdr-app on Work Mac, then try again. | replaces `np.sugg.none`, which tells the reader to type a path |
| `app.np.fix` | label | Fix, in a terminal on Work Mac: | before a command fix; the phone cannot run it |
| `app.np.nofactory` | heading | The factory is not available on Work Mac | New project when the computer has no factory |
| `app.np.nofactory.p` | sentence | It needs sheepdog on herdr-core. Work Mac's sheepdog runs on herdr-tui, or herdr-core does not answer. | with the fix "Open herdr-app, or start herdr-core in a terminal:" and `hc server start` |
| `app.mo.done` | sentence | Used when you start the project. Nothing is sent until then. | the models editor opened from New project, with the button Done |
| `app.load` | status | Loading notes from Work Mac… | first load of a project |
| `app.cant` | error | Can't load notes. Trying again. | followed by "Work Mac did not answer in time. The rest of the app still works." |

## Marks: never colour alone

| Mark | Icon | Word | Colour (token) |
|---|---|---|---|
| start check passes | tick | pass | `done` |
| start check warns | triangle | warning | `primary-text` |
| start check fails | cross | fail | `danger` |
| start check skipped | dash | skipped | `muted` |
| step done | tick in a ring | done | `done` |
| step now | filled dot | now | `working` |
| step to come | empty ring | to come | `muted` |
| step skipped | dash in a dashed ring | skipped | `muted` |
| step ended (task ended without merging) | cross in a ring | dropped, closed | `idle` |
| log: merged | merge | merged | `done` on `done-soft` |
| log: held up | triangle | held up | `primary-text` on `primary-soft`, `blocked` outline |
| log: asked you | speech bubble | asked you | `working` on `working-soft` |
| log: your own action | person | you | `fg` on `code` |
| refusal | triangle | the sentence | `danger` on `danger-bg`; `role="alert"` |
| settings row changed | — | changed | `working` on `working-soft` |
| settings row not allowed | — | not allowed | `danger` on `danger-bg` |
| settings row not used | — | not used | `muted-on-fill` on `code` |
| needs you, header button | speech bubble | needs you: 3 | `fg` on `primary-soft`, `blocked` outline |
| needs you, on a project card | — | needs you: 2 | `fg` on `primary-soft`, `blocked` outline; the card's outline is `blocked` too |
| project state | dot | running, stopped, finished | dot `working`, `idle`, `done`; the word in `fg` on `code` |
| project cannot be read | triangle | can't read project | `danger` |

## Tab order and screen-reader labels

The order is the order in the page's source: the main column, then the
aside (projects, asks for you, agents). Every input has a visible label
tied to it. A refusal is `role="alert"` and sits directly after the
control that caused it; a status line ("Saved…", "Checking the folder…")
is `role="status"`. Focus survives the 3 s poll.


### Board (a running project)

| # | Control | Screen-reader label and notes |
|---|---|---|
| 1 | list / graph switch | group "show tasks as"; buttons "list", "graph", the chosen one pressed |
| 2 | full project goal | summary (exists) |
| 3 | Stop | button "Stop this project" |
| 4 | New goal | button "New goal" |
| 5 | task rows | link per task: ref, title, stage, why, then "step 3 of 5, review: …", model |
| 6 | done fold | summary (exists) |
| 7 | log: task filter | select "task" |
| 8 | log: show routine checks | checkbox "show routine checks" |
| 9 | log: PR links | link "PR #7, opens GitHub" |
| 10 | settings: each top row | see Settings panel |
| 11 | all settings | summary "all settings (38)" |
| 12 | projects | link per project: title, state word, counts |
| 13 | New project | button "New project" (projects card, in the aside) |
| 14 | ask cards | buttons per option (exists) |

### Board, narrow layout (under 760 px)

| # | Control | Screen-reader label and notes |
|---|---|---|
| 1 | the order changes | tasks heading, goal and project bar; asks for you; the task groups; log; projects; settings; agents. The page moves the sections in the source, so the tab order is the order seen |

### Graph view

| # | Control | Screen-reader label and notes |
|---|---|---|
| 1 | list / graph switch | before the graph |
| 2 | the graph box | region "task graph: 20 tasks in 5 layers; scrolls sideways", focusable only when it scrolls; arrow keys scroll it |
| 3 | nodes, layer by layer, left to right | link "T7, Search box on the notes page, working, after T3 and T4"; Enter opens the task; focus and hover draw the node's own edges and neighbours strong and dim the rest |

### New project form

| # | Control | Screen-reader label and notes |
|---|---|---|
| 1 | board | link "board" (back) |
| 2 | folder | input "folder"; described by its hint and by the refusal under it |
| 3 | suggestions | button per folder: "~/code/notes, git repository"; picking one fills the field |
| 4 | goal | textarea "goal" |
| 5 | models rows | button "Edit coordinator models", then worker, reviewer, retro; an open editor takes its row's place and ends with "Done" and "Cancel" |
| 6 | max running | number input "max running", described by "default 3, allowed 1 to 20" |
| 7 | fix blocks | button "Copy the command gh auth login" per command fix, in the start checks card; it copies only what the block shows |
| 8 | Check this folder | button; reads "Check again" once the checks have run |
| 9 | Start the project and its agents | button; when off it is aria-disabled, stays focusable and is described by "Start turns on when no check fails." |
| 10 | pairing box | when shown, directly after the start button: see Pairing |

### Pairing box

| # | Control | Screen-reader label and notes |
|---|---|---|
| 1 | pairing code | input "pairing code"; focus moves here when the box opens; the box is a region "pair this browser" |
| 2 | Pair | button "Pair" |
| 3 | No Sheepdog menu-bar app? | summary |
| 4 | Download the Sheepdog menu-bar app | link, inside the open fold |
| 5 | after Pair succeeds | the box closes and focus goes back to the control that asked (the Change button, Start, Stop the project, or the ask option) |

### Project bar

| # | Control | Screen-reader label and notes |
|---|---|---|
| 1 | running | button "Stop this project", button "New goal" |
| 2 | stopped | button "Resume this project", button "New goal" |
| 3 | finished | button "Reopen with a new goal"; while an agent runs, button "Stop this project" first |
| 4 | stop confirm | group labelled by "Stop this project?"; "Keep it running" (focus lands here), then "Stop the project"; Escape is Keep it running |
| 5 | new-goal form | textarea "new goal" (focus lands here), the submit button, "Cancel" |

### Settings panel

| # | Control | Screen-reader label and notes |
|---|---|---|
| 1 | each models row | button "Edit worker models" |
| 2 | each number row | number input labelled with the row's name, described by "default 2, allowed 1 to 5" and by its refusal; button "Change review rounds", only while the field differs from the stored value; on a changed row, button "Set review rounds back to the default, 2" |
| 3 | each choice row | select labelled with the row's name; button "Change second reviewer", only while the choice differs |
| 4 | a row waiting for its confirm | in place of its Change button: button "Save (Recommended)", button "Keep current". After either answer both buttons go and focus moves to the row's field |
| 5 | all settings | summary "all settings (38)"; its rows follow in the table's order |
| 6 | refusals | role="alert", directly after the row's buttons |

### Models editor

| # | Control | Screen-reader label and notes |
|---|---|---|
| 1 | per candidate, in order | select "CLI, first choice"; input "model, first choice" (with suggestions); select "effort, first choice"; button "Move agy gemini-4-neon up" (not on the first); button "Remove agy gemini-4-neon" (not on the last one left) |
| 2 | Add a model | button |
| 3 | Back to the default | button "Fill in the default worker models" |
| 4 | Change | button "Change worker models" |
| 5 | Cancel | button |
| 6 | after Change, Done or Cancel | the editor closes and focus goes to the row's Edit button; after Change the read-back follows in the row and is announced |

### Task view

| # | Control | Screen-reader label and notes |
|---|---|---|
| 1 | board | link (exists) |
| 2 | steps | an ordered list, not focusable; each item reads "review, now: review round 2; needs 2 passing, at most 5" |
| 3 | details | summary "details" |
| 4 | log for this task: show routine checks | checkbox |

### Log

| # | Control | Screen-reader label and notes |
|---|---|---|
| 1 | task | select "task" |
| 2 | show routine checks | checkbox |
| 3 | PR links and commit ids | link "PR #7, opens GitHub"; a short commit id is text with the whole id as its title |
| 4 | Show them | button, only in the only-routine-checks state |

### Overview and the needs-you indicator

| # | Control | Screen-reader label and notes |
|---|---|---|
| 1 | needs you: 3 | button in the header, first control of every page; "needs you: 3 asks in 2 projects" (singular forms "1 ask", "1 project"), expanded or collapsed. Escape closes the list and focus returns to the button. If a poll resolves the last open ask while the list is open, the open state resets to closed and focus moves out cleanly (to the main column's first control) so it is not stranded and the list does not reopen on a later ask |
| 2 | the open list | directly after the button, in the page under the header: per project a heading "notes · A notes page with search", then a link per ask reading the question |
| 3 | an ask link, activated | the list closes; the ask's project opens and focus lands on that ask's card under asks for you. If that project's board is already shown (not its task view or the new-project form) nothing reloads: the list closes, the card is scrolled into view and focused |
| 4 | focus across the 3 s poll | each ask link carries its ask's id, so focus stays on the same ask when the list is redrawn, also when two asks lead to the same project |
| 5 | all projects | link "all projects", first control of the main column on a project's board and on the new-project form, with two or more projects |
| 6 | board | link "board" on the task view, unchanged; on the new-project form only with no project or one |
| 7 | project cards | link per card: "notes, A notes page with search, running, needs you: 2, 2 of 8 done"; a project that cannot be read is a group, not a link: "old-importer, can't read project, …" |
| 8 | New project | button, after the cards |

## Which board draws which state

| Flow (plan.md, PR 14.0) | Board | Panels |
|---|---|---|
| New project | `FactoryWebNewProject` | way in 1: the empty page · way in 2: the projects list · the form · no suggestion available · the folder is not a git repository · goal empty · changing a model in the form: the editor · changing a model in the form: after Done · a model line that is refused · started |
| Start checks | `FactoryWebStartChecks` | before the first check · checks running · a failed check (pass, warning, fail, skipped; a command fix in its block; Check again; start off) · no failed check: start is on · a typed folder that is not a git repository · the daemon cannot run the checks |
| Pairing at the first write | `FactoryWebPairing` | beside a settings row · in the new-project form · beside a project action · in an ask card · wrong code · too many tries · the Sheepdog menu-bar app is not installed · how the action resumes |
| Stop, resume, new goal | `FactoryWebProjectActions` | where the actions live · the projects list says each state · running: stop asks first · running: new goal · stopped · stopped: a start refused by a start check · stopped: a start refused by a setting |
| Reopen | `FactoryWebProjectActions` | finished: nothing else to do · finished, its last agent still running · finished: the stop confirm · reopen with a new goal · reopen: the same goal typed again |
| Settings | `FactoryWebSettings`, `FactoryWebReadBack` | the panel, all settings open (changed value, stored value not allowed, row not used) · loading · cannot load · a field edited: Change appears · refusal with the range · refusal that names another row to change first · the value is already that · a change waiting for its confirm |
| Models per role | `FactoryWebModels` | the models rows, closed · the candidate editor · read-back for a models row · saved · CLI not installed · CLI not signed in · a model the account does not offer · the account's list is unknown |
| Read-back | `FactoryWebReadBack` | a change waiting for its confirm · the same ask as a card · saved · kept · refused at confirm: the table changed meanwhile · cancelled: the project finished meanwhile · back to the default |
| Task detail | `FactoryWebTaskLife` | the task view (with the brief card) · the details fold, open |
| Log (14.7) | `FactoryWebLog`, `FactoryWebTaskLife` | the log (day headings, marks, tidied line, PR link) · show routine checks on · filtered to one task · empty · only routine checks so far · a long line · a PR number with no address · cannot load · the task view's own log |
| Step bar (14.8) | `FactoryWebTaskLife` | the task view (named steps) · the compact bar on board rows · sent back from review · with a retro step · review skipped by your override · merged · ended without merging: dropped · ended without merging: closed · parked · where plan review will sit |
| Task graph (14.9) | `FactoryWebGraph` | 20 tasks in 5 layers · focus or hover on a node: its own edges stand out · a wide layer of 8 · one task: no graph offered · the switch, on list · 360 px: `FactoryWebGraph360` |
| Project overview and the needs-you indicator (14.10) | `FactoryWebOverview` | two or more projects: the overview (with a finished project and one that cannot be read) · the indicator, open · several projects, none needs you · inside a project · inside a project that needs nothing, while another does · the task view keeps "board" · the new-project form, with two or more projects · one project: no overview · no project |
| The narrow layout | `FactoryWebLifeHome360` | asks directly after the project bar; projects before settings |
| The whole page | `FactoryWebLifeHome` | every new part in its place on a running project |

Each board has a dark copy (`…Dark`) and a 360 px copy (`…360`).

## Open questions

Round 2 (2026-11-24) settled the nine questions the design review marked
"before code PRs" and turned two more into notes for the plan text. What
is left blocks no code PR.

### Settled in round 2 (after the design review, 2026-11-24)

Each is now a board, a string in this table, or both. The number is the question's number in the first drawing.

| # | Question | Settled as |
|---|---|---|
| 2 | The wrong-code sentence | `pr.wrong` is the page's sentence for a 410. 14.2 writes it in `asks.js` when it moves `pairBoxHTML`, and replaces `PAIR_HINT` ("…to answer asks") with `pr.p`. The daemon's own sentence stays |
| 3 | The recommended option | No fill: a bold outline and the tag "Recommended", as the canvas's `FactoryAskCard` already draws it. The tag's text and the label "(Recommended)" stay as `asks.js` writes them today, so 14.2 restyles `.ask-opt.rec` and `.rec-tag` in CSS only. The shipped ask cards change look. The owner has been told |
| 4 | A refusal that carries a setting key | The page never shows the key. A start refused by a stored value answers `ac.refused.set` ("Not started: a setting is not allowed. Change it in settings below.") and the settings row carries `se.bad.p` with the range. Board: `FactoryWebProjectActions`, "stopped: a start refused by a setting" |
| 5 | A refused settings pair in the new-project form | `check` and `start` answer a refused pair as `{name, message}`; the page shows the message under the row with that name (`np.r.row`). At a first start only the form of a value is refused there (`With`); installed and signed in are start checks; "the account does not offer it" stays with `ProposeSetting` and the agent's start. Board: `FactoryWebNewProject`, "a model line that is refused" |
| 7 | The label of a models row | `<role> models` ("worker models"). 14.3 writes it as the row's `Label` in Go |
| 8 | Command fixes with prose in them | The block holds the text after `run:` up to the first ` (` or `, then `; the rest is plain text under the block, and Copy copies only the block. A page rule in 14.5; the checks are not reworded. Board: `FactoryWebStartChecks`, "a failed check" |
| 9 | What the stop confirm can say | No count and no roles. "Its running agents end now. Resume goes on with the same goal." and, on a finished project, "Its last running agent ends now. The project stays finished." The project bar says "Finished. An agent is still running." `Live` as yes or no is enough |
| 10 | Ended and parked in the step bar | One new step state, `ended`, for the last step of a dropped or closed task. Parked is the `now` step with the text `sp.parked`; no look of its own. 14.8 adds the fifth state |
| 12 | The task graph's layout | (a) The graph view takes the page's full width with the aside cards under it. (b) Nodes keep one size; a layer of 8 scrolls in the graph's own box, with the hint `gr.scroll` whenever the box scrolls. (c) Focus or hover draws a node's own edges and their other ends strong and dims the rest, which also makes a layer-skipping edge followable. No text in a node is under 12 px. 14.9 builds all three |

Other changes of round 2, from the review's majors and minors: the log mark "needs a decision" is now "held up"; a settings row shows Change only while its field differs from the stored value; the review-rounds chip is gone from task rows; the board's order is tasks, log, settings, and under 760 px the asks come directly after the project bar and the projects card before settings; the new-project form draws the models editor with Done and the row after Done; a failed check points back to the models rows (`ck.models`, `ac.refused.models`); the app is "the Sheepdog menu-bar app" everywhere; the task view draws the brief card; `np.notgit`, `mo.r.install` and `mo.r.signin` say what to do next (the last two reuse the start check's own detail and fix).

### Notes for the plan text

The design does not edit plan.md. These are the lines the plan's author should change so that plan, boards and table agree.

1. **14.8, "plan review is not a step yet" (question 11).** "Slice 11 is not built" is out of date: 11.1 is merged (#800). `PlanReviews` is stored on the project, not on a task, so who derives a per-task step from it is still to be written. The board keeps only a placeholder that shows where the step would sit, with no text of its own; slice 14 does not build it. **Settled (orchestrator, after the plan update):** 14.8 builds the plan-review step as a real step; see plan.md 14.8.
2. **14.7, "show observations" (question 13).** The toggle is "show routine checks" (`lg.obs`), to match the plan's own empty-state sentence "Nothing but routine checks yet. Show them". "Observation" is not a glossary word.
3. **14.7, the kind `needs_decision`.** Its mark on screen is "held up", not "needs a decision": the coordinator decides, and next to "asked you" the old word read as a question to the owner.
4. **14.2.** It owns `pr.wrong` and `pr.p` in `asks.js`, restyles `.ask-opt.rec` without a fill (CSS only: the tag stays "Recommended"), shows a row's Change button only while the field differs, and holds short messages ("Saved…", "Kept at 2…", "Copied") in its own state across the 3 s poll.
5. **14.4 and 14.5.** `check` and `start` answer a refused settings pair as `{name, message}`. A start refused by a stored value that is not allowed answers `ac.refused.set`, not `Validate`'s sentence with the key. 14.5 cuts a `run:` fix at the first ` (` or `, then `.
6. **14.4 and 14.6.** The stop confirm and the project bar need no count and no roles; `Live` as yes or no is enough.
7. **14.8.** A fifth step state, `ended`. Parked is `now` with its text.
8. **14.9.** The graph view is full width with the aside under it; the focus and hover highlight; 12 px as the smallest text; the scroll hint whenever the box scrolls.
9. **The narrow layout** (under about 760 px): tasks heading and project bar, asks for you, task groups, log, projects, settings, agents, moved in the source so the tab order follows. No PR names this yet; it fits 14.2, which adds the settings section that pushed the asks down.
10. **Glossary.** On screen but not in glossary.md: retro, parked, recap, CLI, effort, override. The models group now says what a retro reviewer is; the other words need glossary lines, which a docs PR adds.

### Still open (none blocks a code PR)

1. **How the page knows the Sheepdog menu-bar app is missing.** Kept as a fold that is always under the pairing form. Detection is not built.
6. **"max running" has no meaning line** (Q5 of the plan). Left as decided; the fresh owner run measures it, and a misread name is fixed by renaming its key.
14. **The models hint.** "In order: the engine uses the first one it can start." mostly holds, but a reviewer or retro candidate equal to the worker's model is kept as a fallback, not skipped. The 14.3 writer checks the sentence for those two roles.
15. **Messages that outlive a poll.** An implementation note for 14.2 (see the plan notes).
16. **The stored value "owner" of `design gate`** is shown as stored ("allowed owner, review, not set"), though on screen the owner is "you". Accepted: renaming a stored value is a Go change outside this slice.

### Review minors not taken, and why

- **"the ladder says"** (not a glossary word): an existing string of the task view that slice 14 does not touch. Listed as a candidate ("last step") for the PR that next edits that card.
- **No graph switch under about 600 px:** needs a plan change (14.9 asks for the graph at 360 px in its own scrolling box). Listed as a candidate.
- **Field outlines at about 1.3:1:** the design system's token, the same on every sheepdog page; a candidate for the design system, not for these boards.
