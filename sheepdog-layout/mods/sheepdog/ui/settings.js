// Settings, opened from the panel's header menu. It uses the same daemon
// routes as the phone's Settings: voice and orchestrator models, languages,
// chattiness, paired devices and the update notice. AI-service consent is
// phone-local today, so there is no daemon control to show here.

import { div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import {
  brainProviderGroups,
  languageChoices,
  SECTIONS,
  sectionOpen,
  sectionSummary,
  updateNotice,
  voiceProviderDetail,
} from "../model/settings.js";
import {
  choice,
  errorText,
  foldSection,
  mutedText,
  overlayButton,
  overlayFrame,
  smallButton,
} from "./overlay-widgets.js";

// What a section holds when it is open: its rows, and the help and error
// under them.
const body = (rows, { footer = "", error = "" } = {}) => ({ rows, footer, error });

// Which function builds each section of model/settings.js.
const BODIES = { voice: voiceBody, brain: brainBody, languages: languagesBody, replies: repliesBody, devices: devicesBody };

function updateBlock(t, update) {
  const notice = updateNotice(update);
  if (!notice) return null;
  return v_flex()
    .w_full()
    .gap(6)
    .p(10)
    .rounded(8)
    .border(1)
    .border_color(t.working)
    .bg(t.raised)
    .child(div().text_size(12).text_color(t.foreground).child(notice.text))
    .when(Boolean(notice.command), (el) => el.child(div().px(8).py(6).rounded(6).bg(t.input).font_family("monospace").text_size(11).selectable().child(notice.command)))
    .when(Boolean(notice.after), (el) => el.child(mutedText(t, notice.after)));
}

// A provider that cannot be chosen: greyed out with the daemon's reason, and a
// button that opens the daemon's setup page, where its key is added. The
// button sits outside the greyed part so it stays readable.
function unavailableRow(t, id, { title, detail, onSetup, busy }) {
  return h_flex()
    .id(id)
    .w_full()
    .min_h(36)
    .px(9)
    .py(6)
    .gap(8)
    .items_center()
    .rounded(7)
    .border(1)
    .border_color(t.border)
    .bg(t.raised)
    .child(
      v_flex()
        .flex_1()
        .min_w(0)
        .gap(1)
        .opacity(0.52)
        .child(div().text_size(12.5).text_color(t.muted).child(title))
        .when(Boolean(detail), (el) => el.child(div().text_size(11).text_color(t.muted).child(detail))),
    )
    .child(smallButton(t, `${id}-setup`, "Add a key in Setup", (_e, cx) => onSetup(cx), { disabled: Boolean(busy) }));
}

function voiceBody(t, form, actions) {
  const list = form.voice;
  const rows = [];
  if (!list) {
    rows.push(mutedText(t, form.errors.voice || "Loading voice models…"));
  } else if (!Array.isArray(list.providers) || list.providers.length === 0) {
    rows.push(mutedText(t, "This daemon offers no voice provider choices."));
  } else {
    for (const provider of list.providers) {
      const id = String(provider.id || "");
      if (provider.available === false) {
        rows.push(unavailableRow(t, `settings-voice-${id}`, { title: provider.label || id, detail: voiceProviderDetail(provider), onSetup: actions.openSetup }));
        continue;
      }
      rows.push(
        choice(t, `settings-voice-${id}`, {
          title: provider.label || id,
          detail: voiceProviderDetail(provider),
          selected: id === list.provider,
          disabled: Boolean(form.busy),
          onClick: (_e, cx) => actions.voice(id, cx),
        }),
      );
    }
  }
  return body(rows, {
    footer: "Which model the voice talks with. The conversation and your agents carry over.",
    error: form.errors.voice,
  });
}

// One provider, collapsed to its name and a summary (the chosen model, or
// whether it is available); pressing it lists only that provider's models.
function brainProviderRow(t, form, group, actions) {
  if (!group.available) {
    return unavailableRow(t, `settings-brain-provider-${group.id}`, { title: group.label, detail: group.summary, onSetup: actions.openSetup });
  }
  return h_flex()
    .id(`settings-brain-provider-${group.id}`)
    .w_full()
    .min_h(36)
    .px(9)
    .py(6)
    .gap(8)
    .items_center()
    .rounded(7)
    .bg(group.current ? t.selected : t.raised)
    .border(1)
    .border_color(group.current ? t.accent : t.border)
    .cursor_pointer()
    .hover((s) => s.bg(t.hover))
    .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
    .on_click((_e, cx) => actions.brainRow(group.id, cx))
    .child(div().w(12).flex_none().text_size(11).text_color(t.muted).child(group.expanded ? "▾" : "▸"))
    .child(
      v_flex()
        .flex_1()
        .min_w(0)
        .gap(1)
        .child(div().text_size(12.5).font_semibold().text_color(t.foreground).child(group.label))
        .child(div().text_size(11).text_color(group.current ? t.accent : t.muted).truncate().child(group.summary)),
    );
}

function brainBody(t, form, actions) {
  const brain = form.brain;
  const rows = [];
  if (!brain) {
    rows.push(mutedText(t, form.errors.brain || "Loading orchestrator models…"));
  } else {
    for (const group of brainProviderGroups(brain, form.brainCatalogs, form.brainOpen)) {
      rows.push(brainProviderRow(t, form, group, actions));
      if (group.loading) rows.push(div().pl(21).child(mutedText(t, "Loading models…")));
      if (group.error) rows.push(div().pl(21).child(mutedText(t, `No model list: ${group.error}`)));
      for (const model of group.models) {
        const name = String(model.name || "");
        rows.push(
          div().pl(21).w_full().child(
            choice(t, `settings-brain-model-${group.id}-${name}`, {
              title: model.display_name || name,
              detail: model.recommended ? "Recommended" : "",
              selected: model.selected,
              disabled: Boolean(form.busy),
              onClick: (_e, cx) => actions.brainModel(group.id, name, cx),
            }),
          ),
        );
      }
      if (group.expanded && group.models.length === 0 && !group.loading && !group.error) {
        rows.push(div().pl(21).child(mutedText(t, "No models were discovered for this provider.")));
      }
    }
    if (brain.error) rows.push(mutedText(t, `Model list not fresh: ${brain.error}`));
  }
  rows.push(
    h_flex()
      .w_full()
      .gap(8)
      .items_center()
      .child(div().flex_1().min_w(0).text_size(11).text_color(t.dim).child("API keys are added in the Setup page."))
      .child(smallButton(t, "settings-open-setup", "Open Setup", (_e, cx) => actions.openSetup(cx))),
  );
  return body(rows, {
    footer: "The model that decides what the voice says and does. It takes effect on the next turn.",
    error: form.errors.brain,
  });
}

function languagesBody(t, form, actions) {
  const settings = form.settings;
  if (!settings) {
    return body([mutedText(t, form.errors.settings || "Loading languages…")], { error: form.errors.settings });
  }
  if (!Array.isArray(settings.language_options) || settings.language_options.length === 0) {
    return body([mutedText(t, "This daemon does not offer language choices.")]);
  }
  const { chosen, others } = languageChoices(settings);
  const rows = [];
  if (chosen.length === 0) rows.push(mutedText(t, "None chosen: the voice follows whatever you speak."));
  chosen.forEach((option, index) => {
    rows.push(
      h_flex()
        .w_full()
        .min_h(36)
        .px(9)
        .gap(6)
        .items_center()
        .rounded(7)
        .bg(t.raised)
        .border(1)
        .border_color(t.border)
        .child(div().w(20).flex_none().text_size(11).text_color(t.dim).child(`${index + 1}.`))
        .child(
          v_flex()
            .flex_1()
            .min_w(0)
            .child(div().text_size(12.5).text_color(t.foreground).child(option.native || option.name || option.code))
            .when(Boolean(option.name && option.name !== option.native), (el) => el.child(div().text_size(10.5).text_color(t.muted).child(option.name))),
        )
        .child(smallButton(t, `language-up-${option.code}`, "↑", (_e, cx) => actions.languageMove(option.code, -1, cx), { disabled: index === 0 || Boolean(form.busy) }))
        .child(smallButton(t, `language-down-${option.code}`, "↓", (_e, cx) => actions.languageMove(option.code, 1, cx), { disabled: index === chosen.length - 1 || Boolean(form.busy) }))
        .child(smallButton(t, `language-remove-${option.code}`, "Remove", (_e, cx) => actions.languageRemove(option.code, cx), { disabled: Boolean(form.busy) })),
    );
  });
  if (others.length > 0) {
    rows.push(div().pt(5).text_size(11).font_semibold().text_color(t.dim).child("Add a language"));
    for (const option of others) {
      rows.push(
        choice(t, `language-add-${option.code}`, {
          title: `${option.native || option.name || option.code}${option.name && option.name !== option.native ? ` · ${option.name}` : ""}`,
          right: "Add",
          disabled: Boolean(form.busy),
          onClick: (_e, cx) => actions.languageAdd(option.code, cx),
        }),
      );
    }
  }
  return body(rows, {
    footer: "Most preferred first. The voice and orchestrator answer in the language you use.",
    error: form.errors.settings,
  });
}

function repliesBody(t, form, actions) {
  const settings = form.settings;
  const rows = [];
  if (!settings) {
    rows.push(mutedText(t, "Reply style is unavailable until the daemon's settings load."));
  } else {
    for (const level of settings.levels || []) {
      rows.push(
        choice(t, `settings-chattiness-${level}`, {
          title: String(level),
          selected: level === settings.chattiness,
          disabled: Boolean(form.busy),
          onClick: (_e, cx) => actions.chattiness(level, cx),
        }),
      );
    }
  }
  return body(rows, {
    footer: "How often Sheepdog speaks up about what your agents are doing.",
    error: settings ? "" : form.errors.settings,
  });
}

function devicesBody(t, form, actions) {
  const rows = [];
  if (form.errors.devices) rows.push(errorText(t, form.errors.devices));
  if (form.devices.length === 0 && !form.errors.devices) rows.push(mutedText(t, "No phones or browsers are paired."));
  for (const device of form.devices) {
    const id = String(device.id || "");
    const asking = form.deviceToRemove === id;
    rows.push(
      h_flex()
        .w_full()
        .min_h(38)
        .px(9)
        .py(6)
        .gap(8)
        .items_center()
        .rounded(7)
        .bg(t.raised)
        .border(1)
        .border_color(asking ? t.blocked : t.border)
        .child(
          v_flex()
            .flex_1()
            .min_w(0)
            .child(div().text_size(12.5).text_color(t.foreground).child(device.name || "Paired device"))
            .child(div().text_size(10.5).text_color(t.muted).child(`${device.kind || "device"}${device.last_seen ? ` · last seen ${device.last_seen}` : ""}`)),
        )
        .when(asking, (el) =>
          el
            .child(smallButton(t, `device-keep-${id}`, "Keep", (_e, cx) => actions.cancelRemove(cx)))
            .child(smallButton(t, `device-remove-confirm-${id}`, "Remove", (_e, cx) => actions.remove(id, cx), { disabled: Boolean(form.busy) })),
        )
        .when(!asking, (el) => el.child(smallButton(t, `device-unpair-${id}`, "Unpair", (_e, cx) => actions.askRemove(id, cx), { disabled: Boolean(form.busy) }))),
    );
  }
  return body(rows, {
    footer: "Removing a device takes away its access. You can pair it again with a new code.",
  });
}

export function settingsOverlay(t, { form, onClose, onReload, actions }) {
  const sections = [];
  const update = updateBlock(t, form.state?.update);
  if (update) sections.push(update);
  if (form.errors.state) sections.push(mutedText(t, `The update notice is unavailable: ${form.errors.state}`));
  if (form.loading) sections.push(mutedText(t, "Loading settings from Sheepdog…"));
  // Each section is folded to its title and current choice until it is
  // opened; a folded section builds none of its rows.
  for (const { id, title } of SECTIONS) {
    const open = sectionOpen(form, id);
    sections.push(
      foldSection(t, id, {
        title,
        summary: sectionSummary(form, id),
        open,
        onToggle: (_e, cx) => actions.toggleSection(id, cx),
        content: open ? BODIES[id](t, form, actions) : null,
      }),
    );
  }
  const body = v_flex()
    .id("sheepdog-settings")
    .flex_1()
    .min_h(0)
    .w_full()
    .overflow_y_scrollbar()
    .p(14)
    .gap(16)
    .when(Boolean(form.notice), (el) => el.child(div().text_size(11.5).text_color(t.done).child(form.notice)))
    .when(Boolean(form.alert), (el) => el.child(errorText(t, form.alert)))
    .children(sections);
  return overlayFrame(t, {
    id: "sheepdog-settings",
    title: "Settings",
    subtitle: "Saved on your computer",
    onClose,
    body,
    footer: h_flex()
      .w_full()
      .px(14)
      .py(8)
      .gap(8)
      .justify_end()
      .border_t(1)
      .border_color(t.border)
      .child(overlayButton(t, "settings-refresh", form.loading ? "Refreshing…" : "Refresh", onReload, { disabled: form.loading }))
      .child(overlayButton(t, "settings-done", "Done", onClose, { primary: true })),
  });
}
