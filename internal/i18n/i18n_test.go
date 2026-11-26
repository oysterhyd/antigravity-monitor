package i18n

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
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
	if got := en.SubagentSummary(0); got != en.SingleAgent {
		t.Fatalf("expected %q for 0 subagents, got %q", en.SingleAgent, got)
	}
	if got := en.SubagentSummary(2); got != "1 Main · 2 Sub" {
		t.Fatalf("expected '1 Main · 2 Sub', got %q", got)
	}
}

var fmtVerbRe = regexp.MustCompile(`%(?:\[\d+\])?[+#\-0]*(?:\d+|\*)?(?:\.(?:\d+|\*))?[vtbcdoqxXUeEfFgGsp]`)

func extractFmtVerbs(s string) []string {
	cleaned := strings.ReplaceAll(s, "%%", "")
	return fmtVerbRe.FindAllString(cleaned, -1)
}

func TestFormatVerbParity(t *testing.T) {
	zhV := reflect.ValueOf(zhStrings)
	enV := reflect.ValueOf(enStrings)
	typ := zhV.Type()

	for i := 0; i < zhV.NumField(); i++ {
		name := typ.Field(i).Name
		zf := zhV.Field(i)
		ef := enV.Field(i)
		switch zf.Kind() {
		case reflect.String:
			zv := extractFmtVerbs(zf.String())
			ev := extractFmtVerbs(ef.String())
			if !reflect.DeepEqual(zv, ev) {
				t.Errorf("format verb mismatch on %s: zh=%v (%q) vs en=%v (%q)", name, zv, zf.String(), ev, ef.String())
			}
		case reflect.Array:
			for j := 0; j < zf.Len(); j++ {
				zv := extractFmtVerbs(zf.Index(j).String())
				ev := extractFmtVerbs(ef.Index(j).String())
				if !reflect.DeepEqual(zv, ev) {
					t.Errorf("format verb mismatch on %s[%d]: zh=%v vs en=%v", name, j, zv, ev)
				}
			}
		}
	}
}

func TestNewStringsR1ToR4(t *testing.T) {
	for _, lang := range []string{LangZH, LangEN} {
		tr := Get(lang)
		v := reflect.ValueOf(tr)
		typ := v.Type()
		for i := 0; i < v.NumField(); i++ {
			name := typ.Field(i).Name
			if v.Field(i).Kind() != reflect.String {
				continue
			}
			val := v.Field(i).String()
			if strings.HasPrefix(val, "\n") || strings.HasSuffix(val, "\n") {
				t.Errorf("lang=%s field %s has leading/trailing newline: %q", lang, name, val)
			}
			upper := strings.ToUpper(val)
			if strings.Contains(upper, "TODO") || strings.Contains(upper, "TBD") {
				t.Errorf("lang=%s field %s contains placeholder text: %q", lang, name, val)
			}
		}

		// Verify all formatter templates render cleanly without fmt errors.
		formatted := []string{
			fmt.Sprintf(tr.HistorySubOutFmt, "2.9M"),
			fmt.Sprintf(tr.HistorySubDailyAvgFmt, "$6.12"),
			fmt.Sprintf(tr.HistorySubHourlyAvgFmt, "$1.36/h"),
			fmt.Sprintf(tr.HistorySubHitFmt, "94.2%"),
			fmt.Sprintf(tr.HistorySubCallsFmt, "1,846"),
			fmt.Sprintf(tr.HistoryInspTokensFmt, "8.7M", "0.4M", "0.3M"),
		}
		for idx, s := range formatted {
			if strings.Contains(s, "%!") {
				t.Fatalf("lang=%s formatted[%d] produced fmt error: %q", lang, idx, s)
			}
		}

		if tr.BackToLive == "" || tr.HistoryTitle == "" {
			t.Fatalf("lang=%s missing header titles", lang)
		}
	}
}
