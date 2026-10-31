// Sheepdog: the conversation with your agents, from the sheepdog daemon on this
// computer. Docked in the sidebar this mod is the panel (the status, the voice
// controls, the question, a composer and the timeline); a sidebar too narrow
// for it shows a compact rail, which opens the same panel as a window (also
// what opens when the mod is not docked). It reads the daemon's HTTP API and
// its /events stream and sends a typed line (POST /api/turn), a tap on a
// question (POST /api/pending/answer) and the voice's controls (POST
// /api/voice/mute, /stop, /ptt); it never opens /ws, which would take the
// voice away from the phone. Only this file and ui/ import "herdr"; net/ and
// model/ run under node, so they are tested there (docs/dev/design/
// desktop-client.md, section 6).

import { View } from "gpui-kit";
import { TextareaState } from "gpui-base";
import { exists } from "fs/promises";
import { connect } from "net";
import { closeOverlay, exec, home, openOverlay, props, run, shared, theme } from "herdr";
import { baseUrl, hostPort } from "./net/config.js";
import { createHttp } from "./net/http.js";
import { createLive, POLL_MS } from "./net/live.js";
import { focusCommand } from "./model/cards.js";
import { keyMaker, restored, submission } from "./model/composer.js";
import { DOCKED_WIDTH, layoutMode, needsDock } from "./model/layout.js";
import { createOutbox, pendingItems } from "./model/outbox.js";
import { amber } from "./model/palette.js";
import { decide, estimateHeight } from "./model/presentation.js";
import { createAnswers } from "./model/questions.js";
import { liveSuggestions } from "./model/replies.js";
import { deriveStatus } from "./model/status.js";
import { createStore } from "./model/store.js";
import { layoutEntries } from "./model/timeline.js";
import { createVoice, voiceControls } from "./model/voice.js";
import { panelView, WINDOW } from "./ui/panel.js";
import { railView } from "./ui/rail.js";

export default class Sheepdog extends View {
  init(_props, cx) {
    const p = props() ?? {};
    // A window is always the panel; the docked block is the panel too, until
    // its sidebar is too narrow (model/layout.js).
    this.overlay = p.overlay;
    this.offerDock = needsDock({ overlay: p.overlay, docked: p.docked });
    this.dockNote = null;
    this.url = p.url || shared("sheepdog.url");
    const base = baseUrl(this.url);
    this.size = this.overlay ? { width: WINDOW.width, height: WINDOW.height } : { width: DOCKED_WIDTH, height: 640 };
    this.technical = false;
    this.wantOlder = false; // the end of the list was drawn
    this.memo = null;
    const http = createHttp({ fetch, base });
    this.store = createStore({ http, conversation: true });
    this.initPanel(http, cx);
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
    cx.timer.every(250, (cx) => {
      this.pageBack(cx);
      // What has stayed long enough (a note, a settled card) goes.
      this.outbox.prune();
      this.answers.prune(this.store.getState().app);
      this.voice.prune();
    });
  }

  // The composer's textarea (made here, it needs a live host call), the typed
  // lines on their way, the taps on questions and the voice's controls.
  initPanel(http, cx) {
    this.sheet = null; // what is open over the list: a card, or the questions
    this.presentation = null; // inline | compact, once decided (model/presentation.js)
    this.focusNote = null;
    const newKey = keyMaker();
    const refresh = () => this.store.refresh();
    this.outbox = createOutbox({ http, newKey, refresh });
    this.answers = createAnswers({ http, newKey, refresh });
    this.outbox.subscribe(() => cx.notify());
    this.answers.subscribe(() => cx.notify());
    this.voice = createVoice({ http, exec, exists, home, newKey, refresh });
    this.voice.subscribe(() => cx.notify());
    // Is Sheepdog.app installed? (Read-only; the timer looks again.)
    this.voice.check();
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
    this.presentation = decide({ heights: cards.map((c) => estimateHeight(c.pending, { width: this.size.width })), area: this.size.height, held }, this.presentation);
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
    // A held hold-to-talk must not leave the microphone open.
    this.voice?.pttUp();
    this.composer?.release();
  }

  // The rail opens the panel as a window; `docked` tells that copy it came
  // from the docked block, so it does not offer docking.
  openPanel() {
    // One window, remembered where it was dropped and back after a restart.
    openOverlay({ mod: "sheepdog", props: { docked: true, ...(this.url ? { url: this.url } : {}) }, kind: "window", key: "sheepdog-panel", keep: true, ...WINDOW });
  }

  // The one step toward the sidebar the mod can take: a copy of the shown
  // layout is a layout you own (layout.clone, which switches to it). Adding
  // the block to it is the user's (see ui/panel.js's hint, README.md).
  copyLayout(cx) {
    const name = shared("layout");
    try {
      if (typeof name !== "string" || !name) throw new Error("no layout name");
      run("layout.clone", { name });
      this.dockNote = null;
    } catch {
      this.dockNote = "herdr-app could not copy the layout. Copy it from herdr-app's layout menu instead.";
    }
    cx.notify();
  }

  // The voice's buttons: what each sends is model/voice.js's.
  voiceHandlers(controls) {
    const send = (action) => (cx) => {
      if (!action) return;
      cx.spawn(async () => {
        await this.voice.send(action);
      });
    };
    return {
      talk: send(controls.talk.action),
      mute: send("mute"),
      stop: send("stop"),
      useHere: send("use-here"),
      pttDown: (cx) => {
        cx.spawn(async () => {
          await this.voice.pttDown();
        });
      },
      pttUp: (cx) => {
        cx.spawn(async () => {
          await this.voice.pttUp();
        });
      },
    };
  }


  // Run in a tab: `sheepdog setup` starts the daemon if needed and walks through the key.
  runSetup() {
    run("terminal.open", { argv: ["sheepdog", "setup"], place: "tab" });
  }

  render() {
    // "Needs you" is the phone's amber, not the theme's red `blocked`.
    const base = theme();
    const t = { ...base, blocked: amber(base.chrome) };
    const snapshot = this.store.getState();
    const status = deriveStatus(snapshot);
    const onSize = (size, cx) => {
      const changed = Math.round(size.width) !== Math.round(this.size.width) || Math.round(size.height) !== Math.round(this.size.height);
      this.size = size;
      if (changed) cx.notify();
    };
    if (layoutMode({ width: this.size.width, overlay: this.overlay }) === "panel") {
      const derived = this.derived();
      const voice = this.voice.getState();
      const controls = voiceControls({ phase: status.phase, voice: snapshot.app?.voice, view: status.voice, installed: voice.installed, noVoice: voice.notice?.code === "no-voice" });
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
        voice: { controls, state: voice, on: this.voiceHandlers(controls) },
        dock: this.offerDock ? { note: this.dockNote, onCopyLayout: (_e, cx) => this.copyLayout(cx) } : null,
        sheet: this.sheet?.type === "card" ? { ...this.sheet, note: this.focusNote, onFocus: (_e, cx) => this.focusAgent(this.sheet.card, cx), onClose: closeSheet } : this.sheet && { ...this.sheet, onClose: closeSheet },
        onSetup: () => this.runSetup(),
        onToggleTechnical: (_e, cx) => {
          this.technical = !this.technical;
          cx.notify();
        },
        onClose: this.overlay ? () => closeOverlay(this.overlay) : null,
        onEndShown: () => {
          this.wantOlder = true;
        },
        onOpenRow: (key, cx) => this.openRow(key, cx),
      }).on_size_change(onSize);
    }
    return railView(t, status, { onOpen: () => this.openPanel() }).on_size_change(onSize);
  }
}
