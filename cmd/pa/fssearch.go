// fssearch.go — the D-GAP-1 composition-root orchestration
// (docs/dispatch-gap-1.md §5). This is where the file-content-search capability
// seam is wired into the REPL: registerFsSearch registers the fs_search tool
// into the registry when fs_search.enabled (默认关 D10). config.applyDefaults
// already whitelisted the name when fs_search.enabled was true. The tool is
// read-only and holds no resources, so there is no deferred Close; it executes
// on the serial tool path (D5) and the loop's turn/step structure is untouched
// (D4).
package main

import (
	"fmt"
	"os"

	"personal-agent/internal/fssearch"
)

// registerFsSearch wires the file-content-search seam (D-GAP-1) when
// fs_search.enabled (默认关 D10): it registers fs_search into the registry.
// config.applyDefaults already whitelisted the name when enabled. The default
// search root is the agent working directory — resolved with os.Getwd, the
// same "agent cwd" default internal/code and internal/skill use (run_command's
// empty workdir inherits it too). Read-only, no resources → no deferred Close.
func (a *app) registerFsSearch() error {
	if !a.cfg.FsSearch.Enabled {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("pa: fs_search cwd: %w", err)
	}
	if err := a.reg.Register(fssearch.NewFsSearchTool(cwd)); err != nil {
		return fmt.Errorf("pa: register %s: %w", fssearch.FsSearchToolName, err)
	}
	return nil
}
