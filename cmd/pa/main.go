// Command pa is the personal agent REPL (M1→M3). It wires the thin core — llm,
// session, tools, prompt, loop — plus the durable store (design.md D8) and
// drives turns from stdin. Sessions persist to data_dir/pa.db and are resumed
// across restarts; /new, /list and /resume manage multiple sessions. M3 adds
// the tool-execution safety policy (whitelist, timeout, output truncation/spill)
// and --config. The DeepSeek API key is read from the DEEPSEEK_API_KEY
// environment variable only.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"personal-agent/internal/compaction"
	"personal-agent/internal/config"
	"personal-agent/internal/jobs"
	"personal-agent/internal/kb"
	"personal-agent/internal/llm"
	"personal-agent/internal/llm/deepseek"
	"personal-agent/internal/loop"
	"personal-agent/internal/plan"
	"personal-agent/internal/prompt"
	"personal-agent/internal/schedule"
	"personal-agent/internal/session"
	"personal-agent/internal/skill"
	"personal-agent/internal/spill"
	"personal-agent/internal/store"
	"personal-agent/internal/subagent"
	"personal-agent/internal/tools"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the configuration file")
	flag.Parse()

	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "pa: DEEPSEEK_API_KEY is not set (API keys only ever come from the environment)")
		os.Exit(1)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}

	st, err := store.OpenSQLite(filepath.Join(cfg.DataDir, "pa.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	defer st.Close()

	// M3: the Execute pipeline's safety policy — whitelist, deadline, output
	// cap with spill to <data_dir>/spill (design.md §5).
	reg := tools.New()
	reg.SetPolicy(tools.PolicyFromConfig(cfg.Tools, cfg.DataDir))
	// The read-only built-ins are always registered; the whitelist gates their
	// execution. The execution-class tool is registered only when enabled
	// (默认关闭, D10).
	if err := reg.Register(tools.GetTime{}); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	if err := reg.Register(tools.ReadFile{}); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	if cfg.Tools.RunCommand.Enabled {
		if err := reg.Register(tools.NewRunCommand(cfg.Tools.RunCommand.Workdir)); err != nil {
			fmt.Fprintln(os.Stderr, "pa:", err)
			os.Exit(1)
		}
	}

	promptBuilder, err := prompt.LoadDir(cfg.PromptsDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	promptBuilder.SetTools(func() []llm.ToolSchema { return reg.Specs() })

	client := deepseek.New(deepseek.Config{
		APIKey:     apiKey,
		BaseURL:    cfg.BaseURL,
		Model:      cfg.Model,
		MaxRetries: 2,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	app := &app{
		cfg:    cfg,
		store:  st,
		reg:    reg,
		prompt: promptBuilder,
		llm:    client,
	}
	// M4b: wire the knowledge base seam — provider + kb_* tools + catalog —
	// when kb.enabled (默认关闭, D10). kb.registerKB appends the kb_* tool names
	// to nothing itself; config.applyDefaults already whitelisted them when
	// kb.enabled was true.
	if err := app.registerKB(); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	if app.kb != nil {
		defer app.kb.Close()
	}
	// M5a-2: wire the jobs seam — Local registry + the five job_* tools + the
	// D3 event sink — when jobs.enabled (默认关闭, D10). config.applyDefaults
	// already whitelisted the job_* names when jobs.enabled was true. The
	// deferred Close cancels and awaits every live background job at shutdown
	// so no goroutine leaks (lifecycle reversible, ADR 决策 ①).
	if err := app.registerJobs(); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	if app.jobs != nil {
		defer app.jobs.Close()
	}
	// M5b-2: wire the subagent seam — spawn provider + Runtime + the four
	// subagent_* tools + the D3 event sink — when subagent.enabled (默认关闭,
	// D10). config.applyDefaults already whitelisted the subagent_* names when
	// subagent.enabled was true. The deferred Close cancels and awaits every
	// live child at shutdown so no background goroutine leaks (lifecycle
	// reversible, ADR 决策 ②).
	if err := app.registerSubagent(); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	if app.subagents != nil {
		defer app.subagents.Close()
	}
	// M5c-2b: wire the compaction seam — BasicEngine for the /compact command
	// and the loop "compaction" pre-step injector — when compaction.enabled
	// (默认关闭, D10). Compaction whitelists no consumer tools (it has none:
	// automatic triggering runs through the loop pre-step injector, manual
	// through the /compact command), so config.applyDefaults already handled
	// the whole gate. The engine shares the caller-owned LLM and holds no
	// closable resources, so there is no deferred Close.
	if err := app.registerCompaction(); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	// M5d-2: wire the skill seam — filesystem provider + Registry + the
	// skill_load tool + the "skill" pre-step catalog injector — when
	// skill.enabled (默认关闭, D10). config.applyDefaults already whitelisted
	// skill_load when skill.enabled was true. The deferred Close releases the
	// registry and its providers at shutdown (lifecycle reversible, ADR
	// 决策 ④).
	if err := app.registerSkills(); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	if app.skills != nil {
		defer app.skills.Close()
	}
	// M6a-2: wire the schedule seam — in-memory Provider + Engine + the three
	// schedule_* tools + the D3 event sink + the "schedule" pre-step injector
	// — when schedule.enabled (默认关闭, D10). config.applyDefaults already
	// whitelisted the schedule_* names when schedule.enabled was true. The
	// deferred Close releases the provider and rejects further operations at
	// shutdown (lifecycle reversible, ADR 决策 M6a). There is no background
	// ticker: the loop's per-turn "schedule" pre-step injector advances the
	// clock on the serial path (D5).
	if err := app.registerSchedules(); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	if app.schedules != nil {
		defer app.schedules.Close()
	}
	// M6b-2: wire the plan seam — in-memory Provider + Engine + the six
	// plan_* tools + the D3 event sink — when plan.enabled (默认关闭, D10).
	// config.applyDefaults already whitelisted the plan_* names when
	// plan.enabled was true. The deferred Close releases the provider and
	// rejects further operations at shutdown (lifecycle reversible, ADR
	// 决策 M6b). The plan tree is a planning model only — execution delegation
	// to subagents is deferred to M6c+.
	if err := app.registerPlans(); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	if app.plans != nil {
		defer app.plans.Close()
	}
	// M6c-2: wire the spill seam — in-memory Provider + Engine + the four
	// spill_* tools + the D3 event sink + the turn-completion auto-sedimentation
	// hook — when spill.enabled (默认关闭, D10). config.applyDefaults already
	// whitelisted the spill_* names when spill.enabled was true. The deferred
	// Close releases the provider and rejects further operations at shutdown
	// (lifecycle reversible, ADR 决策 M6c). AutoSpill runs on the serial
	// turn-completion path (after each completed turn in the REPL, D5); there
	// is no background goroutine.
	if err := app.registerSpills(); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	if app.spills != nil {
		defer app.spills.Close()
	}
	if err := app.startup(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	app.repl(ctx)
}

// app holds the REPL's mutable session state.
type app struct {
	cfg    config.Config
	store  store.Store
	reg    *tools.Registry
	prompt *prompt.Builder
	llm    llm.LLM
	kb     kb.KB // nil when kb disabled (D10)

	currentID string
	log       *session.Log
	jobs      *jobs.Local      // nil when jobs disabled (D10)
	subagents subagent.Runtime // nil when subagent disabled (D10)

	compaction compaction.Engine // nil when compaction disabled (D10)
	skills     skill.Registry    // nil when skill disabled (D10)
	schedules  schedule.Engine   // nil when schedule disabled (D10)
	plans      plan.Engine       // nil when plan disabled (D10)
	spills     spill.Engine      // nil when spill disabled (D10)

	// skillProjectRoot / skillUserHome override the filesystem skill provider's
	// project/user roots when non-empty; empty uses the provider defaults (the
	// working directory and the user home). They exist so the wiring tests can
	// pin deterministic roots.
	skillProjectRoot string
	skillUserHome    string
}

// startup resumes the most recently updated session, or starts a fresh one.
func (a *app) startup(ctx context.Context) error {
	sessions, err := a.store.ListSessions(ctx)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		if err := a.newSession(ctx); err != nil {
			return err
		}
		fmt.Printf("started new session %s\n", a.currentID)
		return nil
	}
	// ListSessions returns most recently updated first.
	last := sessions[0]
	if err := a.resumeSession(ctx, last.ID); err != nil {
		return err
	}
	fmt.Printf("resumed session %s (%d events)\n", a.currentID, len(a.log.Events()))
	return nil
}

// newSession starts a fresh session with a random id.
func (a *app) newSession(ctx context.Context) error {
	id, err := newSessionID()
	if err != nil {
		return fmt.Errorf("pa: generate session id: %w", err)
	}
	if err := a.store.CreateSession(ctx, id, time.Now().UTC()); err != nil {
		return err
	}
	a.currentID = id
	a.log = session.New()
	a.attachSink(ctx)
	a.bindSpillOwner()
	return nil
}

// resumeSession loads a session's full history from the store into a new log.
func (a *app) resumeSession(ctx context.Context, id string) error {
	events, err := a.store.LoadSession(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no such session %q (see /list)", id)
		}
		return err
	}
	a.currentID = id
	a.log = session.New()
	if err := a.log.Restore(events); err != nil {
		return err
	}
	a.attachSink(ctx)
	a.bindSpillOwner()
	return nil
}

// attachSink forwards every appended event to the durable store for the
// current session (D8: append-on-write, replay at startup).
func (a *app) attachSink(ctx context.Context) {
	id := a.currentID
	a.log.SetSink(func(ev session.Event) error {
		return a.store.AppendEvents(ctx, id, []session.Event{ev})
	})
}

// bindSpillOwner points the tool registry's spill naming at the active
// session. The next-seq closure is pinned to the current log, so a spill is
// named <session>-<seq>.txt with the exact seq of the tool/result event that
// will carry the locator (M3). Called on every session switch.
func (a *app) bindSpillOwner() {
	log := a.log
	a.reg.SetOwner(tools.Owner{
		SessionID: a.currentID,
		NextSeq:   func() uint64 { return log.NextSeq() },
	})
}

// newLoop builds a Loop bound to the current session log. The Recall hook is
// the M4b proactive-recall extension point (dispatch-m4b §2): it runs the
// per-turn recall orchestration in cmd/pa; the loop's turn/step structure is
// unchanged (D4).
func (a *app) newLoop() *loop.Loop {
	return loop.New(loop.Config{
		LLM:    a.llm,
		Log:    a.log,
		Tools:  a.reg,
		Prompt: a.prompt,
		Model:  a.cfg.Model,
		Recall: a.recall,
		// M5c-2b: the "compaction" pre-step injector (auto token-pressure
		// compaction) is appended when compaction is enabled; it runs after the
		// M4b recall hook, inside the loop's existing pre-step extension point
		// (D4 — the turn/step structure is unchanged).
		PreStep: a.preStepInjectors(),
		OnText:  func(delta string) { fmt.Print(delta) },
		OnError: func(err error) {
			fmt.Fprintln(os.Stderr, "\n[stream error]", err)
		},
	})
}

// repl drives turns from stdin, handling the session commands.
func (a *app) repl(ctx context.Context) {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("pa — personal agent REPL. Type /help for the command table.")
	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "/exit" || line == "/quit" {
			break
		}
		if strings.HasPrefix(line, "/") {
			if err := a.command(ctx, line); err != nil {
				fmt.Fprintln(os.Stderr, "pa:", err)
			}
			continue
		}
		if err := a.newLoop().Run(ctx, line); err != nil {
			fmt.Fprintln(os.Stderr, "\npa:", err)
		} else {
			// M4c: post-answer extraction writeback, orchestrated by the
			// composition root outside the loop (D4). Fail-open by contract:
			// extractTurn never returns an error and never affects the next
			// answer.
			a.extractTurn(ctx, line)
			// M6c-2: post-turn auto-sedimentation, orchestrated by the
			// composition root outside the loop (D4). It runs once per
			// completed turn on the serial REPL path (D5) and never duplicates:
			// the AutoSpill policy is idempotent by content hash and this is
			// the only invocation point. Fail-open by contract.
			a.spillAutoSpill(ctx)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
}

// command dispatches the /-commands.
func (a *app) command(ctx context.Context, line string) error {
	fields := strings.Fields(line)
	switch fields[0] {
	case "/help":
		a.printHelp()
	case "/new":
		if err := a.newSession(ctx); err != nil {
			return err
		}
		fmt.Printf("new session %s\n", a.currentID)
	case "/list":
		return a.listSessions(ctx)
	case "/resume":
		if len(fields) < 2 {
			return fmt.Errorf("usage: /resume <id>")
		}
		if err := a.resumeSession(ctx, fields[1]); err != nil {
			return err
		}
		fmt.Printf("resumed session %s (%d events)\n", a.currentID, len(a.log.Events()))
	case "/kb-status":
		return a.kbStatus(ctx)
	case "/kb-reindex":
		return a.kbReindex(ctx)
	case "/compact":
		return a.compactCommand(ctx, fields[1:])
	default:
		return fmt.Errorf("unknown command %q (try /help)", fields[0])
	}
	return nil
}

// printHelp prints the complete command table (M3 CLI 完善; M4b adds kb).
func (a *app) printHelp() {
	fmt.Println(`commands:
  /new              start a new session
  /list             list all sessions (most recently updated first)
  /resume <id>      resume an existing session by id
  /kb-status        knowledge-base status (entries / db size / recent writes)
  /kb-reindex       rebuild the knowledge-base FTS index
  /compact          compact the session now (fold old context into a summary)
  /compact region <start> <end>  compact only the given surface event range
  /help             show this command table
  /exit             quit (alias: /quit)
  anything else     send to the agent as a message

startup:  pa [--config <path>]   config defaults to config.yaml`)
	fmt.Printf("enabled tools: %s\n", strings.Join(a.cfg.Tools.Enabled, ", "))
	if a.cfg.KB.Enabled {
		fmt.Printf("knowledge base: enabled (db=%s, recall_limit=%d, catalog=%v)\n",
			a.cfg.KB.DBPath, a.cfg.KB.RecallLimitValue(), a.cfg.KB.CatalogValue())
	} else {
		fmt.Println("knowledge base: disabled (kb.enabled=false)")
	}
	if a.cfg.Jobs.Enabled {
		fmt.Printf("jobs: enabled (max_concurrent_jobs_per_owner=%d)\n", a.cfg.Jobs.MaxConcurrentJobsPerOwner)
	} else {
		fmt.Println("jobs: disabled (jobs.enabled=false)")
	}
	if a.cfg.Compaction.Enabled {
		fmt.Printf("compaction: enabled (token_threshold=%d, retain_turns=%d)\n",
			a.cfg.Compaction.TokenThreshold, a.cfg.Compaction.RetainTurns)
	} else {
		fmt.Println("compaction: disabled (compaction.enabled=false)")
	}
	if a.cfg.Skill.Enabled {
		fmt.Printf("skills: enabled (catalog_max_chars=%d, body_max_chars=%d)\n",
			a.cfg.Skill.CatalogMaxChars, a.cfg.Skill.BodyMaxChars)
	} else {
		fmt.Println("skills: disabled (skill.enabled=false)")
	}
	if a.cfg.Schedule.Enabled {
		fmt.Printf("schedules: enabled (tick_interval=%s)\n", a.cfg.Schedule.TickInterval.Duration)
	} else {
		fmt.Println("schedules: disabled (schedule.enabled=false)")
	}
	if a.cfg.Plan.Enabled {
		fmt.Println("plans: enabled (goal → plan → todo planning tree)")
	} else {
		fmt.Println("plans: disabled (plan.enabled=false)")
	}
	if a.cfg.Spill.Enabled {
		fmt.Printf("spills: enabled (auto_spill=%v)\n", a.cfg.Spill.AutoSpillValue())
	} else {
		fmt.Println("spills: disabled (spill.enabled=false)")
	}
}

func (a *app) listSessions(ctx context.Context) error {
	sessions, err := a.store.ListSessions(ctx)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Println("no sessions yet — type /new to start one")
		return nil
	}
	for _, s := range sessions {
		marker := " "
		if s.ID == a.currentID {
			marker = "*"
		}
		fmt.Printf("%s %s  created=%s  updated=%s  events=%d\n",
			marker, s.ID,
			s.CreatedAt.Local().Format(time.RFC3339),
			s.UpdatedAt.Local().Format(time.RFC3339),
			s.EventCount)
	}
	return nil
}

// newSessionID returns a short random session id (e.g. "s-1a2b3c4d").
func newSessionID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "s-" + hex.EncodeToString(b[:]), nil
}
