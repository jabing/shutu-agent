// compact.go — the M5c-2b composition-root orchestration (dispatch-m5c-2b
// §1/§2/§3). This is where the context-compaction capability seam is wired into
// the REPL: registerCompaction creates the BasicEngine when compaction.enabled
// (D10), /compact (+ /compact region) are the manual command, and (Pass 2) the
// loop's "compaction" pre-step injector runs the token-pressure auto-compaction.
// The compaction/start → compaction/summary → compaction/end observation events
// are appended to the active session log on the serial path — the command
// handler or the pre-step injector — never from a background goroutine (D5),
// mirroring the job/subagent onEvent sink pattern (D3). The log stays
// append-only (D1): the engine appends the summary as a surfaceOp.replace
// user/message (M5c-1a) and these events only record that fact; nothing is ever
// physically deleted.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"personal-agent/internal/compaction"
	"personal-agent/internal/session"
)

// registerCompaction creates the default BasicEngine when compaction.enabled,
// and wires nothing when disabled (D10, mirrors registerJobs/registerSubagent).
// Unlike kb/jobs/subagent there are no consumer tools to register or whitelist
// (compaction has none): automatic triggering runs through the loop pre-step
// injector, manual through the /compact command. The engine holds no closable
// resources (it shares the caller-owned LLM), so there is no deferred Close.
func (a *app) registerCompaction() error {
	if !a.cfg.Compaction.Enabled {
		return nil
	}
	a.compaction = compaction.NewBasic(compaction.BasicOpts{
		LLM:            a.llm,
		Model:          a.cfg.Model,
		TokenThreshold: a.cfg.Compaction.TokenThreshold,
		RetainTurns:    a.cfg.Compaction.RetainTurns,
	})
	return nil
}

// compactAndLog runs one compaction attempt through the engine and appends the
// compaction/* observation events (D3) on the serial path: compaction/start is
// logged before the engine call (the attempt marker, with its reason/trigger),
// then compaction/summary + compaction/end when the attempt produced a result
// (bounded summary, shadowed range, tokens saved). A nil result (nothing
// foldable) or an engine error leaves only the start — the ADR's "orphan start
// reveals an interrupted/no-op attempt" signal. Event append failures are
// surfaced as stderr warnings and never block the attempt (fail-open, same as
// the job/subagent onEvent sinks).
func (a *app) compactAndLog(ctx context.Context, reason, trigger string, run func() (*compaction.Result, error)) (*compaction.Result, error) {
	if _, err := a.log.Append(session.EventCompactionStart, session.NewCompactionStart(reason, trigger)); err != nil {
		fmt.Fprintln(os.Stderr, "pa: compaction/start event:", err)
	}
	res, err := run()
	if err != nil || res == nil {
		return res, err
	}
	if _, err := a.log.Append(session.EventCompactionSummary, session.NewCompactionSummary(res.CompactionID, res.Summary)); err != nil {
		fmt.Fprintln(os.Stderr, "pa: compaction/summary event:", err)
	}
	if _, err := a.log.Append(session.EventCompactionEnd, session.NewCompactionEnd(res.CompactionID, res.ShadowedRange, res.ShadowedTokens)); err != nil {
		fmt.Fprintln(os.Stderr, "pa: compaction/end event:", err)
	}
	return res, nil
}

// compactCommand handles the /compact and /compact region <start> <end>
// commands (dispatch-m5c-2b §1). It reports the capability as unavailable when
// compaction is disabled (D10); otherwise it performs one manual compaction on
// the serial command path and prints the summary, the shadowed surface range
// and the tokens saved. The compaction/* observation events are appended by
// compactAndLog — the command itself adds no extra event type.
func (a *app) compactCommand(ctx context.Context, args []string) error {
	if a.compaction == nil {
		fmt.Println("compaction: disabled (compaction.enabled=false)")
		return nil
	}
	var res *compaction.Result
	var err error
	switch {
	case len(args) == 3 && args[0] == "region":
		start, e1 := strconv.ParseInt(args[1], 10, 64)
		end, e2 := strconv.ParseInt(args[2], 10, 64)
		if e1 != nil || e2 != nil {
			return fmt.Errorf("usage: /compact region <start> <end> (integer event seqs)")
		}
		res, err = a.compactAndLog(ctx, "manual /compact region command", "manual",
			func() (*compaction.Result, error) { return a.compaction.CompactRegion(ctx, a.log, start, end) })
	case len(args) != 0:
		return fmt.Errorf("usage: /compact or /compact region <start> <end>")
	default:
		res, err = a.compactAndLog(ctx, "manual /compact command", "manual",
			func() (*compaction.Result, error) { return a.compaction.CompactNow(ctx, a.log) })
	}
	if err != nil {
		return err
	}
	if res == nil {
		fmt.Println("compaction: nothing to compact")
		return nil
	}
	fmt.Printf("compacted %d events (seq %d..%d), saved %d tokens (id %s)\nsummary: %s\n",
		len(res.ShadowedSeqs), res.ShadowedRange[0], res.ShadowedRange[1], res.ShadowedTokens, res.CompactionID, res.Summary)
	return nil
}
