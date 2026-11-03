// Package attention decides which agents want a person's eyes: it classifies
// an agent into Needs you, Working or Done with an unread and a pinned mark
// (Classify), orders the agent list (Compare), and keeps the marks of a
// backend that has no labels of its own (Local).
//
// The marks themselves are herdr.Marks, read and written through
// herdr.Marker. herdr-core keeps them in terminal labels that every Herdr
// client shares; the old herdr has no labels and no way to record "seen"
// short of focusing a pane in the user's own window, which sheepdog never
// does, so Local keeps them in <data_dir>/agent_marks.json.
//
// The phone and the herdr-app panel implement the same Classify and Compare
// in Swift and JavaScript. ios/Tests/Fixtures/agent-attention.json holds the
// cases all three read.
package attention
