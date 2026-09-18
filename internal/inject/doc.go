// Package inject is the debug-only fake event path of plan §3.8: it lets
// the /debug page (and tests) drive the whole supervisor chain without a
// real herdr or a microphone.
//
// Herdr decorates a supervisor.Herdr: panes registered with SetScreen are
// "synthetic" — Read answers from the stored screen text, Prompt and
// SendKeys are recorded as herdr.send events (with synthetic:true) instead
// of failing, and ListAgents appends them to the real list. With a nil
// inner client it is the whole mock herdr of `sheepdog serve --herdr=mock`.
//
// Injector marks a pane synthetic in the state table and feeds a
// herdr.Transition (Source = state.SourceInject) into the same
// Supervisor.HandleTransition path real transitions take, so the log shows
// the real chain (herdr.state → llm.call → pending.created → voice.push).
//
// Router is the text-only tool router used when the voice session is off:
// it maps a handful of trivial utterances straight onto voice tool calls
// ("yes" → confirm, "stop" → cancel, "what's going on" → briefing,
// "tell …" → propose, "ask …" → ask). It asks no model and understands
// nothing else; it exists so `simulate utterance` works with --voice=off.
package inject
