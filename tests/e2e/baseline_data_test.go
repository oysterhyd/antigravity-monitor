package e2e_test

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/config"
	"github.com/oysterhyd/antigravity-monitor/internal/i18n"
	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/source"
)

func TestBaseline_FixtureSelfCheckAndProtobufRoundtrip(t *testing.T) {
	ts := anchorNow.Add(-15 * time.Minute)
	blob := encodeGenMetadataBlob(genSpec{
		GenIndex:         4,
		StepIndices:      []int{12, 13},
		LastStepIndex:    11,
		RequestID:        "req-42",
		CascadeID:        "conv-root",
		SubagentType:     "research",
		ModelSlug:        "gemini-4-neon",
		ModelDisplay:     "Gemini 4 Neon (High)",
		InputTokens:      9690,
		CacheReadTokens:  33325,
		CacheWriteTokens: 500,
		OutputTokens:     1220,
		ThinkingTokens:   579,
		ResponseTokens:   641,
		ContextUsed:      44235,
		ContextLimit:     512000,
		TTFT:             2*time.Second + 250*time.Millisecond,
		DecodeDuration:   5*time.Second + 500*time.Millisecond,
		ExtraLargeBlob:   true,
	})

	gm, err := source.DecodeGenMetadata(4, blob)
	if err != nil {
		t.Fatalf("DecodeGenMetadata: %v", err)
	}
	if gm.StepIndex != 12 || gm.RequestID != "req-42" || gm.SubagentType != "research" {
		t.Fatalf("unexpected decoded metadata: %+v", gm)
	}
	if gm.InputTokens != 9690 || gm.CacheReadTokens != 33325 || gm.CacheWriteTokens != 500 || gm.OutputTokens != 1220 {
		t.Fatalf("unexpected token counts: %+v", gm)
	}
	if gm.TotalDuration != 7*time.Second+750*time.Millisecond {
		t.Fatalf("TotalDuration = %v, want 7.75s", gm.TotalDuration)
	}

	stepMeta := encodeStepMetadataTimestamp(ts)
	gotTS := source.DecodeStepTimestamp(stepMeta)
	if !gotTS.Equal(ts) {
		t.Fatalf("DecodeStepTimestamp = %v, want %v", gotTS, ts)
	}
}

func TestBaseline_SourceWALAndOrphanSubagents(t *testing.T) {
	fx := seedBaselineMultiRangeFixture(t)

	// Confirm WAL file exists and has non-zero size before reading.
	walPath := filepath.Join(fx.RootDir, "conversations", "conv-today-active.db-wal")
	info, err := os.Stat(walPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("expected active non-empty WAL file %s: err=%v info=%v", walPath, err, info)
	}

	prov, err := source.NewProvider(source.Options{
		RootDir:        fx.RootDir,
		MaxOpenDBs:     3,
		DisableWatcher: true,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	snap := prov.Snapshot()
	if snap.RunningCount != 1 {
		t.Fatalf("RunningCount = %d, want 1", snap.RunningCount)
	}

	cToday, ok := snap.FindConversation("conv-today-active")
	if !ok {
		t.Fatalf("expected conv-today-active in snapshot")
	}
	// 2 valid root rows (empty failure skipped) + 1 orphan subagent row = 3 requests.
	if cToday.TreeMetrics.RequestCount != 3 {
		t.Fatalf("conv-today-active TreeMetrics.RequestCount = %d, want 3", cToday.TreeMetrics.RequestCount)
	}
	if len(cToday.Subagents) != 1 || cToday.Subagents[0].ID != "orphan-sub-1" {
		t.Fatalf("expected orphan-sub-1 attached via conversation_parents, got %+v", cToday.Subagents)
	}

	// Append another uncheckpointed WAL row and verify incremental refresh picks it up.
	fx.AppendWALGenRow(t, "conv-today-active", genSpec{
		GenIndex:        3,
		StepIndices:     []int{4},
		LastStepIndex:   3,
		ModelSlug:       "gemini-4-neon",
		ModelDisplay:    "Gemini 4 Neon (High)",
		InputTokens:     1000,
		CacheReadTokens: 9000,
		OutputTokens:    400,
		ContextUsed:     10400,
		ContextLimit:    512000,
		TTFT:            800 * time.Millisecond,
		DecodeDuration:  1200 * time.Millisecond,
		Timestamp:       anchorNow.Add(-5 * time.Minute),
	})
	if err := prov.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh after WAL append: %v", err)
	}
	cAfter, ok := prov.Snapshot().FindConversation("conv-today-active")
	if !ok || cAfter.TreeMetrics.RequestCount != 4 {
		t.Fatalf("expected 4 requests after WAL append, got %d", cAfter.TreeMetrics.RequestCount)
	}
}

func TestBaseline_ProtobufEmptyFailureAndCorruptRows(t *testing.T) {
	failBlob := encodeGenMetadataBlob(genSpec{
		GenIndex:     0,
		Error:        "canceled",
		ModelDisplay: "Claude 4.5 Sonnet",
	})
	gm, err := source.DecodeGenMetadata(0, failBlob)
	if err != nil {
		t.Fatalf("DecodeGenMetadata empty failure: %v", err)
	}
	if !gm.IsEmptyFailure() {
		t.Fatalf("expected IsEmptyFailure() == true")
	}

	fx := newAntigravityFixture(t)
	fx.InsertSummary(t, summarySpec{
		ConversationID:   "conv-corrupt-mix",
		Title:            "Corrupt Resilience Test",
		StepCount:        3,
		LastModifiedTime: anchorNow,
		Status:           "CASCADE_RUN_STATUS_IDLE",
	})
	// Row 0: empty failure; Row 1: corrupt protobuf bytes; Row 2: valid telemetry.
	fx.AppendWALGenRow(t, "conv-corrupt-mix", genSpec{
		GenIndex:    0,
		StepIndices: []int{1},
		Error:       "canceled",
	})
	fx.AppendRawGenRow(t, "conv-corrupt-mix", 1, []byte{0xff, 0xff, 0x00, 0x01})
	fx.AppendWALGenRow(t, "conv-corrupt-mix", genSpec{
		GenIndex:        2,
		StepIndices:     []int{2},
		ModelSlug:       "gemini-4-flash",
		ModelDisplay:    "Gemini 4 Flash",
		InputTokens:     2000,
		CacheReadTokens: 8000,
		OutputTokens:    500,
		ContextUsed:     10500,
		ContextLimit:    512000,
		TTFT:            500 * time.Millisecond,
		DecodeDuration:  1000 * time.Millisecond,
		Timestamp:       anchorNow.Add(-1 * time.Minute),
	})

	prov, err := source.NewProvider(source.Options{
		RootDir:        fx.RootDir,
		DisableWatcher: true,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	c, ok := prov.Snapshot().FindConversation("conv-corrupt-mix")
	if !ok || c.TreeMetrics.RequestCount != 1 {
		t.Fatalf("expected 1 valid request despite empty failure & corrupt row, got ok=%v count=%d", ok, c.TreeMetrics.RequestCount)
	}
}

func TestBaseline_PricingLongContextAndCacheSavings(t *testing.T) {
	calc, err := metrics.NewDefaultCalculator()
	if err != nil {
		t.Fatalf("NewDefaultCalculator: %v", err)
	}

	recStd := model.RequestRecord{
		ModelSlug:       "gemini-4-neon",
		InputTokens:     10000,
		CacheReadTokens: 90000,
		OutputTokens:    2000,
		ContextUsed:     102000, // <= 200k standard tier
		TTFT:            1 * time.Second,
		TotalDuration:   3 * time.Second,
	}
	calc.EnrichRecord(&recStd)

	recLong := model.RequestRecord{
		ModelSlug:       "gemini-4-neon",
		InputTokens:     10000,
		CacheReadTokens: 90000,
		OutputTokens:    2000,
		ContextUsed:     245000, // > 200k long-context tier
		TTFT:            1 * time.Second,
		TotalDuration:   3 * time.Second,
	}
	calc.EnrichRecord(&recLong)

	if recLong.CostUSD <= recStd.CostUSD {
		t.Fatalf("expected >200k long-context cost (%f) > standard cost (%f)", recLong.CostUSD, recStd.CostUSD)
	}
	if recStd.CacheSavedUSD <= 0 || math.Abs(recStd.CacheHitRate-0.9) > 1e-6 {
		t.Fatalf("unexpected cache savings/rate: saved=%f hit=%f", recStd.CacheSavedUSD, recStd.CacheHitRate)
	}
	if math.Abs(recStd.DecodeTPS-1000.0) > 1e-6 {
		t.Fatalf("DecodeTPS = %f, want 1000.0", recStd.DecodeTPS)
	}

	// Unknown model slug falls back cleanly to DefaultRule.
	unknown := model.RequestRecord{
		ModelSlug:    "custom-unknown-model",
		InputTokens:  1000,
		OutputTokens: 500,
	}
	calc.EnrichRecord(&unknown)
	if unknown.CostUSD <= 0 {
		t.Fatalf("expected non-zero fallback cost for unknown model, got %f", unknown.CostUSD)
	}
}

func TestBaseline_ConfigAtomicSaveAndLocaleNormalization(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nested", "settings.json")

	st, err := config.NewStore(cfgPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	saved, err := st.Update(func(s *model.Settings) {
		s.ThemeMode = "DARK"
		s.Language = "en-US"
		s.ContextWarnThreshold = 0 // intentionally disabled
		s.AlwaysOnTop = true
	})
	if err != nil {
		t.Fatalf("Store.Update: %v", err)
	}
	if saved.ThemeMode != "dark" || saved.Language != "en" || saved.ContextWarnThreshold != 0 {
		t.Fatalf("unexpected normalized settings: %+v", saved)
	}

	st2, err := config.NewStore(cfgPath)
	if err != nil {
		t.Fatalf("reload NewStore: %v", err)
	}
	got := st2.Get()
	if got != saved {
		t.Fatalf("reloaded settings mismatch: got %+v, want %+v", got, saved)
	}

	if i18n.Normalize("fr-FR") != i18n.LangZH || i18n.Normalize("") != i18n.LangZH {
		t.Fatalf("expected unknown/empty locales to normalize to zh")
	}
	if i18n.Normalize("EN") != i18n.LangEN || i18n.Normalize("en_us") != i18n.LangEN {
		t.Fatalf("expected EN variants to normalize to en")
	}
	zh := i18n.Get("zh")
	en := i18n.Get("en")
	if zh.Settings == "" || en.Settings == "" || zh.Settings == en.Settings {
		t.Fatalf("expected distinct non-empty localized Settings labels: zh=%q en=%q", zh.Settings, en.Settings)
	}
}
