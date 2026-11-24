package i18n

import (
	"reflect"
	"testing"

	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

func TestAllStringsPopulated(t *testing.T) {
	for _, lang := range []string{LangZH, LangEN} {
		tr := Get(lang)
		v := reflect.ValueOf(tr)
		typ := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			name := typ.Field(i).Name
			switch f.Kind() {
			case reflect.String:
				if f.String() == "" {
					t.Errorf("lang=%s field %s is empty", lang, name)
				}
			case reflect.Array:
				for j := 0; j < f.Len(); j++ {
					if f.Index(j).String() == "" {
						t.Errorf("lang=%s field %s[%d] is empty", lang, name, j)
					}
				}
			}
		}
	}
}

func TestNormalizeAndHelpers(t *testing.T) {
	if got := Normalize("en-US"); got != LangEN {
		t.Fatalf("expected en, got %q", got)
	}
	if got := Normalize(""); got != LangZH {
		t.Fatalf("expected zh default, got %q", got)
	}

	en := Get(LangEN)
	if got := en.AgentName(model.RootAgentID, "主 Agent"); got != "Main Agent" {
		t.Fatalf("expected Main Agent, got %q", got)
	}
	if got := en.AgentName("sub-1", "Codebase Researcher"); got != "Codebase Researcher" {
		t.Fatalf("expected custom subagent preserved, got %q", got)
	}
	if got := en.SegmentLabel(model.ContextSegToolOutput); got != "Tool Out" {
		t.Fatalf("expected Tool Out, got %q", got)
	}
}
