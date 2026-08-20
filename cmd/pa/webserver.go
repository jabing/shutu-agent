// webserver.go — the M10a composition root for the unified web portal (ADR
// 2026-08-20-m10-web-portal.md D-WEB-7): when web_server.enabled (默认关 D10)
// it builds the bearer-authenticated net/http portal over the read-only store
// and starts the listener on a background goroutine. An empty token fails
// closed at startup (no bare server, D-WEB-2). main defers Close to shut the
// listener at shutdown (lifecycle reversible).
//
// M10 W1 (ADR 2026-08-20-m10-web-workspace.md D-WEB2-A/B/C): this file also
// owns the real-time event hub — attachSink publishes each persisted event and
// the web's SSE streams subscribe per session id — and injects the interactive
// handlers (message dispatch with implicit resume, session new/resume, the
// event source) into the otherwise generic webserver at registration time.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"personal-agent/internal/session"
	"personal-agent/internal/webserver"
)

// eventHub is the real-time event broadcaster (ADR D-WEB2-B): attachSink
// publishes every persisted event of the current session, and each SSE stream
// subscribes to one session id. Publish is non-blocking — a slow subscriber
// whose buffer is full is dropped (select default) so the hub can never stall
// the serial persist path; honest: under extreme load SSE may drop an event and
// the frontend falls back on the snapshot plus the later events.
const eventHubBuffer = 256

type eventHub struct {
	mu   sync.Mutex
	subs map[string]map[chan session.Event]struct{}
}

// NewEventHub returns an empty event hub.
func NewEventHub() *eventHub {
	return &eventHub{subs: make(map[string]map[chan session.Event]struct{})}
}

// Publish broadcasts ev to every subscriber of the session (non-blocking: a
// subscriber whose buffer is full is dropped rather than blocking the caller —
// the serial loop/persist path must never wait on a slow SSE consumer).
func (h *eventHub) Publish(sessionID string, ev session.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[sessionID] {
		select {
		case ch <- ev:
		default:
			// Buffer full: drop this slow subscriber.
		}
	}
}

// Subscribe registers a buffered subscriber channel for a session and returns
// the channel plus an unsubscribe closure. The closure unsubscribes and closes
// the channel, so a reader's range loop ends.
func (h *eventHub) Subscribe(sessionID string) (chan session.Event, func()) {
	ch := make(chan session.Event, eventHubBuffer)
	h.mu.Lock()
	if h.subs[sessionID] == nil {
		h.subs[sessionID] = make(map[chan session.Event]struct{})
	}
	h.subs[sessionID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if set := h.subs[sessionID]; set != nil {
			if _, ok := set[ch]; ok {
				delete(set, ch)
				close(ch)
			}
			if len(set) == 0 {
				delete(h.subs, sessionID)
			}
		}
		h.mu.Unlock()
	}
}

// SubscribeInto subscribes to a session and forwards every event to sink on a
// background goroutine. The returned func unsubscribes and stops the forwarder
// (the subscriber channel is closed, ending the forwarder's range loop).
func (h *eventHub) SubscribeInto(sessionID string, sink func(session.Event)) func() {
	ch, unsub := h.Subscribe(sessionID)
	go func() {
		for ev := range ch {
			sink(ev)
		}
	}()
	return unsub
}

func (a *app) registerWebServer() error {
	if !a.cfg.WebServer.Enabled {
		return nil // D10: not registered when disabled
	}
	srv, err := webserver.New(a.store, a.cfg.WebServer.Token, a.cfg.WebServer.Addr)
	if err != nil {
		return fmt.Errorf("register web server: %w", err)
	}
	if a.hub == nil {
		a.hub = NewEventHub()
	}
	// M10 W1 (ADR D-WEB2): inject the interactive handlers — message dispatch
	// (with implicit resume), session new/resume and the real-time event source
	// (the hub). The webserver stays generic; cmd/pa provides the behavior.
	srv.SetMessageHandler(func(ctx context.Context, sessionID, text string) error {
		return a.webMessage(ctx, sessionID, text)
	})
	srv.SetSessionManager(func(ctx context.Context, action, id string) (string, error) {
		return a.webSessionManager(ctx, action, id)
	})
	srv.SetEventSource(func(sessionID string, sink func(session.Event)) func() {
		return a.hub.SubscribeInto(sessionID, sink)
	})
	// M10 W2 (ADR D-WEB2-D): inject the sanitized config view. webConfig never
	// exposes web_server.token or any key — the webserver only forwards it.
	srv.SetConfigProvider(a.webConfig)
	// M10 W4 (ADR D-WEB2-H): inject the read-only subagent and background-job
	// panels. Each provider returns sanitized views (id/status/timestamps only);
	// a disabled capability answers an empty list, never an error.
	srv.SetSubagentProvider(a.webSubagents)
	srv.SetJobsProvider(a.webJobs)
	a.webserver = srv
	go func() {
		if err := srv.Serve(); err != nil {
			fmt.Fprintln(os.Stderr, "pa: web server:", err)
		}
	}()
	return nil
}

// webMessage handles one web chat message for a session (ADR D-WEB2-A): when
// the target session differs from the current one it is resumed first (attachSink
// already rebinds to the new session), then the turn runs under the global serial
// lock with a silent loop (chunks already persist; the SSE event stream renders
// the flow).
func (a *app) webMessage(ctx context.Context, sessionID, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("empty message text")
	}
	if sessionID != "" && sessionID != a.currentID {
		if err := a.resumeSession(ctx, sessionID); err != nil {
			return err
		}
	}
	return a.runTurn(ctx, text, false)
}

// webSessionManager implements the session new/resume API (ADR D-WEB2-C),
// reusing the REPL's newSession/resumeSession.
func (a *app) webSessionManager(ctx context.Context, action, id string) (string, error) {
	switch action {
	case "new":
		if err := a.newSession(ctx); err != nil {
			return "", err
		}
		return a.currentID, nil
	case "resume":
		if err := a.resumeSession(ctx, id); err != nil {
			return "", err
		}
		return a.currentID, nil
	default:
		return "", fmt.Errorf("unknown session action %q", action)
	}
}

// maxWebToolsList caps the tool-whitelist entries served by webConfig (M10 W2):
// the settings page shows the count plus a bounded sample, so a huge whitelist
// never floods the payload (the "…" tail marks a truncation).
const maxWebToolsList = 30

// webConfig returns the sanitized, flat configuration view served by
// GET /api/config (M10 W2, ADR D-WEB2-D): model/provider/mode, each capability
// gate's enabled flag, the web-server address and the tool whitelist (count +
// bounded list). Secrets never leave — web_server.token is omitted entirely
// (keys live in the environment, never in this config), so a compromised
// settings page cannot leak credentials. Field names are snake_case.
func (a *app) webConfig() map[string]any {
	enabled := a.cfg.Tools.Enabled
	tools := enabled
	if len(enabled) > maxWebToolsList {
		tools = append([]string(nil), enabled[:maxWebToolsList]...)
		tools = append(tools, "…")
	}
	return map[string]any{
		"model":        a.cfg.Model,
		"base_url":     a.cfg.BaseURL,
		"llm_provider": a.cfg.LLM.Provider,
		"mode":         a.cfg.Mode,

		// Capability gates (D10: each seam is default off).
		"terminal_enabled":   a.cfg.Terminal.Enabled,
		"fs_enabled":         a.cfg.Fs.Enabled,
		"fs_search_enabled":  a.cfg.FsSearch.Enabled,
		"ralph_enabled":      a.cfg.Ralph.Enabled,
		"workflow_enabled":   a.cfg.Workflow.Enabled,
		"kb_enabled":         a.cfg.KB.Enabled,
		"jobs_enabled":       a.cfg.Jobs.Enabled,
		"subagent_enabled":   a.cfg.Subagent.Enabled,
		"web_enabled":        a.cfg.Web.Enabled,
		"eval_enabled":       a.cfg.Eval.Enabled,
		"code_enabled":       a.cfg.Code.Enabled,
		"interact_enabled":   a.cfg.Interact.Enabled,
		"mcp_enabled":        a.cfg.Mcp.Enabled,
		"skill_enabled":      a.cfg.Skill.Enabled,
		"schedule_enabled":   a.cfg.Schedule.Enabled,
		"plan_enabled":       a.cfg.Plan.Enabled,
		"spill_enabled":      a.cfg.Spill.Enabled,
		"compaction_enabled": a.cfg.Compaction.Enabled,
		"multimodal_enabled": a.cfg.LLM.Multimodal.Enabled,

		"web_server_addr":     a.cfg.WebServer.Addr,
		"tools_enabled_count": len(enabled),
		"tools_enabled":       tools,
	}
}

// webSubagents returns the sanitized active sub-agent views for GET
// /api/subagents (ADR D-WEB2-H): only id/label/running — never prompts or
// outputs. A disabled subagent capability answers an empty list, not an error.
func (a *app) webSubagents(ctx context.Context) ([]map[string]any, error) {
	if a.subagents == nil {
		return []map[string]any{}, nil
	}
	children, err := a.subagents.ListChildren(ctx, a.currentID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(children))
	for _, c := range children {
		out = append(out, map[string]any{"id": c.ID, "label": c.Label, "running": c.Running})
	}
	return out, nil
}

// webJobs returns the sanitized background-job views for GET /api/jobs (ADR
// D-WEB2-H): id/kind/label/status/detail/started_at/finished_at — never outputs
// or owner-session internals. A disabled jobs capability answers an empty list.
func (a *app) webJobs(ctx context.Context) ([]map[string]any, error) {
	if a.jobs == nil {
		return []map[string]any{}, nil
	}
	snaps, err := a.jobs.List(ctx, a.currentID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(snaps))
	for _, j := range snaps {
		item := map[string]any{
			"id": j.ID, "kind": j.Kind, "label": j.Label,
			"status": j.Status, "detail": j.Detail,
			"started_at": j.StartedAt,
		}
		if j.FinishedAt != nil {
			item["finished_at"] = *j.FinishedAt
		}
		out = append(out, item)
	}
	return out, nil
}
