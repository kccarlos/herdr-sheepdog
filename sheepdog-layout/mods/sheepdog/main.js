// Sheepdog: the conversation with your agents, from the sheepdog daemon on this
// computer. Docked in the sidebar this mod is the panel (the status, the voice
// controls, the question, a composer and the timeline); a sidebar too narrow
// for it shows a compact rail, which opens the same panel as a window (also
// what opens when the mod is not docked). It reads the daemon's HTTP API and
// its /events stream and sends a typed line, a tap on a question, the voice's
// controls, a confirmed new conversation, and the Settings actions; it never
// opens /ws, which would take the voice away from the phone. Only this file and
// ui/ import "herdr"; net/ and model/ run under node, so they are tested there
// (docs/dev/design/desktop-client.md, section 6).

import { View } from "gpui-kit";
import { TextareaState } from "gpui-base";
import { exists } from "fs/promises";
import { connect } from "net";
import { closeOverlay, exec, focused, home, nextCommand, openOverlay, props, run, setShared, shared, theme, window as appWindow } from "herdr";
import { baseUrl, hostPort } from "./net/config.js";
import { createHttp } from "./net/http.js";
import { createLive, POLL_MS } from "./net/live.js";
import { focusCommand } from "./model/cards.js";
import { keyMaker, restored, submission } from "./model/composer.js";
import { COMPACT_QUESTION_HEIGHT, DOCKED_WIDTH, layoutMode, needsDock, panelBudget } from "./model/layout.js";
import { createOutbox, pendingItems } from "./model/outbox.js";
import { amber } from "./model/palette.js";
import { decide, estimateHeight, natural } from "./model/presentation.js";
import { createAnswers } from "./model/questions.js";
import { keyAction, leaveCommand } from "./model/keys.js";
import { liveSuggestions } from "./model/replies.js";
import * as overlay from "./model/overlay-actions.js";
import { emptySettingsForm, SETTINGS_OPEN_KEY } from "./model/settings.js";
import { sheepdogProgram, startDaemon } from "./model/host-app.js";
import { later } from "./model/later.js";
import { openSetup } from "./model/setup-link.js";
import { deriveStatus } from "./model/status.js";
import { createStore } from "./model/store.js";
import { layoutEntries } from "./model/timeline.js";
import { createVoice, voiceControls } from "./model/voice.js";
import { agentOverlay } from "./ui/agent.js";
import { menuOverlay } from "./ui/menu.js";
import { pairOverlay } from "./ui/pair.js";
import { panelView, WINDOW } from "./ui/panel.js";
import { resetOverlay } from "./ui/reset.js";
import { settingsOverlay } from "./ui/settings.js";
import { railView } from "./ui/rail.js";

export default class Sheepdog extends View {
  init(_props, cx) {
    const p = props() ?? {};
    // A window is always the panel; the docked block is the panel too, until
    // its sidebar is too narrow (model/layout.js). Settings, pairing, agent
    // details, New conversation and the header menu are separate overlays.
    this.overlay = p.overlay;
    this.overlayRole = typeof p.role === "string" ? p.role : p.overlay ? "panel" : "block";
    const panelCopy = this.overlayRole === "block" || this.overlayRole === "panel";
    this.offerDock = panelCopy && needsDock({ overlay: p.overlay, docked: p.docked });
    this.dockNote = null;
    this.url = p.url || shared("sheepdog.url");
    const base = baseUrl(this.url);
    // What model/overlay-actions.js needs of the view (it has no herdr imports).
    this.base = base;
    this.exec = exec;
    this.platform = () => appWindow()?.platform;
    this.size = this.overlay ? { width: WINDOW.width, height: WINDOW.height } : { width: DOCKED_WIDTH, height: 640 };
    this.technical = false;
    this.wantOlder = false; // the end of the list was drawn
    this.memo = null;
    const http = (this.http = createHttp({ fetch, base }));
    if (panelCopy) {
      this.store = createStore({ http, conversation: true });
      this.initPanel(http, cx);
      this.initLive(cx, base);
      if (this.overlayRole === "block") this.initKeys(cx);
      cx.timer.every(250, (cx) => {
        this.pageBack(cx);
        // What has stayed long enough (a note, a settled card) goes.
        this.outbox.prune();
        this.answers.prune(this.store.getState().app);
        this.voice.prune(this.watchRunning());
      });
    } else if (this.overlayRole === "settings") this.initSettings(cx);
    else if (this.overlayRole === "pair") this.initPair(cx);
    else if (this.overlayRole === "agent") this.initAgent(p.agent, cx);
    else if (this.overlayRole === "reset") this.initReset();
    if (!panelCopy && this.overlayRole !== "menu") {
      this.overlayFocus = cx.focus_handle();
      this.overlayFocus.focus();
    }
  }

  initLive(cx, base) {
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
  }

  // The docked block can hold the keyboard (`"focusable"` in mod.json):
  // `pane.go` sends "focus", and the keys are the rules of model/keys.js.
  initKeys(cx) {
    this.panelFocus = cx.focus_handle();
    cx.spawn(async (cx) => {
      for (;;) {
        if ((await nextCommand()) === "focus") {
          this.panelFocus.focus();
          cx.notify();
        }
      }
    });
  }

  onKey(event, cx) {
    const question = this.answers.cards(this.store.getState().app).find((card) => card.phase === "open") ?? null;
    const action = keyAction(event.key, { onPanel: this.panelFocus.is_focused(), question, suggestions: this.derived().live });
    if (!action) return;
    cx.stop_propagation();
    if (action.type === "composer") this.composer.focus();
    else if (action.type === "panel") this.panelFocus.focus();
    else if (action.type === "leave") this.leave();
    else if (action.type === "answer") this.answer(action.question, action.option, cx);
    else if (action.type === "send") this.send(action.text, cx, false);
  }

  // Esc on the panel: the keys go back to the pane that had them (a click
  // into the panel leaves `pane.go` without a starting point, so not pane.go).
  leave() {
    const { name, args } = leaveCommand(focused());
    try {
      run(name, args);
    } catch {
      run("pane.go", { direction: "left" });
    }
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
    this.voice = createVoice({ http, exec, exists, home, newKey, refresh, openSetup: () => openSetup({ exec, platform: this.platform(), base: this.base }) });
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

  initSettings(cx) {
    // Which sections are open is remembered between openings.
    this.settingsForm = emptySettingsForm(shared(SETTINGS_OPEN_KEY));
    overlay.loadSettings(this, cx);
  }

  toggleSettingsSection(id, cx) {
    overlay.toggleSettingsSection(this, id, cx);
    setShared(SETTINGS_OPEN_KEY, this.settingsForm.open);
  }

  initPair(cx) {
    this.pairForm = { loading: true, error: "", link: "", matrix: null };
    overlay.loadPair(this, cx, false);
  }

  initAgent(name, cx) {
    this.agentName = String(name || "");
    this.agentForm = { loading: true, error: "", agent: null };
    overlay.loadAgent(this, cx);
  }

  initReset() {
    this.resetForm = { busy: false, error: "" };
    const newKey = keyMaker();
    this.newResetKey = newKey;
    this.resetKey = newKey();
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

  // A press on a row of the list (its key is the row id): a card opens, or,
  // when the row names an agent, that agent's details do.
  openRow(key, cx) {
    const item = this.entries().find((e) => e.row.id === key)?.row.item;
    if (item?.card) {
      this.sheet = { type: "card", card: item.card };
      this.focusNote = null;
      cx.notify();
      return;
    }
    const agent = String(item?.agent || "").trim();
    if (agent) this.openAgentDetails(agent);
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
  questions(snapshot, area) {
    const cards = this.answers.cards(snapshot.app);
    const held = this.sheet?.type === "questions" || this.answers.busy() || cards.some((c) => c.phase === "answered" || c.phase === "gone");
    this.presentation = decide({ heights: cards.map((c) => estimateHeight(c.pending, { width: this.size.width })), area, held }, this.presentation);
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
    openOverlay({ mod: "sheepdog", props: { role: "panel", docked: true, ...(this.url ? { url: this.url } : {}) }, kind: "window", key: "sheepdog-panel", keep: true, ...WINDOW });
  }

  overlayProps(extra = {}) {
    return { ...(this.url ? { url: this.url } : {}), ...extra };
  }

  closeOverlaySelf() {
    closeOverlay(props().overlay);
  }

  openHeaderMenu() {
    openOverlay({
      mod: "sheepdog",
      kind: "popup",
      width: 240,
      props: this.overlayProps({ role: "menu" }),
    });
  }

  chooseMenu(role, cx) {
    closeOverlay(props().overlay);
    if (role === "reset") {
      openOverlay({
        mod: "sheepdog",
        kind: "dialog",
        width: 400,
        props: this.overlayProps({ role }),
      });
    } else {
      const size = role === "settings" ? { width: 540, height: 700 } : { width: 410, height: 650 };
      openOverlay({
        mod: "sheepdog",
        kind: "window",
        key: `sheepdog-${role}`,
        props: this.overlayProps({ role }),
        ...size,
      });
    }
  }

  openAgentDetails(name) {
    openOverlay({
      mod: "sheepdog",
      kind: "window",
      key: "sheepdog-agent-details",
      width: 440,
      height: 500,
      props: this.overlayProps({ role: "agent", agent: name }),
    });
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
      action: send(controls.action?.id),
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

  // The facts about this computer that model/host-app.js words: where the
  // app is looked for and whether it runs. Read-only.
  host() {
    const { installed, running } = this.voice.getState();
    return { platform: this.platform(), installed, running };
  }

  // Is "is Sheepdog.app running" worth asking: macOS, daemon up, no holder.
  watchRunning() {
    const host = this.host();
    return host.platform === "macos" && deriveStatus(this.store.getState(), host).voice.noHolder === true;
  }

  // Start Sheepdog (daemon down): `sheepdog start`, then look again. `cx` is
  // the click's; the async part uses only the spawned context (model/later.js).
  startDaemon(cx) {
    if (this.starting) return;
    this.starting = true;
    this.downNote = "Starting Sheepdog…";
    cx.notify();
    later(cx, async (acx) => {
      const result = await startDaemon(exec, { exists, home: home() });
      this.starting = false;
      this.downNote = result.ok ? null : result.text;
      acx.notify();
      try {
        await this.store.refresh();
      } catch {
        // the next poll tells
      }
      acx.notify();
    });
  }

  // Run in a tab: `sheepdog setup` starts the daemon if needed and walks through the key.
  runSetup(cx) {
    later(cx, async () => {
      const program = await sheepdogProgram({ exists, home: home() });
      run("terminal.open", { argv: [program, "setup"], place: "tab" });
    });
  }

  render() {
    // "Needs you" is the phone's amber, not the theme's red `blocked`.
    const base = theme();
    const t = { ...base, blocked: amber(base.chrome) };
    if (this.overlayRole === "menu") {
      return menuOverlay(t, {
        onSettings: (_e, cx) => this.chooseMenu("settings", cx),
        onPair: (_e, cx) => this.chooseMenu("pair", cx),
        onReset: (_e, cx) => this.chooseMenu("reset", cx),
      });
    }
    if (this.overlayRole === "settings") {
      const view = settingsOverlay(t, {
        form: this.settingsForm,
        onClose: () => this.closeOverlaySelf(),
        onReload: (_e, cx) => overlay.loadSettings(this, cx),
        actions: {
          voice: (id, cx) => overlay.chooseVoiceProvider(this, id, cx),
          brainRow: (id, cx) => overlay.toggleBrainRow(this, id, cx),
          brainModel: (provider, model, cx) => overlay.chooseBrainModel(this, provider, model, cx),
          chattiness: (level, cx) => overlay.chooseChattiness(this, level, cx),
          toggleSection: (id, cx) => this.toggleSettingsSection(id, cx),
          languageAdd: (code, cx) => overlay.addLanguage(this, code, cx),
          languageRemove: (code, cx) => overlay.removeLanguage(this, code, cx),
          languageMove: (code, delta, cx) => overlay.moveLanguage(this, code, delta, cx),
          askRemove: (id, cx) => overlay.askRemoveDevice(this, id, cx),
          cancelRemove: (cx) => overlay.cancelRemoveDevice(this, cx),
          remove: (id, cx) => overlay.removeDevice(this, id, cx),
          openSetup: (cx) => overlay.openSetupPage(this, cx),
        },
      });
      return view.track_focus(this.overlayFocus).on_key_down((event, cx) => {
        if (event.key === "escape") {
          this.closeOverlaySelf();
          cx.stop_propagation();
        }
      });
    }
    if (this.overlayRole === "pair") {
      const view = pairOverlay(t, {
        form: this.pairForm,
        onClose: () => this.closeOverlaySelf(),
        onRefresh: (_e, cx) => overlay.loadPair(this, cx, true),
      });
      return view.track_focus(this.overlayFocus).on_key_down((event, cx) => {
        if (event.key === "escape") {
          this.closeOverlaySelf();
          cx.stop_propagation();
        }
      });
    }
    if (this.overlayRole === "agent") {
      const view = agentOverlay(t, {
        name: this.agentName,
        form: this.agentForm,
        onClose: () => this.closeOverlaySelf(),
      });
      return view.track_focus(this.overlayFocus).on_key_down((event, cx) => {
        if (event.key === "escape") {
          this.closeOverlaySelf();
          cx.stop_propagation();
        }
      });
    }
    if (this.overlayRole === "reset") {
      const view = resetOverlay(t, {
        ...this.resetForm,
        onClose: () => this.closeOverlaySelf(),
        onReset: (_e, cx) => overlay.resetConversation(this, cx),
      });
      return view.track_focus(this.overlayFocus).on_key_down((event, cx) => {
        if (event.key === "escape") {
          this.closeOverlaySelf();
          cx.stop_propagation();
        }
      });
    }
    const snapshot = this.store.getState();
    const status = deriveStatus(snapshot, this.host());
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
      const questionCards = this.answers.cards(snapshot.app);
      const questionHeights = questionCards.map((card) => estimateHeight(card.pending, { width: this.size.width }));
      const bannerHeights = [];
      if (this.offerDock) bannerHeights.push(150);
      if (status.phase === "setup") bannerHeights.push(150);
      if (status.paused && status.phase !== "down") bannerHeights.push(40);
      const voiceHeight = status.phase === "ok"
        ? (controls.action ? 130 : controls.useHere || controls.hint || controls.install || voice.notice ? 88 : 58)
        : 0;
      const composerHeight = derived.live ? 116 : 80;
      const budgetFor = (question) =>
        panelBudget({
          height: this.size.height,
          header: 54,
          banners: bannerHeights,
          voice: voiceHeight,
          question,
          composer: composerHeight,
        });
      const questionBudget = budgetFor(natural(questionHeights));
      const questions = this.questions(snapshot, questionBudget.questionArea);
      const layout = budgetFor(questions.presentation === "compact" ? COMPACT_QUESTION_HEIGHT : natural(questionHeights));
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
        compose: { state: this.composer, live: derived.live, notice: outbox.notice, busy: outbox.busy, compact: layout.compactBanners || layout.detailsScroll, maxHeight: composerHeight, onSend: (text, cx) => this.send(text, cx, false) },
        questions,
        voice: { controls, state: voice, on: this.voiceHandlers(controls) },
        layout,
        dock: this.offerDock ? { note: this.dockNote, onCopyLayout: (_e, cx) => this.copyLayout(cx) } : null,
        sheet: this.sheet?.type === "card" ? { ...this.sheet, note: this.focusNote, onFocus: (_e, cx) => this.focusAgent(this.sheet.card, cx), onClose: closeSheet } : this.sheet && { ...this.sheet, onClose: closeSheet },
        onSetup: (_e, cx) => this.runSetup(cx),
        onStart: (_e, cx) => this.startDaemon(cx),
        downNote: this.downNote ?? null,
        onToggleTechnical: (_e, cx) => {
          this.technical = !this.technical;
          cx.notify();
        },
        onMenu: () => this.openHeaderMenu(),
        onClose: this.overlay ? () => closeOverlay(this.overlay) : null,
        onEndShown: () => {
          this.wantOlder = true;
        },
        onOpenRow: (key, cx) => this.openRow(key, cx),
      })
        .on_size_change(onSize)
        .when(Boolean(this.panelFocus), (el) => el.track_focus(this.panelFocus).on_key_down((event, cx) => this.onKey(event, cx)));
    }
    return railView(t, status, { onOpen: () => this.openPanel() }).on_size_change(onSize);
  }
}
