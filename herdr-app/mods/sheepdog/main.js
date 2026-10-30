// Sheepdog: the conversation with your agents, from the sheepdog daemon on this
// computer. Docked, this mod is the rail (the voice, who needs you); opened as
// an overlay it is the panel (a window with the read-only timeline). It reads
// the daemon's HTTP API and its /events stream and sends nothing; it never
// opens /ws, which would take the voice away from the phone. Only this file
// and ui/ import "herdr"; net/ and model/ run under node, so they are tested
// there (docs/dev/design/desktop-client.md, section 6).

import { View } from "gpui-kit";
import { connect } from "net";
import { closeOverlay, openOverlay, props, run, shared, theme } from "herdr";
import { baseUrl, hostPort } from "./net/config.js";
import { createHttp } from "./net/http.js";
import { createLive, POLL_MS } from "./net/live.js";
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
    // The rail has no use for the timeline, so it never asks for it.
    this.store = createStore({ http: createHttp({ fetch, base }), conversation: this.isPanel });
    // The data layer is a task: it keeps asking until close().
    cx.spawn(async (cx) => {
      this.store.subscribe(() => cx.notify());
      this.live = createLive({
        store: this.store,
        connect,
        endpoint: hostPort(base),
        clock: { now: () => Date.now(), sleep: (ms) => cx.sleep(ms) },
      });
      await this.live.run();
    });
    if (this.isPanel) cx.timer.every(250, (cx) => this.pageBack(cx));
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

  // The list's entries for the history as it is, laid out once per change.
  entries() {
    const { history } = this.store.getState();
    if (this.memo?.history !== history || this.memo.technical !== this.technical) {
      this.memo = { history, technical: this.technical, entries: layoutEntries(history.items, { showTechnical: this.technical }) };
    }
    return this.memo.entries;
  }

  async close() {
    this.live?.stop();
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
      return panelView(t, {
        status,
        entries: status.phase === "down" ? [] : this.entries(),
        paging: { synced: snapshot.history.synced, canLoadOlder: snapshot.history.nextBefore != null, loadingOlder: snapshot.loadingOlder },
        technical: this.technical,
        retryMs: POLL_MS,
        onSetup: () => this.runSetup(),
        onToggleTechnical: (_e, cx) => {
          this.technical = !this.technical;
          cx.notify();
        },
        onClose: () => closeOverlay(props().overlay),
        onEndShown: () => {
          this.wantOlder = true;
        },
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
