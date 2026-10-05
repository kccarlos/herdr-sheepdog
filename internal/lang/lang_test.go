package lang

import (
	"strings"
	"testing"
)

// The catalog has unique codes, both Chinese scripts as their own
// entries next to English, and every entry names its providers.
func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, l := range Catalog {
		if seen[strings.ToLower(l.Code)] {
			t.Errorf("duplicate code %s", l.Code)
		}
		seen[strings.ToLower(l.Code)] = true
		if l.Name == "" || l.Native == "" || len(l.Providers) == 0 {
			t.Errorf("incomplete entry %+v", l)
		}
	}
	if len(Catalog) < 3 || Catalog[0].Code != "en" || Catalog[1].Code != "zh-Hans" || Catalog[2].Code != "zh-Hant" {
		t.Errorf("first entries = %+v", Catalog[:3])
	}
	if Catalog[1].Native != "简体中文" || Catalog[2].Native != "繁體中文" {
		t.Errorf("Chinese natives = %q %q", Catalog[1].Native, Catalog[2].Native)
	}
}

func TestNormalize(t *testing.T) {
	got, err := Normalize([]string{"ZH-hans", " en ", "zh-Hans", "ja"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "zh-Hans,en,ja" {
		t.Errorf("Normalize = %v", got)
	}
	if _, err := Normalize([]string{"en", "klingon"}); err == nil || !strings.Contains(err.Error(), "klingon") {
		t.Errorf("unknown code err = %v", err)
	}
	if got, err := Normalize([]string{}); err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty = %v %v, want a non-nil empty list", got, err)
	}
}

func TestLine(t *testing.T) {
	cases := []struct {
		codes []string
		want  string
	}{
		{nil, ""},
		{[]string{"nope"}, ""},
		{[]string{"en"}, "The shepherd's language setting, in order of preference: English. Answer in the language they use."},
		{[]string{"zh-Hans", "en"}, "The shepherd's language setting, in order of preference: Chinese (Simplified) 简体中文, English. Answer in the language they use; write Chinese in Simplified characters."},
		{[]string{"zh-Hant"}, "The shepherd's language setting, in order of preference: Chinese (Traditional) 繁體中文. Answer in the language they use; write Chinese in Traditional characters."},
		{[]string{"zh-Hant", "zh-Hans"}, "The shepherd's language setting, in order of preference: Chinese (Traditional) 繁體中文, Chinese (Simplified) 简体中文. Answer in the language they use; write Chinese in Traditional characters unless they ask for Simplified."},
		{[]string{"ja", "pt-BR"}, "The shepherd's language setting, in order of preference: Japanese (日本語), Portuguese (Brazil) Português (Brasil). Answer in the language they use."},
	}
	for _, c := range cases {
		if got := Line(c.codes); got != c.want {
			t.Errorf("Line(%v) =\n %q\nwant\n %q", c.codes, got, c.want)
		}
	}
}
