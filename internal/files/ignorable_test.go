package files

import "testing"

// Default-ignorable code points do not make a sensitive name ordinary.
func TestSensitiveIgnorable(t *testing.T) {
	for _, n := range []string{
		".\u200benv", ".\ufeffgit", ".e\u00adnv", ".en\u034fv", ".s\u2060sh",
		".\ufe0fenv", ".aws\u200d", "id_\u200crsa", "k.p\u00adem",
	} {
		if !Sensitive(n) {
			t.Errorf("Sensitive(%q) = false", n)
		}
	}
	if Sensitive("notes.txt") {
		t.Error("notes.txt is sensitive")
	}
}
