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
import { closeOverlay, exec, home, openOverlay, props, run, shared, theme } from "herdr";
import { baseUrl, hostPort } from "./net/config.js";
import { createHttp } from "./net/http.js";
import { createLive, POLL_MS } from "./net/live.js";
import { agentDetails } from "./model/agents.js";
import { focusCommand } from "./model/cards.js";
import { keyMaker, restored, submission } from "./model/composer.js";
import { COMPACT_QUESTION_HEIGHT, DOCKED_WIDTH, layoutMode, needsDock, panelBudget } from "./model/layout.js";
import { createOutbox, pendingItems } from "./model/outbox.js";
import { amber } from "./model/palette.js";
import { decide, estimateHeight, natural } from "./model/presentation.js";
import { createAnswers } from "./model/questions.js";
import { liveSuggestions } from "./model/replies.js";
import {
  addLanguage,
  askToRemoveDevice,
  beginSettingsAction,
  cancelDeviceRemoval,
  defaultBrainModel,
  emptySettingsForm,
  finishSettingsAction,
  moveLanguage,
  removeLanguage,
  settingsFailed,
  settingsLoadFinished,
  settingsLoaded,
} from "./model/settings.js";
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
    this.size = this.overlay ? { width: WINDOW.width, height: WINDOW.height } : { width: DOCKED_WIDTH, height: 640 };
    this.technical = false;
    this.wantOlder = false; // the end of the list was drawn
    this.memo = null;
    const http = (this.http = createHttp({ fetch, base }));
    if (panelCopy) {
      this.store = createStore({ http, conversation: true });
      this.initPanel(http, cx);
      this.initLive(cx, base);
      cx.timer.every(250, (cx) => {
        this.pageBack(cx);
        // What has stayed long enough (a note, a settled card) goes.
        this.outbox.prune();
        this.answers.prune(this.store.getState().app);
        this.voice.prune();
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

  initSettings(cx) {
    this.settingsForm = emptySettingsForm();
    this.loadSettings(cx);
  }

  initPair(cx) {
    this.pairForm = { loading: true, error: "", link: "", matrix: null };
    this.loadPair(cx, false);
  }

  initAgent(name, cx) {
    this.agentName = String(name || "");
    this.agentForm = { loading: true, error: "", agent: null };
    this.loadAgent(cx);
  }

  initReset() {
    this.resetForm = { busy: false, error: "" };
    const newKey = keyMaker();
    this.newResetKey = newKey;
    this.resetKey = newKey();
  }

  setSettings(form, cx) {
    this.settingsForm = form;
    cx.notify();
  }

  words(error) {
    return error?.message || String(error || "It did not answer.");
  }

  async loadSettings(cx) {
    this.setSettings({ ...this.settingsForm, loading: true, notice: "" }, cx);
    const [state, voice, brain, settings, devices] = await Promise.allSettled([
      this.http.state(),
      this.http.voiceProviders(),
      this.http.brainModels(),
      this.http.settings(),
      this.http.devices(),
    ]);
    let form = this.settingsForm;
    for (const [part, result] of [["state", state], ["voice", voice], ["brain", brain], ["settings", settings], ["devices", devices]]) {
      form = result.status === "fulfilled"
        ? settingsLoaded(form, part, result.value)
        : settingsFailed(form, part, this.words(result.reason));
    }
    this.setSettings(settingsLoadFinished(form), cx);
  }

  async chooseVoiceProvider(id, cx) {
    if (this.settingsForm.busy || id === this.settingsForm.voice?.provider) return;
    this.setSettings(beginSettingsAction(this.settingsForm, "voice"), cx);
    try {
      const voice = await this.http.setVoiceProvider(id);
      let form = finishSettingsAction(this.settingsForm, "voice", { notice: "Voice model changed." });
      form = settingsLoaded(form, "voice", voice);
      this.setSettings(form, cx);
    } catch (error) {
      this.setSettings(finishSettingsAction(this.settingsForm, "voice", { error: this.words(error) }), cx);
    }
  }

  async chooseBrainProvider(id, cx) {
    if (this.settingsForm.busy || id === this.settingsForm.brain?.provider) return;
    this.setSettings(beginSettingsAction(this.settingsForm, "brain"), cx);
    try {
      const catalog = await this.http.brainModels(id);
      const model = defaultBrainModel(catalog, id);
      if (!model) {
        throw new Error(`No model list for ${id}${catalog.error ? `: ${catalog.error}` : ""} — the orchestrator was left as it is.`);
      }
      const brain = await this.http.setBrainModel(id, model);
      let form = finishSettingsAction(this.settingsForm, "brain", { notice: "Orchestrator changed." });
      form = settingsLoaded(form, "brain", brain);
      this.setSettings(form, cx);
    } catch (error) {
      this.setSettings(finishSettingsAction(this.settingsForm, "brain", { error: this.words(error) }), cx);
    }
  }

  async chooseBrainModel(provider, model, cx) {
    if (this.settingsForm.busy || model === this.settingsForm.brain?.model) return;
    this.setSettings(beginSettingsAction(this.settingsForm, "brain"), cx);
    try {
      const brain = await this.http.setBrainModel(provider, model);
      let form = finishSettingsAction(this.settingsForm, "brain", { notice: "Orchestrator changed." });
      form = settingsLoaded(form, "brain", brain);
      this.setSettings(form, cx);
    } catch (error) {
      this.setSettings(finishSettingsAction(this.settingsForm, "brain", { error: this.words(error) }), cx);
    }
  }

  async chooseChattiness(level, cx) {
    if (this.settingsForm.busy || level === this.settingsForm.settings?.chattiness) return;
    this.setSettings(beginSettingsAction(this.settingsForm, "settings"), cx);
    try {
      const settings = await this.http.setSettings({ chattiness: level });
      let form = finishSettingsAction(this.settingsForm, "settings", { notice: "Reply style changed." });
      form = settingsLoaded(form, "settings", settings);
      this.setSettings(form, cx);
    } catch (error) {
      this.setSettings(finishSettingsAction(this.settingsForm, "settings", { error: this.words(error) }), cx);
    }
  }

  async setLanguages(codes, cx) {
    if (this.settingsForm.busy) return;
    this.setSettings(beginSettingsAction(this.settingsForm, "settings"), cx);
    try {
      const settings = await this.http.setSettings({ languages: codes });
      let form = finishSettingsAction(this.settingsForm, "settings", { notice: "Languages changed." });
      form = settingsLoaded(form, "settings", settings);
      this.setSettings(form, cx);
    } catch (error) {
      this.setSettings(finishSettingsAction(this.settingsForm, "settings", { error: this.words(error) }), cx);
    }
  }

  addLanguage(code, cx) {
    const codes = this.settingsForm.settings?.languages || [];
    this.setLanguages(addLanguage(codes, code), cx);
  }

  removeLanguage(code, cx) {
    const codes = this.settingsForm.settings?.languages || [];
    this.setLanguages(removeLanguage(codes, code), cx);
  }

  moveLanguage(code, delta, cx) {
    const codes = this.settingsForm.settings?.languages || [];
    this.setLanguages(moveLanguage(codes, code, delta), cx);
  }

  askRemoveDevice(id, cx) {
    this.setSettings(askToRemoveDevice(this.settingsForm, id), cx);
  }

  cancelRemoveDevice(cx) {
    this.setSettings(cancelDeviceRemoval(this.settingsForm), cx);
  }

  async removeDevice(id, cx) {
    if (this.settingsForm.busy) return;
    const name = this.settingsForm.devices.find((device) => device.id === id)?.name || "The device";
    this.setSettings(beginSettingsAction(this.settingsForm, "devices"), cx);
    try {
      await this.http.removeDevice(id);
      let form = finishSettingsAction(this.settingsForm, "devices", { notice: `${name} was unpaired.` });
      form = settingsLoaded(form, "devices", this.settingsForm.devices.filter((device) => device.id !== id));
      this.setSettings(cancelDeviceRemoval(form), cx);
    } catch (error) {
      this.setSettings(finishSettingsAction(this.settingsForm, "devices", { error: this.words(error) }), cx);
    }
  }

  async loadPair(cx, fresh) {
    this.pairForm = { ...this.pairForm, loading: true, error: "" };
    cx.notify();
    try {
      // The first request may mint the code; the matrix request reuses it.
      const pair = await this.http.pair(fresh);
      const matrix = await this.http.pairQR();
      this.pairForm = { loading: false, error: "", link: String(pair.link || ""), matrix };
    } catch (error) {
      this.pairForm = { loading: false, error: this.words(error), link: "", matrix: null };
    }
    cx.notify();
  }

  async loadAgent(cx) {
    try {
      const agents = await this.http.agents();
      this.agentForm = { loading: false, error: "", agent: agentDetails(agents, this.agentName) };
    } catch (error) {
      this.agentForm = { loading: false, error: this.words(error), agent: null };
    }
    cx.notify();
  }

  async resetConversation(cx) {
    if (this.resetForm.busy) return;
    this.resetForm = { busy: true, error: "" };
    cx.notify();
    let answered = false;
    try {
      await this.http.reset(this.resetKey);
      answered = true;
      closeOverlay(props().overlay);
      return;
    } catch (error) {
      answered = error?.kind !== "down";
      this.resetForm = { busy: false, error: this.words(error) };
    }
    if (answered) this.resetKey = this.newResetKey();
    cx.notify();
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
        onReload: (_e, cx) => this.loadSettings(cx),
        actions: {
          voice: (id, cx) => this.chooseVoiceProvider(id, cx),
          brainProvider: (id, cx) => this.chooseBrainProvider(id, cx),
          brainModel: (provider, model, cx) => this.chooseBrainModel(provider, model, cx),
          chattiness: (level, cx) => this.chooseChattiness(level, cx),
          languageAdd: (code, cx) => this.addLanguage(code, cx),
          languageRemove: (code, cx) => this.removeLanguage(code, cx),
          languageMove: (code, delta, cx) => this.moveLanguage(code, delta, cx),
          askRemove: (id, cx) => this.askRemoveDevice(id, cx),
          cancelRemove: (cx) => this.cancelRemoveDevice(cx),
          remove: (id, cx) => this.removeDevice(id, cx),
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
        onRefresh: (_e, cx) => this.loadPair(cx, true),
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
        onReset: (_e, cx) => this.resetConversation(cx),
      });
      return view.track_focus(this.overlayFocus).on_key_down((event, cx) => {
        if (event.key === "escape") {
          this.closeOverlaySelf();
          cx.stop_propagation();
        }
      });
    }
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
      const questionCards = this.answers.cards(snapshot.app);
      const questionHeights = questionCards.map((card) => estimateHeight(card.pending, { width: this.size.width }));
      const bannerHeights = [];
      if (this.offerDock) bannerHeights.push(150);
      if (status.phase === "setup") bannerHeights.push(150);
      if (status.paused && status.phase !== "down") bannerHeights.push(40);
      const voiceHeight = status.phase === "ok"
        ? (controls.useHere || controls.hint || controls.install || voice.notice ? 88 : 58)
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
        onSetup: () => this.runSetup(),
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
      }).on_size_change(onSize);
    }
    return railView(t, status, { onOpen: () => this.openPanel() }).on_size_change(onSize);
  }
}
