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

	"personal-agent/internal/config"
	"personal-agent/internal/llm"
	"personal-agent/internal/llm/deepseek"
	"personal-agent/internal/loop"
	"personal-agent/internal/prompt"
	"personal-agent/internal/session"
	"personal-agent/internal/store"
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

	currentID string
	log       *session.Log
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

// newLoop builds a Loop bound to the current session log.
func (a *app) newLoop() *loop.Loop {
	return loop.New(loop.Config{
		LLM:    a.llm,
		Log:    a.log,
		Tools:  a.reg,
		Prompt: a.prompt,
		Model:  a.cfg.Model,
		OnText: func(delta string) { fmt.Print(delta) },
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
	default:
		return fmt.Errorf("unknown command %q (try /help)", fields[0])
	}
	return nil
}

// printHelp prints the complete command table (M3 CLI 完善).
func (a *app) printHelp() {
	fmt.Println(`commands:
  /new              start a new session
  /list             list all sessions (most recently updated first)
  /resume <id>      resume an existing session by id
  /help             show this command table
  /exit             quit (alias: /quit)
  anything else     send to the agent as a message

startup:  pa [--config <path>]   config defaults to config.yaml`)
	fmt.Printf("enabled tools: %s\n", strings.Join(a.cfg.Tools.Enabled, ", "))
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
