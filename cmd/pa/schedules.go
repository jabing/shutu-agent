// schedules.go — the M6a-2 composition-root orchestration (dispatch-m6a-2
// §4/§5). This is where the schedule capability seam is wired into the REPL:
// registerSchedules creates the in-memory Provider + Engine and registers the
// three schedule_* tools when schedule.enabled (D10), wires the D3 event sink
// so schedule/create, schedule/list and schedule/delete are appended to the
// active session log, and the loop's "schedule" pre-step injector (registered
// after the skill injector) advances the schedule clock on the serial path —
// a due trigger is turned into a schedule/fire event and, when jobs is
// enabled, a background job executing the trigger's payload. The loop's
// turn/step structure is untouched (D4) and there is deliberately no
// background ticker (D5): every side effect happens inside the pre-step
// injector on the serial path, and the fired job goroutine never touches the
// session log (the fire event is appended before the job is enqueued).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jabing/shutu-agent/internal/jobs"
	"github.com/jabing/shutu-agent/internal/llm"
	"github.com/jabing/shutu-agent/internal/loop"
	"github.com/jabing/shutu-agent/internal/schedule"
	"github.com/jabing/shutu-agent/internal/session"
	"github.com/jabing/shutu-agent/internal/tools"
)

// registerSchedules creates the in-memory Provider + Engine and registers the
// three schedule_* tools when schedule.enabled, and wires the D3 event sink.
// When schedule is disabled it creates nothing and registers nothing (D10,
// mirrors registerJobs/registerSkills).
func (a *app) registerSchedules() error {
	if !a.cfg.Schedule.Enabled {
		return nil
	}
	prov := schedule.NewMemProvider()
	eng := schedule.NewEngine(prov)
	a.schedules = eng
	// D3 event sink: schedule/* events are appended to the active session log.
	// The callback only ever runs inside a schedule_* tool Execute or the
	// pre-step fire injector — the serial main-loop path (D5). a.log is read
	// at call time, so a session switch (/new, /resume) is honored the same
	// way as the kb/jobs/subagent/skill wiring.
	onEvent := func(typ string, data any) {
		if _, err := a.log.Append(typ, data); err != nil {
			fmt.Fprintln(os.Stderr, "pa: "+typ+" event:", err)
		}
	}
	st := schedule.NewScheduleTools(eng, onEvent)
	for _, t := range []tools.Tool{
		st.Create(),
		st.List(),
		st.Delete(),
	} {
		if err := a.reg.Register(t); err != nil {
			return fmt.Errorf("pa: register %s: %w", t.Name(), err)
		}
	}
	return nil
}

// scheduleInjector builds the "schedule" pre-step injector (ADR 决策 M6a /
// dispatch-m6a-2 §4): once per turn — after user/message is appended, before
// the first step's model request — it advances the schedule clock by one Tick.
// It is appended after the skill injector in preStepInjectors so the ordering
// is recall → compaction → skill → schedule.
func (a *app) scheduleInjector() loop.PreStepInjector {
	return loop.PreStepInjector{
		Name:   "schedule",
		Inject: a.schedulePreStep,
	}
}

// schedulePreStep is the "schedule" pre-step injector body. It calls
// Engine.Tick(now) once (a pure advancement; no background ticker, D5) and,
// for every schedule the engine reports as due, appends a schedule/fire event
// (bounded payload, D3) and — when a job registry is wired — enqueues a
// background job executing the trigger's payload with owner = the current
// session. With no job engine the fire event is still logged. Every append
// happens here on the serial pre-step path; a failing tick is surfaced as a
// stderr warning and contributes no context (fail-open, the same contract as
// the kb recall / skill catalog injectors). The injector returns no context
// message: schedule/fire is log-only and the fired payload reaches the model
// through the enqueued job's tool/result.
func (a *app) schedulePreStep(ctx context.Context, _ string) []llm.Message {
	if a.schedules == nil {
		return nil
	}
	fired, err := a.schedules.Tick(ctx, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "[schedule tick failed open]", err)
		return nil
	}
	if len(fired) == 0 {
		return nil
	}
	// The engine returns ids only; re-list to carry each fired schedule's
	// payload in the event and the job (serial path, so the table is stable).
	all, err := a.schedules.List(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[schedule list failed open]", err)
		all = nil
	}
	payloads := make(map[string]string, len(all))
	for _, s := range all {
		payloads[s.ID] = s.Payload
	}
	for _, id := range fired {
		payload := payloads[id]
		if _, err := a.log.Append(session.EventScheduleFire, session.NewScheduleFire(id, payload)); err != nil {
			fmt.Fprintln(os.Stderr, "pa: schedule/fire event:", err)
		}
		// Enqueue a background job executing the payload (D5: the fire event
		// is appended above on the serial path; the job goroutine only carries
		// the payload, never the session log). No job engine ⇒ the fire is
		// logged only.
		if a.jobs != nil {
			if _, err := a.jobs.Start(ctx, jobs.JobStart{
				Kind:         "schedule",
				Label:        "schedule " + id + " fired",
				OwnerSession: a.currentID,
				Run:          scheduleFireRun(payload),
			}); err != nil {
				fmt.Fprintln(os.Stderr, "pa: enqueue schedule fire job:", err)
			}
		}
	}
	return nil
}

// scheduleFireRun is the Run body of a fired schedule's background job
// (dispatch-m6a-2 §4). M6a-2 v1 has no executor for arbitrary payload
// instruction text, so the job settles immediately and records the payload as
// its output — job_read surfaces exactly what fired. Cancellation is observed
// through the job context (jobs registry cancel/close semantics); the job
// goroutine never touches the session log (D5).
func scheduleFireRun(payload string) func(ctx context.Context) (jobs.JobOutcome, error) {
	return func(ctx context.Context) (jobs.JobOutcome, error) {
		if err := ctx.Err(); err != nil {
			return jobs.JobOutcome{Status: jobs.StatusKilled, Detail: "schedule fire job cancelled"}, nil
		}
		return jobs.JobOutcome{Status: jobs.StatusCompleted, Detail: "schedule fired", Output: payload}, nil
	}
}
