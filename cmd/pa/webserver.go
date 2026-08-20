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
