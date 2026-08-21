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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/jabing/shutu-agent/internal/llm"
	"github.com/jabing/shutu-agent/internal/session"
	"github.com/jabing/shutu-agent/internal/webserver"
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
	srv.SetMessageHandler(func(ctx context.Context, sessionID, text string, images []llm.ImageRef) error {
		return a.webMessage(ctx, sessionID, text, images)
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
	// M10 P5 (ADR D-WEB2-I): wire the image-attachment store when multimodal is
	// enabled (registerAttachments created it); otherwise the attachment APIs
	// stay at 501 and image-carrying messages answer 501/400.
	if a.attachStore != nil {
		srv.SetAttachmentStore(a.attachStore)
	}
	// P5.1 (模型选择实时生效): wire the live model switch.
	srv.SetModelSwitcher(func(ctx context.Context, provider, model string) error {
		return a.webSwitchModel(ctx, provider, model)
	})
	// M11 (增加提供方 / 增加自定义提供方): wire the provider-management API. A
	// "save" of a built-in provider stores only the API-key override (custom:false);
	// a "save" of a custom provider (custom:true) persists the full profile + key;
	// "delete" removes a custom provider. All apply immediately via registerLLM.
	srv.SetProviderManager(func(ctx context.Context, action string, edit webserver.ProviderEdit) error {
		switch action {
		case "save":
			if edit.Custom {
				return a.webSaveCustomProvider(ctx, edit.ID, edit.Name, edit.BaseURL, edit.Model, edit.APIKey)
			}
			return a.webSaveProvider(ctx, edit.ID, edit.APIKey)
		case "delete":
			return a.webDeleteCustomProvider(ctx, edit.ID)
		default:
			return fmt.Errorf("unknown provider action %q", action)
		}
	})
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
// the flow). P5: an images list logs a user/message event carrying the image
// blocks first (only the refs — the bytes live in the attachment store, same
// path as /attach, D4: the loop is untouched), then the text turn runs.
func (a *app) webMessage(ctx context.Context, sessionID, text string, images []llm.ImageRef) error {
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return errors.New("empty message text")
	}
	if sessionID != "" && sessionID != a.currentID {
		if err := a.resumeSession(ctx, sessionID); err != nil {
			return err
		}
	}
	if len(images) > 0 {
		if !a.multimodalEnabled() || a.attachStore == nil {
			return fmt.Errorf("multimodal disabled (llm.multimodal.enabled=false)")
		}
		blocks := make([]llm.ContentBlock, 0, len(images))
		for _, img := range images {
			blocks = append(blocks, llm.ContentBlock{Kind: llm.BlockImage, Image: img})
		}
		if a.log == nil {
			return fmt.Errorf("no active session")
		}
		if _, err := a.log.Append(session.EventUserMessage, session.NewUserMessageWithBlocks("", blocks)); err != nil {
			return fmt.Errorf("web message: log image: %w", err)
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
// settings page cannot leak credentials. Field names are snake_case. P5.1 adds
// the live model panel: the currently active provider's model plus the
// registered providers (id/available/model/candidates) for the pickers.
func (a *app) webConfig() map[string]any {
	enabled := a.cfg.Tools.Enabled
	tools := enabled
	if len(enabled) > maxWebToolsList {
		tools = append([]string(nil), enabled[:maxWebToolsList]...)
		tools = append(tools, "…")
	}
	return map[string]any{
		"model":        llmProviderModel(a.cfg, a.cfg.LLM.Provider),
		"base_url":     a.cfg.BaseURL,
		"llm_provider": a.cfg.LLM.Provider,
		"mode":         a.cfg.Mode,
		"providers":    a.webProviders(), // P5.1 live model pickers

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
		"multimodal_enabled": a.multimodalEnabled(),

		"web_server_addr":     a.cfg.WebServer.Addr,
		"tools_enabled_count": len(enabled),
		"tools_enabled":       tools,
	}
}

// webProviders returns the known providers for the P5.1/M11 model pickers:
// every built-in provider (deepseek always; openai/anthropic even when their
// credential is absent, so the "增加提供方" setup flow can configure them) plus
// every M11 custom provider declared in settings. Each entry carries its id,
// whether it is a custom provider, registration/availability state, the
// configured-key state (configured = a key is present in settings or env), its
// current model, base_url, suggested model candidates and the env var that
// carries its credential. Only these leaf fields leave the process — never
// keys, prompts or tokens.
func (a *app) webProviders() []map[string]any {
	a.llmMu.RLock()
	reg := a.llmReg
	a.llmMu.RUnlock()
	if reg == nil {
		return nil
	}
	out := make([]map[string]any, 0, 8)
	// Built-in provider definitions (deepseek always, openai/anthropic even when
	// unregistered) so the settings page can add their key.
	type builtin struct {
		id       string
		custom   bool
		model    string
		baseURL  string
		env      string
		envKnown bool
	}
	builtins := []builtin{
		{id: "deepseek", model: llmProviderModel(a.cfg, "deepseek"), baseURL: llmProviderBaseURL(a.cfg, "deepseek"), env: "DEEPSEEK_API_KEY"},
		{id: "openai", model: llmProviderModel(a.cfg, "openai"), baseURL: llmProviderBaseURL(a.cfg, "openai"), env: "OPENAI_API_KEY"},
		{id: "anthropic", model: llmProviderModel(a.cfg, "anthropic"), baseURL: llmProviderBaseURL(a.cfg, "anthropic"), env: "ANTHROPIC_API_KEY"},
	}
	for _, b := range builtins {
		registered := false
		available := false
		if p, err := reg.Get(b.id); err == nil {
			registered = true
			available = p.Available()
		}
		out = append(out, map[string]any{
			"id":         b.id,
			"custom":     false,
			"registered": registered,
			"available":  available,
			"configured": a.providerKey(b.id) != "",
			"model":      b.model,
			"base_url":   b.baseURL,
			"candidates": modelCandidates(b.id),
			"env_var":    b.env,
		})
	}
	// M11 custom providers from settings.
	for _, cp := range a.customProviders {
		registered := false
		available := false
		if p, err := reg.Get(cp.ID); err == nil {
			registered = true
			available = p.Available()
		}
		out = append(out, map[string]any{
			"id":         cp.ID,
			"name":       cp.Name,
			"custom":     true,
			"registered": registered,
			"available":  available,
			"configured": a.providerKey(cp.ID) != "",
			"model":      cp.Model,
			"base_url":   cp.BaseURL,
			"candidates": nil,
			"env_var":    llmKeyEnv(cp.ID),
		})
	}
	return out
}

// webSaveProvider persists a provider API-key override (M11, POST
// /api/config/provider): it writes llm.key.<id> to the settings table and
// rebuilds the registry so the change applies immediately (no restart — the
// registry is built per registerLLM, and this re-runs it). It runs under
// turnMu (D5 serial: no turn is in flight while the registry is rebuilt).
// An empty api_key removes the override, falling back to the env var.
func (a *app) webSaveProvider(ctx context.Context, id, apiKey string) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.llmReg == nil {
		return fmt.Errorf("llm not registered")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("provider id is required")
	}
	if apiKey != "" {
		if err := a.store.SetSetting(ctx, "llm.key."+id, apiKey); err != nil {
			return err
		}
		if a.llmKeys == nil {
			a.llmKeys = map[string]string{}
		}
		a.llmKeys[id] = apiKey
	} else {
		if err := a.store.DeleteSetting(ctx, "llm.key."+id); err != nil {
			return err
		}
		delete(a.llmKeys, id)
	}
	// Rebuild the registry so the new key is live immediately.
	if err := a.registerLLM(); err != nil {
		return err
	}
	if a.compaction != nil {
		_ = a.registerCompaction()
	}
	return nil
}

// webSaveCustomProvider persists a custom OpenAI-compatible provider
// declaration (M11, POST /api/config/provider with custom:true): it validates
// the profile, stores llm.custom.<id> + an optional llm.key.<id> override and
// rebuilds the registry immediately.
func (a *app) webSaveCustomProvider(ctx context.Context, id, name, baseURL, model, apiKey string) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.llmReg == nil {
		return fmt.Errorf("llm not registered")
	}
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	baseURL = strings.TrimSpace(baseURL)
	model = strings.TrimSpace(model)
	if id == "" || name == "" || baseURL == "" || model == "" {
		return errors.New("id, name, base_url and model are required")
	}
	if id == "deepseek" || id == "openai" || id == "anthropic" {
		return errors.New("custom provider id conflicts with a built-in provider")
	}
	if !validProviderRoute(id) {
		return errors.New("provider id must be lowercase letters, digits or '-'")
	}
	raw, err := json.Marshal(customProviderProfile{ID: id, Name: name, BaseURL: baseURL, Model: model})
	if err != nil {
		return err
	}
	if err := a.store.SetSetting(ctx, "llm.custom."+id, string(raw)); err != nil {
		return err
	}
	if apiKey != "" {
		if err := a.store.SetSetting(ctx, "llm.key."+id, apiKey); err != nil {
			return err
		}
		if a.llmKeys == nil {
			a.llmKeys = map[string]string{}
		}
		a.llmKeys[id] = apiKey
	}
	replaced := false
	for i := range a.customProviders {
		if a.customProviders[i].ID == id {
			a.customProviders[i] = customProviderProfile{ID: id, Name: name, BaseURL: baseURL, Model: model}
			replaced = true
			break
		}
	}
	if !replaced {
		a.customProviders = append(a.customProviders, customProviderProfile{ID: id, Name: name, BaseURL: baseURL, Model: model})
	}
	if err := a.registerLLM(); err != nil {
		return err
	}
	if a.compaction != nil {
		_ = a.registerCompaction()
	}
	return nil
}

// webDeleteCustomProvider removes a custom provider declaration (M11, DELETE
// /api/config/provider): it deletes llm.custom.<id> and its key override, then
// rebuilds the registry. Built-in providers cannot be removed.
func (a *app) webDeleteCustomProvider(ctx context.Context, id string) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.llmReg == nil {
		return fmt.Errorf("llm not registered")
	}
	id = strings.TrimSpace(id)
	if id == "deepseek" || id == "openai" || id == "anthropic" {
		return errors.New("built-in providers cannot be removed")
	}
	found := false
	for _, cp := range a.customProviders {
		if cp.ID == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("custom provider %q not found", id)
	}
	if err := a.store.DeleteSetting(ctx, "llm.custom."+id); err != nil {
		return err
	}
	if err := a.store.DeleteSetting(ctx, "llm.key."+id); err != nil {
		return err
	}
	kept := a.customProviders[:0]
	for _, cp := range a.customProviders {
		if cp.ID != id {
			kept = append(kept, cp)
		}
	}
	a.customProviders = kept
	delete(a.llmKeys, id)
	if err := a.registerLLM(); err != nil {
		return err
	}
	if a.compaction != nil {
		_ = a.registerCompaction()
	}
	return nil
}

// validProviderRoute reports whether id is a safe custom-provider route:
// lowercase letters, digits and single '-' separators.
func validProviderRoute(id string) bool {
	if id == "" {
		return false
	}
	for i, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		if r == '-' && i > 0 && i < len(id)-1 {
			continue
		}
		return false
	}
	return true
}

// webSwitchModel implements POST /api/config/model (P5.1, 模型选择实时生效): it
// validates and applies a live provider/model change, then rebuilds the
// selected LLM provider — no restart. It runs under turnMu (D5 serial: no turn
// is in flight while the selection swaps) and registerLLM publishes the new
// pointer under llmMu, so the very next message (buildLoop re-wires every turn)
// talks to the new provider. The change is runtime-only: config.yaml stays the
// source of truth for the next launch. Fail-closed: on error the previous
// selection is fully restored.
func (a *app) webSwitchModel(ctx context.Context, provider, model string) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.llmReg == nil {
		return fmt.Errorf("llm not registered")
	}
	if provider != "" {
		p, err := a.llmReg.Get(provider)
		if err != nil {
			return fmt.Errorf("unknown provider %q (registered: %s)", provider, llmProviderIDs(a.llmReg))
		}
		if !p.Available() {
			return fmt.Errorf("provider %q not available (missing %s)", provider, llmCredentialEnv(provider))
		}
	}
	target := provider
	if target == "" {
		target = a.cfg.LLM.Provider
	}
	// Snapshot for rollback.
	oldProvider := a.cfg.LLM.Provider
	oldModel, oldOpenAI, oldAnthropic := a.cfg.Model, a.cfg.LLM.OpenAI.Model, a.cfg.LLM.Anthropic.Model
	if provider != "" {
		a.cfg.LLM.Provider = provider
	}
	if model != "" {
		switch target {
		case "openai":
			a.cfg.LLM.OpenAI.Model = model
		case "anthropic":
			a.cfg.LLM.Anthropic.Model = model
		default:
			a.cfg.Model = model
		}
	}
	if err := a.registerLLM(); err != nil {
		// Restore the previous selection — never leave a half-applied switch.
		a.cfg.LLM.Provider = oldProvider
		a.cfg.Model, a.cfg.LLM.OpenAI.Model, a.cfg.LLM.Anthropic.Model = oldModel, oldOpenAI, oldAnthropic
		return err
	}
	// Rebuild compaction on the new provider so auto-summaries follow the switch.
	if a.compaction != nil {
		_ = a.registerCompaction()
	}
	return nil
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
