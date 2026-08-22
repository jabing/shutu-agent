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

	"github.com/jabing/shutu-agent/internal/config"
	"github.com/jabing/shutu-agent/internal/llm"
	"github.com/jabing/shutu-agent/internal/session"
	"github.com/jabing/shutu-agent/internal/store"
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
	// dsh-session-status: wire the live per-session status computation so the
	// sidebar renders the status dot + hover card from runtime state (running
	// turn / running subagents / pending interaction / finished-but-unviewed).
	srv.SetSessionStatusProvider(a.sessionStatus)
	// M10 W2 (ADR D-WEB2-D): inject the sanitized config view. webConfig never
	// exposes web_server.token or any key — the webserver only forwards it.
	srv.SetConfigProvider(a.webConfig)
	srv.SetContextWindow(a.contextWindowOf)
	srv.SetTurnStopper(a.stopTurn)
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
				models := make([]customModel, 0, len(edit.Models))
				for _, m := range edit.Models {
					models = append(models, customModel{ID: m.ID, Name: m.Name, ContextWindow: m.ContextWindow, MaxTokens: m.MaxTokens})
				}
				return a.webSaveCustomProvider(ctx, edit.ID, edit.Name, edit.BaseURL, edit.Model, edit.APIKey, edit.Protocol, models)
			}
			return a.webSaveProvider(ctx, edit.ID, edit.APIKey)
		case "delete":
			return a.webDeleteCustomProvider(ctx, edit.ID)
		default:
			return fmt.Errorf("unknown provider action %q", action)
		}
	})
	// M11-pi-ai (模型探测, dsh discovery 对齐): wire the 获取可用模型 API so the
	// 增加自定义提供方 / 编辑卡 can fill the multi-model list from the endpoint.
	srv.SetProviderDiscover(func(ctx context.Context, req webserver.ProviderDiscover) ([]webserver.ProviderModel, error) {
		models, err := a.webDiscoverModels(ctx, discoverRequest{
			Provider: req.Provider,
			BaseURL:  req.BaseURL,
			Protocol: req.Protocol,
			APIKey:   req.APIKey,
		})
		if err != nil {
			return nil, err
		}
		out := make([]webserver.ProviderModel, 0, len(models))
		for _, m := range models {
			out = append(out, webserver.ProviderModel{ID: m.ID, Name: m.Name, ContextWindow: m.ContextWindow, MaxTokens: m.MaxTokens})
		}
		return out, nil
	})
	// 技能设置页 (dsh-skill-mcp-panel 对齐): wire the skill-management API. The
	// manager is created lazily (independent of skill.enabled) so the page
	// always lists the skill files it manages.
	srv.SetSkillManager(a.webSkills)
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
	// A cancellable turn context so POST /api/sessions/{id}/stop can abort this
	// turn (dsh 停止按钮) without touching the request context (which the
	// handler returns, cancelling it, after the turn completes).
	turnCtx, cancel := context.WithCancel(ctx)
	a.setTurnCancel(cancel)
	defer func() { a.clearTurnCancel(); cancel() }()
	if err := a.runTurn(turnCtx, text, false); err != nil {
		return err
	}
	// session-title alignment (dsh): after the first eligible message, the
	// deterministic fallback is stored and the asynchronous model title is
	// scheduled. This runs after the turn, outside turnMu, so it never delays
	// the answer.
	a.ensureSessionTitle(ctx, sessionID)
	// dsh-session-status: keep this session out of the finished-but-unviewed
	// reminder while the user is on it (the turn above bumped updated_at past
	// the previous view, so this restores last_viewed_at >= updated_at).
	a.markSessionViewed(ctx, sessionID)
	return nil
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

// webConfig returns the sanitized, flat configuration view served by
// GET /api/config (M10 W2, ADR D-WEB2-D): model/provider/mode, each capability
// gate's enabled flag and the web-server address. Secrets never leave —
// web_server.token is omitted entirely
// (keys live in the environment, never in this config), so a compromised
// settings page cannot leak credentials. Field names are snake_case. P5.1 adds
// the live model panel: the currently active provider's model plus the
// registered providers (id/available/model/candidates) for the pickers.
// builtinContextWindows are the known DeepSeek defaults; unknown models fall
// back to the webserver's defaultContextWindow (128k).
var builtinContextWindows = map[string]int{
	"deepseek-chat":     128000,
	"deepseek-reasoner": 128000,
}

// contextWindowOf resolves the effective model's context window for the
// ContextMeter. It honors the per-session model override (store assertion,
// same as the webserver's config handlers) and falls back to the global model.
func (a *app) contextWindowOf(sessionID string) int {
	model := ""
	if scs, ok := a.store.(store.SessionConfigStore); ok && sessionID != "" {
		if cfg, err := scs.GetSessionConfig(context.Background(), sessionID); err == nil && cfg.Model != "" {
			model = cfg.Model
		}
	}
	if model == "" {
		model = a.cfg.Model
	}
	if model == "" {
		return 0
	}
	if w, ok := builtinContextWindows[model]; ok {
		return w
	}
	return 0
}

// setTurnCancel registers the web turn's cancel func for the running turn.
func (a *app) setTurnCancel(cancel context.CancelFunc) {
	a.cancelMu.Lock()
	defer a.cancelMu.Unlock()
	a.turnCancel = cancel
}

// clearTurnCancel drops the registered cancel func once the turn settles.
func (a *app) clearTurnCancel() {
	a.cancelMu.Lock()
	defer a.cancelMu.Unlock()
	a.turnCancel = nil
}

// stopTurn cancels the running web turn (POST /api/sessions/{id}/stop). It is a
// no-op when no turn is in flight; returns an error only when the id does not
// match the session whose turn is running.
func (a *app) stopTurn(sessionID string) error {
	a.cancelMu.Lock()
	defer a.cancelMu.Unlock()
	if a.turnCancel == nil {
		return errors.New("no turn running")
	}
	if sessionID != "" && sessionID != a.currentID {
		return errors.New("turn belongs to another session")
	}
	a.turnCancel()
	return nil
}

func (a *app) webConfig() map[string]any {
	return map[string]any{
		"model":        llmProviderModel(a.cfg, a.cfg.LLM.Provider),
		"base_url":     a.cfg.BaseURL,
		"llm_provider": a.cfg.LLM.Provider,
		"mode":         a.cfg.Mode,
		"providers":    a.webProviders(), // P5.1 live model pickers

		// Capability gates (dsh 对齐: 默认全开, nil*bool→on; 显式 enabled:false 关).
		"terminal_enabled":   config.Enabled(a.cfg.Terminal.Enabled),
		"fs_enabled":         config.Enabled(a.cfg.Fs.Enabled),
		"fs_search_enabled":  config.Enabled(a.cfg.FsSearch.Enabled),
		"ralph_enabled":      config.Enabled(a.cfg.Ralph.Enabled),
		"workflow_enabled":   config.Enabled(a.cfg.Workflow.Enabled),
		"kb_enabled":         config.Enabled(a.cfg.KB.Enabled),
		"jobs_enabled":       config.Enabled(a.cfg.Jobs.Enabled),
		"subagent_enabled":   config.Enabled(a.cfg.Subagent.Enabled),
		"web_enabled":        config.Enabled(a.cfg.Web.Enabled),
		"eval_enabled":       config.Enabled(a.cfg.Eval.Enabled),
		"code_enabled":       config.Enabled(a.cfg.Code.Enabled),
		"interact_enabled":   config.Enabled(a.cfg.Interact.Enabled),
		"mcp_enabled":        config.Enabled(a.cfg.Mcp.Enabled),
		"skill_enabled":      config.Enabled(a.cfg.Skill.Enabled),
		"schedule_enabled":   config.Enabled(a.cfg.Schedule.Enabled),
		"plan_enabled":       config.Enabled(a.cfg.Plan.Enabled),
		"spill_enabled":      config.Enabled(a.cfg.Spill.Enabled),
		"compaction_enabled": config.Enabled(a.cfg.Compaction.Enabled),
		"multimodal_enabled": a.multimodalEnabled(),

		"web_server_addr":     a.cfg.WebServer.Addr,
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
	out := make([]map[string]any, 0, len(builtinProviders)+len(a.customProviders))
	// Built-in provider directory (M11-pi-ai): every provider pi-ai can
	// authenticate with an API key is listed, registered or not, so the settings
	// page can add their key. deepseek/openai/anthropic keep their
	// config-driven model/base_url (config.yaml llm.* sections); the rest carry
	// the directory default.
	for _, bp := range builtinProviders {
		model := bp.model
		baseURL := bp.baseURL
		if bp.id == "deepseek-official" || bp.id == "openai" || bp.id == "anthropic" {
			model = llmProviderModel(a.cfg, bp.id)
			baseURL = llmProviderBaseURL(a.cfg, bp.id)
		}
		registered := false
		available := false
		if p, err := reg.Get(bp.id); err == nil {
			registered = true
			available = p.Available()
		}
		out = append(out, map[string]any{
			"id":         bp.id,
			"name":       bp.name,
			"protocol":   string(bp.protocol),
			"protocol_label": protocolLabel(bp.protocol),
			"custom":     false,
			"registered": registered,
			"available":  available,
			"configured": a.providerKey(bp.id) != "",
			"model":      model,
			"base_url":   baseURL,
			"candidates": modelCandidates(bp.id),
			"env_var":    providerEnv(bp.id),
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
			"id":             cp.ID,
			"name":           cp.Name,
			"custom":         true,
			"registered":     registered,
			"available":      available,
			"configured":     a.providerKey(cp.ID) != "",
			"model":          cp.Model,
			"base_url":       cp.BaseURL,
			"candidates":     nil,
			"env_var":        llmKeyEnv(cp.ID),
			"protocol":       cp.Protocol,
			"protocol_label": protocolLabel(providerProtocol(cp.Protocol)),
			"models":         cp.Models,
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

// webSaveCustomProvider persists a custom provider declaration (M11, POST
// /api/config/provider with custom:true): it validates the profile (id/name/
// base_url + at least one model, wire protocol when given), stores
// llm.custom.<id> + an optional llm.key.<id> override and rebuilds the registry
// immediately. The protocol is one of the four supported wire protocols
// (M11-pi-ai); an empty protocol means the OpenAI-compatible default. models is
// the multi-model list (M11-pi-ai ModelListEditor 对齐): the effective default
// model is the first entry, or the legacy single model argument when the list
// is empty (a hand-declared provider needs at least one).
func (a *app) webSaveCustomProvider(ctx context.Context, id, name, baseURL, model, apiKey, protocol string, models []customModel) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.llmReg == nil {
		return fmt.Errorf("llm not registered")
	}
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	baseURL = strings.TrimSpace(baseURL)
	model = strings.TrimSpace(model)
	protocol = strings.TrimSpace(protocol)
	if id == "" || baseURL == "" {
		return errors.New("id and base_url are required")
	}
	if name == "" {
		// dsh CustomProviderCard: the display name is optional and defaults to
		// the route id (displayName.length === 0 → omitted from the profile).
		name = id
	}
	if _, ok := builtinProviderByID(id); ok {
		return errors.New("custom provider id conflicts with a built-in provider")
	}
	if !validProviderRoute(id) {
		return errors.New("provider id must start with a lowercase letter and contain only lowercase letters, digits or single '-' separators")
	}
	if protocol != "" && !validProtocol(protocol) {
		return errors.New("protocol must be one of openai-completions, anthropic-messages, google-generative-ai, openai-responses")
	}
	if protocol == "" {
		protocol = string(protocolCompletions)
	}
	// Validate the model list: at least one entry, each with a non-empty id.
	// The effective default model is the first entry; a legacy single model
	// (no list) is accepted as-is.
	if len(models) > 0 {
		cleaned := models[:0]
		for _, m := range models {
			m.ID = strings.TrimSpace(m.ID)
			if m.ID == "" {
				continue
			}
			cleaned = append(cleaned, m)
		}
		models = cleaned
	}
	if len(models) > 0 {
		model = models[0].ID
	} else if model == "" {
		return errors.New("at least one model is required")
	}
	raw, err := json.Marshal(customProviderProfile{ID: id, Name: name, BaseURL: baseURL, Model: model, Protocol: protocol, Models: models})
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
	profile := customProviderProfile{ID: id, Name: name, BaseURL: baseURL, Model: model, Protocol: protocol, Models: models}
	replaced := false
	for i := range a.customProviders {
		if a.customProviders[i].ID == id {
			a.customProviders[i] = profile
			replaced = true
			break
		}
	}
	if !replaced {
		a.customProviders = append(a.customProviders, profile)
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
	if _, ok := builtinProviderByID(id); ok {
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

// validProviderRoute reports whether id is a safe custom-provider route
// (dsh ROUTE_PATTERN /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/ 对齐): lowercase letters
// and digits, '-' only as a separator between alphanumeric runs — a leading
// letter is required so the derived credential name (<ROUTE>_API_KEY) is a
// valid shell identifier, and a trailing or doubled '-' is rejected.
func validProviderRoute(id string) bool {
	if id == "" {
		return false
	}
	if !(id[0] >= 'a' && id[0] <= 'z') {
		return false
	}
	prevDash := false
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			prevDash = false
		case c == '-':
			if prevDash {
				return false // doubled '-'
			}
			prevDash = true
		default:
			return false
		}
	}
	return !prevDash // no trailing '-'
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
func (a *app) webSubagents(ctx context.Context, sessionID string) ([]map[string]any, error) {
	if a.subagents == nil {
		return []map[string]any{}, nil
	}
	// dsh session-scoped catalog: the popover shows the requested session's
	// subagents; a blank session_id falls back to the backend's current session.
	if sessionID == "" {
		sessionID = a.currentID
	}
	children, err := a.subagents.ListChildren(ctx, sessionID)
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
// session_id scopes it to one session (dsh session-header action); blank falls
// back to the backend's current session.
func (a *app) webJobs(ctx context.Context, sessionID string) ([]map[string]any, error) {
	if a.jobs == nil {
		return []map[string]any{}, nil
	}
	if sessionID == "" {
		sessionID = a.currentID
	}
	snaps, err := a.jobs.List(ctx, sessionID)
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
