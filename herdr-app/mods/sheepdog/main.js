// Sheepdog: the conversation with your agents, from the sheepdog daemon on this
// computer. Docked, this mod is the rail (the voice, who needs you); opened as
// an overlay it is the panel (a window with the timeline and a composer). It
// reads the daemon's HTTP API and its /events stream and sends two things: a
// typed line (POST /api/turn) and a tap on a question (POST
// /api/pending/answer); it never opens /ws, which would take the voice away
// from the phone. Only this file and ui/ import "herdr"; net/ and model/ run
// under node, so they are tested there (docs/dev/design/desktop-client.md,
// section 6).

import { View } from "gpui-kit";
import { TextareaState } from "gpui-base";
import { connect } from "net";
import { closeOverlay, openOverlay, props, run, shared, theme } from "herdr";
import { baseUrl, hostPort } from "./net/config.js";
import { createHttp } from "./net/http.js";
import { createLive, POLL_MS } from "./net/live.js";
import { focusCommand } from "./model/cards.js";
import { keyMaker, restored, submission } from "./model/composer.js";
import { createOutbox, pendingItems } from "./model/outbox.js";
import { decide, estimateHeight } from "./model/presentation.js";
import { createAnswers } from "./model/questions.js";
import { liveSuggestions } from "./model/replies.js";
import { deriveStatus } from "./model/status.js";
import { createStore } from "./model/store.js";
import { layoutEntries } from "./model/timeline.js";
import { panelView, WINDOW } from "./ui/panel.js";
import { COMPACT_BELOW, railView } from "./ui/rail.js";

export default class Sheepdog extends View {
  init(_props, cx) {
    const p = props() ?? {};
    // An overlay copy is the panel; the docked block is the rail.
    this.isPanel = Boolean(p.overlay);
    this.url = p.url || shared("sheepdog.url");
    const base = baseUrl(this.url);
    this.size = { width: 200, height: 400 };
    this.technical = false;
    this.wantOlder = false; // the end of the list was drawn
    this.memo = null;
    const http = createHttp({ fetch, base });
    // The rail has no use for the timeline, so it never asks for it.
    this.store = createStore({ http, conversation: this.isPanel });
    if (this.isPanel) this.initPanel(http, cx);
    // The data layer is a task: it keeps asking until close().
    cx.spawn(async (cx) => {
      this.store.subscribe((snapshot) => {
        // The conversation may now show what the outbox is waiting on.
        this.outbox?.reconcile(snapshot.history.items);
        cx.notify();
      });
      this.live = createLive({
        store: this.store,
        connect,
        endpoint: hostPort(base),
        clock: { now: () => Date.now(), sleep: (ms) => cx.sleep(ms) },
      });
      await this.live.run();
    });
    if (this.isPanel) {
      cx.timer.every(250, (cx) => {
        this.pageBack(cx);
        // What has stayed long enough (a note, a settled card) goes.
        this.outbox.prune();
        this.answers.prune(this.store.getState().app);
      });
    }
  }

  // What only the panel has: the composer's textarea (made here, it needs a
  // live host call), the typed lines on their way and the taps on questions.
  initPanel(http, cx) {
    this.panelSize = { width: WINDOW.width, height: WINDOW.height };
    this.sheet = null; // what is open over the list: a card, or the questions
    this.presentation = null; // inline | compact, once decided (model/presentation.js)
    this.focusNote = null;
    const newKey = keyMaker();
    const refresh = () => this.store.refresh();
    this.outbox = createOutbox({ http, newKey, refresh });
    this.answers = createAnswers({ http, newKey, refresh });
    this.outbox.subscribe(() => cx.notify());
    this.answers.subscribe(() => cx.notify());
    this.composer = TextareaState.new({ placeholder: "Message Sheepdog…", rows: 1 });
    this.composer.set_auto_grow(1, 5);
    // `submit`, never `change`: an input method's commit fires `change`.
    this.composer.on("submit", (e, cx) => {
      const text = submission(this.composer.value(), e);
      if (text == null) {
        // Enter on nothing: drop the newline it inserted. Shift+Enter keeps its.
        if (!e.shift) this.composer.set_value("");
        return;
      }
      this.send(text, cx, true);
    });
  }

  // Sends a typed line: what the textarea held (`fromComposer`) or a tapped
  // suggested reply. One line at a time; while one is on its way the text stays.
  send(text, cx, fromComposer) {
    if (this.outbox.getState().busy) {
      if (fromComposer) this.composer.set_value(text);
      return;
    }
    if (fromComposer) this.composer.set_value("");
    cx.spawn(async (cx) => {
      const { restore } = await this.outbox.submit(text);
      // A failed line goes back into the textarea; a tapped option stays a button.
      if (restore != null && fromComposer) this.composer.set_value(restored(restore, this.composer.value()));
      cx.notify();
    });
  }

  answer(question, option, cx) {
    cx.spawn(async () => {
      await this.answers.answer(question, option);
    });
  }

  // Shows the agent a card is about in herdr-app, in place of the phone's agent window.
  focusAgent(card, cx) {
    const command = focusCommand(card);
    if (!command) return;
    try {
      run(command.name, command.args);
      this.focusNote = null;
    } catch {
      this.focusNote = "herdr-app could not show that agent.";
    }
    cx.notify();
  }

  // A press on a row of the list (its key is the row id): a card opens.
  openRow(key, cx) {
    const card = this.entries().find((e) => e.row.id === key)?.row.item?.card;
    if (!card) return;
    this.sheet = { type: "card", card };
    this.focusNote = null;
    cx.notify();
  }

  // The end of the list was drawn (render cannot ask: it may not notify): page
  // back. Older rows are added below, so nothing moves under the reader.
  pageBack(cx) {
    if (!this.wantOlder) return;
    this.wantOlder = false;
    cx.spawn(async () => {
      await this.store.loadOlder();
    });
  }

  // What the list draws and the composer offers, for the history as it is and
  // the lines still on their way, worked out once per change: the entries, and
  // the suggested replies that can still be tapped.
  derived() {
    const { history, replies } = this.store.getState();
    const sending = this.outbox.getState().entries;
    const m = this.memo;
    if (m?.history !== history || m.technical !== this.technical || m.sending !== sending || m.replies !== replies) {
      const items = [...history.items, ...pendingItems(sending)];
      this.memo = { history, technical: this.technical, sending, replies, entries: layoutEntries(items, { showTechnical: this.technical }), live: liveSuggestions(items, replies) };
    }
    return this.memo;
  }

  entries() {
    return this.derived().entries;
  }

  // How the open questions float: the cards, and inline or compact for this
  // window (the rule of model/presentation.js, with heights estimated).
  questions(snapshot) {
    const cards = this.answers.cards(snapshot.app);
    const held = this.sheet?.type === "questions" || this.answers.busy() || cards.some((c) => c.phase === "answered" || c.phase === "gone");
    this.presentation = decide({ heights: cards.map((c) => estimateHeight(c.pending, { width: this.panelSize.width })), area: this.panelSize.height, held }, this.presentation);
    // With nothing to answer there is nothing to open.
    if (cards.length === 0 && this.sheet?.type === "questions") this.sheet = null;
    return {
      cards,
      presentation: this.presentation,
      busy: this.answers.busy(),
      onAnswer: (q, option, cx) => this.answer(q, option, cx),
      onOpen: (_e, cx) => {
        this.sheet = { type: "questions" };
        cx.notify();
      },
    };
  }

  async close() {
    this.live?.stop();
    this.composer?.release();
  }

  openPanel() {
    // One window, remembered where it was dropped and back after a restart.
    openOverlay({ mod: "sheepdog", props: this.url ? { url: this.url } : {}, kind: "window", key: "sheepdog-panel", keep: true, ...WINDOW });
  }

  // Run in a tab: `sheepdog setup` starts the daemon if needed and walks through the key.
  runSetup() {
    run("terminal.open", { argv: ["sheepdog", "setup"], place: "tab" });
  }

  render() {
    const t = theme();
    const snapshot = this.store.getState();
    const status = deriveStatus(snapshot);
    if (this.isPanel) {
      const derived = this.derived();
      const outbox = this.outbox.getState();
      const closeSheet = (_e, cx) => {
        this.sheet = null;
        cx.notify();
      };
      return panelView(t, {
        status,
        entries: status.phase === "down" ? [] : derived.entries,
        paging: { synced: snapshot.history.synced, canLoadOlder: snapshot.history.nextBefore != null, loadingOlder: snapshot.loadingOlder },
        technical: this.technical,
        retryMs: POLL_MS,
        compose: { state: this.composer, live: derived.live, notice: outbox.notice, busy: outbox.busy, onSend: (text, cx) => this.send(text, cx, false) },
        questions: this.questions(snapshot),
        sheet: this.sheet?.type === "card" ? { ...this.sheet, note: this.focusNote, onFocus: (_e, cx) => this.focusAgent(this.sheet.card, cx), onClose: closeSheet } : this.sheet && { ...this.sheet, onClose: closeSheet },
        onSetup: () => this.runSetup(),
        onToggleTechnical: (_e, cx) => {
          this.technical = !this.technical;
          cx.notify();
        },
        onClose: () => closeOverlay(props().overlay),
        onEndShown: () => {
          this.wantOlder = true;
        },
        onOpenRow: (key, cx) => this.openRow(key, cx),
      }).on_size_change((size, cx) => {
        const changed = Math.round(size.width) !== Math.round(this.panelSize.width) || Math.round(size.height) !== Math.round(this.panelSize.height);
        this.panelSize = size;
        if (changed) cx.notify();
      });
    }
    return railView(t, status, {
      width: this.size.width,
      retryMs: POLL_MS,
      onOpen: () => this.openPanel(),
    }).on_size_change((size, cx) => {
      const was = this.size.width < COMPACT_BELOW;
      this.size = size;
      if (was !== size.width < COMPACT_BELOW) cx.notify();
    });
  }
}
