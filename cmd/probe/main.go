package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/source"
)

func main() {
	var (
		rootDir     = flag.String("root", source.DefaultRootDir(), "Antigravity data directory")
		pricingPath = flag.String("pricing", "", "Optional pricing override JSON file")
		limit       = flag.Int("limit", 5, "Maximum top-level conversations to display")
		recentReqs  = flag.Int("reqs", 5, "Number of recent requests to display per conversation")
		convFilter  = flag.String("conv", "", "Filter to conversation ID (or prefix)")
		watch       = flag.Bool("watch", false, "Stream live snapshot updates as local DBs/transcripts change")
		verify      = flag.Bool("verify", false, "Cross-verify L2 protobuf tokens against L3 transcript.jsonl")
		asJSON      = flag.Bool("json", false, "Output raw AppSnapshot JSON")
	)
	flag.Parse()

	maxHydrated := *limit
	if maxHydrated < 8 {
		maxHydrated = 8
	}
	maxConvs := max(20, maxHydrated)
	if *convFilter != "" {
		maxConvs = 200
		maxHydrated = 200
	}

	opts := source.Options{
		RootDir:             *rootDir,
		PricingOverridePath: *pricingPath,
		MaxOpenDBs:          5,
		MaxConversations:    maxConvs,
		MaxHydratedTrees:    maxHydrated,
		DisableWatcher:      !*watch,
	}

	provider, err := source.NewProvider(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: failed to initialize provider: %v\n", err)
		os.Exit(1)
	}
	defer provider.Close()

	if !*watch {
		snap := provider.Snapshot()
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(snap)
			return
		}
		renderSnapshot(snap, *limit, *recentReqs, *convFilter, provider.OpenConvDBHandles())
		if *verify {
			fmt.Println()
			runTranscriptVerification(*rootDir, snap, *convFilter)
		}
		return
	}

	// Watch mode: stream live updates via SnapshotProvider.Subscribe().
	ch, cancel := provider.Subscribe()
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	fmt.Printf("Watching %s (Ctrl-C to exit)...\n\n", *rootDir)
	var lastSig string
	for {
		select {
		case <-sigCh:
			fmt.Println("\nStopped.")
			return
		case snap, ok := <-ch:
			if !ok {
				return
			}
			sig := snapshotSignature(snap)
			if sig == lastSig {
				continue
			}
			lastSig = sig
			if *asJSON {
				b, _ := json.Marshal(snap)
				fmt.Println(string(b))
				continue
			}
			fmt.Printf("\n==================== [%s] UPDATE ====================\n", snap.GeneratedAt.Local().Format("15:04:05.000"))
			renderSnapshot(snap, *limit, *recentReqs, *convFilter, provider.OpenConvDBHandles())
		}
	}
}

func renderSnapshot(snap model.AppSnapshot, limit, recentReqs int, convFilter string, openHandles int) {
	fmt.Printf("Antigravity Local Probe | %s | Conversations: %d | Running: %d | Open L2 DBs: %d\n",
		snap.GeneratedAt.Local().Format("2006-01-02 15:04:05"),
		len(snap.Conversations),
		snap.RunningCount,
		openHandles,
	)
	if snap.ActiveConversationID != "" {
		fmt.Printf("Active Conversation: %s\n", snap.ActiveConversationID)
	}
	fmt.Println(strings.Repeat("-", 96))

	shown := 0
	for _, conv := range snap.Conversations {
		if convFilter != "" && !strings.HasPrefix(strings.ToLower(conv.ID), strings.ToLower(convFilter)) {
			continue
		}
		if limit > 0 && shown >= limit {
			break
		}
		shown++
		renderConversation(conv, recentReqs)
	}

	if shown == 0 {
		fmt.Println("No matching conversations found.")
	}
}

func renderConversation(c model.ConversationSnapshot, recentReqs int) {
	stateTag := "IDLE"
	if c.Running {
		stateTag = "RUNNING"
	}
	fmt.Printf("\n[%s] %s (%s)\n", stateTag, c.Title, shortID(c.ID))
	if c.Workspace != "" {
		fmt.Printf("  Workspace : %s | Updated: %s | Active Agent: %s\n",
			c.Workspace, c.LastUpdated.Local().Format("2006-01-02 15:04:05"), c.ActiveAgentID)
	} else {
		fmt.Printf("  Updated   : %s | Active Agent: %s\n",
			c.LastUpdated.Local().Format("2006-01-02 15:04:05"), c.ActiveAgentID)
	}

	tm := c.TreeMetrics
	fmt.Printf("  Tree Summary : Reqs=%d (Turn=%d) | Cost: Turn $%.4f / Session $%.4f (Root $%.4f / Sub $%.4f) | Saved $%.4f\n",
		tm.RequestCount, tm.TurnRequestCount,
		tm.TurnCostUSD, tm.SessionCostUSD,
		c.Root.Metrics.SessionCostUSD, c.SubagentsCostUSD,
		tm.CacheSavedUSD,
	)
	fmt.Printf("  Tree Perf    : CacheHit=%.1f%% | AvgTTFT=%s | PureDecodeTPS=%.1f t/s | PeakCtx=%.1f%% (%s / %s @ %s)\n",
		tm.WeightedCacheHitRate*100,
		fmtDur(tm.AvgTTFT),
		tm.WeightedDecodeTPS,
		c.PeakContext.Ratio*100,
		fmtTokens(c.PeakContext.Used),
		fmtTokens(c.PeakContext.Limit),
		c.PeakContext.AgentName,
	)

	fmt.Println("  Agent Tree:")
	renderAgentLine("  ├─", c.Root)
	for i, sub := range c.Subagents {
		prefix := "  │  ├─"
		if i == len(c.Subagents)-1 {
			prefix = "  │  └─"
		}
		renderAgentLine(prefix, sub)
	}

	if recentReqs > 0 && len(c.Requests) > 0 {
		start := len(c.Requests) - recentReqs
		if start < 0 {
			start = 0
		}
		fmt.Printf("  Recent %d Requests:\n", len(c.Requests)-start)
		for _, r := range c.Requests[start:] {
			turnMark := " "
			if r.InCurrentTurn {
				turnMark = "*"
			}
			fmt.Printf("    %s#%-3d step=%-4d %-20s | %-26s | in=%-6s cr=%-7s out=%-5s | hit=%5.1f%% | ttft=%-6s | tps=%6.1f | $%.4f\n",
				turnMark,
				r.Seq,
				r.StepIndex,
				truncate(r.AgentName, 20),
				truncate(r.ModelDisplay, 26),
				fmtTokens(r.InputTokens),
				fmtTokens(r.CacheReadTokens),
				fmtTokens(r.OutputTokens),
				r.CacheHitRate*100,
				fmtDur(r.TTFT),
				r.DecodeTPS,
				r.CostUSD,
			)
		}
	}
}

func renderAgentLine(prefix string, a model.AgentNode) {
	act := " "
	if a.Active {
		act = "●"
	}
	modelName := a.ModelDisplay
	if modelName == "" {
		modelName = "-"
	}
	m := a.Metrics
	fmt.Printf("%s %s %-22s [%-24s] Ctx %5.1f%% (%6s/%6s) | Reqs=%-3d | $%.4f | Hit %5.1f%% | TTFT %-6s | TPS %5.1f\n",
		prefix,
		act,
		truncate(a.Name, 22),
		truncate(modelName, 24),
		a.ContextRatio*100,
		fmtTokens(a.ContextUsed),
		fmtTokens(a.ContextLimit),
		m.RequestCount,
		m.SessionCostUSD,
		m.WeightedCacheHitRate*100,
		fmtDur(m.AvgTTFT),
		m.WeightedDecodeTPS,
	)
}

func runTranscriptVerification(rootDir string, snap model.AppSnapshot, convFilter string) {
	fmt.Println("=== L2 Protobuf vs L3 transcript.jsonl Token Alignment Verification ===")
	var (
		checkedConvs int
		matchedReqs  int
		mismatches   int
		l2Only       int
	)
	for _, conv := range snap.Conversations {
		if convFilter != "" && !strings.HasPrefix(strings.ToLower(conv.ID), strings.ToLower(convFilter)) {
			continue
		}
		if len(conv.Requests) == 0 {
			continue
		}
		// Group requests by their underlying conversation DB ID (root or subagent).
		byConvID := make(map[string][]model.RequestRecord)
		for _, r := range conv.Requests {
			byConvID[r.ConversationID] = append(byConvID[r.ConversationID], r)
		}
		for cid, reqs := range byConvID {
			tp := source.TranscriptPath(rootDir, cid)
			tailer := source.NewTranscriptTailer(tp)
			if _, err := tailer.Sync(); err != nil {
				continue
			}
			td := tailer.Snapshot()
			if len(td.PlannersByStep) == 0 {
				continue
			}
			checkedConvs++
			for _, r := range reqs {
				pl, ok := td.PlannersByStep[r.StepIndex]
				if !ok {
					l2Only++
					continue
				}
				if r.InputTokens == pl.InputTokens &&
					r.CacheReadTokens == pl.CacheReadTokens &&
					r.OutputTokens == pl.OutputTokens {
					matchedReqs++
				} else {
					mismatches++
					fmt.Printf("  MISMATCH conv=%s step=%d: L2(in=%d, cr=%d, out=%d) vs L3(in=%d, cr=%d, out=%d)\n",
						shortID(cid), r.StepIndex,
						r.InputTokens, r.CacheReadTokens, r.OutputTokens,
						pl.InputTokens, pl.CacheReadTokens, pl.OutputTokens,
					)
				}
			}
		}
	}
	fmt.Printf("Checked %d conversation transcripts: %d exact matches, %d mismatches, %d L2-only steps (not in transcript).\n",
		checkedConvs, matchedReqs, mismatches, l2Only)
	if mismatches > 0 {
		os.Exit(2)
	}
}

func snapshotSignature(snap model.AppSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d:%s|", snap.RunningCount, snap.ActiveConversationID)
	for _, c := range snap.Conversations {
		fmt.Fprintf(&b, "%s:%v:%d:%.6f:%d|", c.ID, c.Running, c.TreeMetrics.RequestCount, c.TreeMetrics.SessionCostUSD, len(c.Subagents))
	}
	return b.String()
}

func fmtTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func fmtDur(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return string(r[:maxLen])
	}
	return string(r[:maxLen-1]) + "…"
}
