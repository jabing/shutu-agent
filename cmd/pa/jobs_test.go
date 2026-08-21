package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jabing/shutu-agent/internal/config"
	"github.com/jabing/shutu-agent/internal/session"
	"github.com/jabing/shutu-agent/internal/tools"
)

// makeJobsApp builds a minimal app for registerJobs tests: only the fields
// registerJobs touches (cfg.Jobs, reg, log, currentID) are set.
func makeJobsApp(enabled bool) *app {
	return &app{
		cfg:       config.Config{Jobs: config.JobsConfig{Enabled: config.Bool(enabled), MaxConcurrentJobsPerOwner: 10}},
		reg:       tools.New(),
		log:       session.New(),
		currentID: "s-test",
	}
}

// jobsPolicy whitelists the five job tools so registry Execute can run them
// (in production config.applyDefaults + PolicyFromConfig do this).
func jobsPolicy() tools.Policy {
	return tools.Policy{
		Enabled:     []string{"job_start", "job_status", "job_cancel", "job_wait", "job_read"},
		Timeout:     0, // no per-tool deadline in tests
		OutputLimit: 0,
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestRegisterJobsDisabledRegistersNothing verifies the D10 gate: with
// jobs.enabled=false the composition root creates no registry and registers no
// job_* tool (dispatch-m5a-2 §4).
func TestRegisterJobsDisabledRegistersNothing(t *testing.T) {
	app := makeJobsApp(false)
	if err := app.registerJobs(); err != nil {
		t.Fatalf("registerJobs: %v", err)
	}
	if app.jobs != nil {
		t.Fatal("jobs registry must be nil when jobs.enabled=false")
	}
	for _, spec := range app.reg.Specs() {
		if strings.HasPrefix(spec.Name, "job_") {
			t.Fatalf("job tool %q registered while jobs disabled", spec.Name)
		}
	}
}

// TestRegisterJobsEnabledRegistersAndValidates verifies the enabled path: the
// registry is created, all five job_* tools are registered, D7 schema
// validation rejects bad arguments at the Execute gate, valid calls flow
// through, and the job/start event lands in the session log (D3 wiring).
func TestRegisterJobsEnabledRegistersAndValidates(t *testing.T) {
	app := makeJobsApp(true)
	app.reg.SetPolicy(jobsPolicy())
	if err := app.registerJobs(); err != nil {
		t.Fatalf("registerJobs: %v", err)
	}
	defer app.jobs.Close()
	if app.jobs == nil {
		t.Fatal("jobs registry must be created when jobs.enabled=true")
	}
	specs := app.reg.Specs()
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.Name)
	}
	for _, want := range []string{"job_start", "job_status", "job_cancel", "job_wait", "job_read"} {
		if !containsStr(names, want) {
			t.Fatalf("registered tools %v lack %q", names, want)
		}
	}

	// D7: bad arguments are rejected before any tool code runs.
	for _, tc := range []struct {
		name string
		args string
	}{
		{"job_status", `{}`},                           // missing required id
		{"job_status", `{"id":123}`},                   // id must be a string
		{"job_start", `{}`},                            // missing required command
		{"job_start", `{"command":""}`},                // empty command
		{"job_cancel", `{}`},                           // missing required id
		{"job_wait", `{"id":"x","timeout_seconds":0}`}, // timeout must be >= 1
		{"job_read", `{"id":false}`},                   // wrong id type
		{"job_status", `{"id":"x","extra":1}`},         // additional properties rejected
	} {
		if _, err := app.reg.Execute(context.Background(), tc.name, json.RawMessage(tc.args)); err == nil {
			t.Errorf("%s with args %s must be rejected (D7)", tc.name, tc.args)
		}
	}

	// A valid call flows through: start a real command, then observe it.
	res, err := app.reg.Execute(context.Background(), "job_start", json.RawMessage(`{"command":"echo d7-ok"}`))
	if err != nil {
		t.Fatalf("job_start via registry: %v", err)
	}
	if !strings.Contains(res.Output, "started job ") {
		t.Fatalf("job_start output = %q, want started job ...", res.Output)
	}
	if _, err := app.reg.Execute(context.Background(), "job_status", json.RawMessage(`{"id":"bash-1"}`)); err != nil {
		t.Fatalf("job_status via registry: %v", err)
	}
	// The job/start event was appended to the session log (D3).
	foundStart := false
	for _, ev := range app.log.Events() {
		if ev.Type == session.EventJobStart {
			foundStart = true
		}
	}
	if !foundStart {
		t.Fatal("job/start event missing from the session log after job_start")
	}
}
