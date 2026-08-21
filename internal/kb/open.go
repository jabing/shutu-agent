// Composition helper: construct a KB provider from configuration. The M4b
// composition root (cmd/pa) calls this; keeping the gate here makes "kb 默认关
// 闭，不初始化" (D10) a first-class, tested behavior.
package kb

import (
	"fmt"

	"github.com/jabing/shutu-agent/internal/config"
)

// NewFromConfig initializes the default SQLite KB provider only when kb is
// enabled. When disabled it returns (nil, nil) and opens no database file —
// the "kb.enabled 默认关闭时不初始化" contract (D10, dispatch-m4a §3). DBPath
// is already defaulted to <data_dir>/kb/knowledge.sqlite by config.Load.
func NewFromConfig(cfg config.KBConfig) (KB, error) {
	if !config.Enabled(cfg.Enabled) {
		return nil, nil
	}
	if cfg.DBPath == "" {
		return nil, fmt.Errorf("kb: db_path must be set when kb.enabled is true")
	}
	return OpenSQLite(cfg.DBPath)
}
