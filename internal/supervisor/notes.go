package supervisor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// NotesFile is the name of the notes log under the data dir.
const NotesFile = "notes.jsonl"

// appendNote records a note in the state session and, when a data dir is
// configured, appends it to notes.jsonl. Notes come from the brain's own
// note tool, whose result is already in the transcript, so the note is
// not ingested a second time.
func (s *Supervisor) appendNote(text string) error {
	n := state.Note{Text: text, At: s.cfg.Now()}
	if err := s.store.UpdateSession(func(ss *state.Session) { ss.Notes = append(ss.Notes, n) }); err != nil {
		return err
	}
	if s.cfg.DataDir != "" {
		if err := os.MkdirAll(s.cfg.DataDir, 0o700); err != nil {
			return fmt.Errorf("notes: %w", err)
		}
		f, err := os.OpenFile(filepath.Join(s.cfg.DataDir, NotesFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("notes: %w", err)
		}
		defer func() { _ = f.Close() }()
		line, err := json.Marshal(n)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("notes: %w", err)
		}
	}
	s.log.Log(logSource, "note.created", "", map[string]any{"text": text})
	return nil
}
