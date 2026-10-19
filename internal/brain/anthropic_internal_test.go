package brain

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// Every tool declaration converts to a schema strict tool use accepts:
// additionalProperties false on every object, no nullable keyword, and
// nothing outside the supported subset.
func TestStrictSchemaOverEveryTool(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		for _, tool := range anthropicTools(toolDeclarations(readOnly)) {
			data, err := json.Marshal(tool)
			if err != nil {
				t.Fatal(err)
			}
			var v map[string]any
			_ = json.Unmarshal(data, &v)
			if v["strict"] != true {
				t.Errorf("%v: not strict", v["name"])
			}
			checkStrict(t, v["name"].(string), v["input_schema"].(map[string]any))
		}
	}
}

func checkStrict(t *testing.T, path string, s map[string]any) {
	t.Helper()
	for k := range s {
		switch k {
		case "type", "description", "enum", "required", "properties", "items", "additionalProperties":
		default:
			t.Errorf("%s: unsupported keyword %q", path, k)
		}
	}
	if props, ok := s["properties"].(map[string]any); ok || s["type"] == "object" {
		if s["additionalProperties"] != false {
			t.Errorf("%s: object without additionalProperties false", path)
		}
		for name, p := range props {
			checkStrict(t, path+"."+name, p.(map[string]any))
		}
	}
	if items, ok := s["items"].(map[string]any); ok {
		checkStrict(t, path+"[]", items)
	}
}

func TestStrictSchemaNullable(t *testing.T) {
	yes := true
	got := strictSchema(schemaOf(&genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{
		"n": {Type: genai.TypeString, Nullable: &yes},
	}}))
	data, _ := json.Marshal(got)
	if !strings.Contains(string(data), `"n":{"type":["string","null"]}`) || strings.Contains(string(data), "nullable") {
		t.Errorf("schema = %s", data)
	}
}

func TestAnthropicToolID(t *testing.T) {
	if anthropicToolID("toolu_01AbC-d", "x") != "toolu_01AbC-d" {
		t.Error("an accepted id was changed")
	}
	a, b := anthropicToolID("bt 1/2", "read_screen"), anthropicToolID("bt 1/2", "read_screen")
	if a != b || !toolUseIDRE.MatchString(a) || a == anthropicToolID("bt 1/3", "read_screen") {
		t.Errorf("derived ids %q %q", a, b)
	}
	if anthropicToolID("", "recall") == anthropicToolID("", "read_screen") {
		t.Error("empty ids of different calls collide")
	}
}
