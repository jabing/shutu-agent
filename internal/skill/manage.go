// manage.go — the M5d-2 skill-management surface for the web settings page
// (dsh-skill-mcp-panel 对齐, user 2026-09). The catalog-facing provider stays
// read-only (List/Get); this module scans the same roots with the manager's
// eyes: recursive directory traversal (with 分类 folders and rel paths),
// disabled entries (SKILL.md.disabled / <name>.md.disabled) kept visible so the
// page can re-enable them, and two scopes — global (user-dsh + custom dirs) and
// project (project-dsh + project-agents). Every write (enable/disable rename,
// delete, add, migrate) is a plain filesystem operation on the skill files; the
// catalog provider re-reads the directory each turn, so changes take effect
// without a restart (the same hot semantics as the plugin's *.disabled rename).
//
// Scope is a single "project root vs user root" split — the plugin's per-workspace
// rows collapse to our two roots because shutu is a single-cwd agent (有差异对齐,
// 显式记录). Bundled/read-only skills do not exist here (no deployment-bundled
// skill directory), so every discovered entry is manageable.
package skill

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// yamlUnmarshal is the package-local YAML decode (manage.go needs it for the
// strict add validation; filesystem.go already imports the same parser).
func yamlUnmarshal(data []byte, out any) error {
	return yaml.Unmarshal(data, out)
}

// decodeBase64Std decodes standard base64.
func decodeBase64Std(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// DISABLED_SUFFIX is the hot-disable marker (dsh-skill-mcp-panel 对齐): renaming
// a skill file to <file>.disabled hides it from the catalog provider (its
// scanner looks for SKILL.md / <name>.md), so disable takes effect immediately;
// renaming back re-enables it.
const DISABLED_SUFFIX = ".disabled"

// Skill scopes (plugin workspace split collapsed to our two roots).
const (
	ScopeGlobal  = "global"  // user-dsh + custom dirs (user-level roots)
	ScopeProject = "project" // project-dsh + project-agents (project roots)
)

// ManageEntry is one discovered skill as the settings page manages it: identity
// plus the durable file facts that make enable/disable/delete/migrate exact.
type ManageEntry struct {
	Name           string
	Description    string
	WhenToUse      string
	Enabled        bool
	Kind           string // "bundle" | "flat"
	File           string // actual file (may carry the .disabled suffix)
	DirBundle      bool
	Source         string
	Scope          string
	Rel            string
	ModelInvocable bool
	UserInvocable  bool
}

// Manager scans the filesystem skill roots with management in mind. It shares
// the provider's root resolution (FSOpts), so the page sees exactly what the
// catalog would serve plus the disabled entries.
type Manager struct {
	fs *Filesystem
}

// NewManager builds a Manager from the same roots the provider resolves.
func NewManager(opts FSOpts) (*Manager, error) {
	fs, err := NewFilesystem(opts)
	if err != nil {
		return nil, err
	}
	return &Manager{fs: fs}, nil
}

// rootScope reports which scope a root belongs to. user-dsh and custom dirs are
// global (user-level); project-dsh and project-agents are project.
func rootScope(source string) string {
	switch source {
	case SourceProjectDSH, SourceProjectAgents:
		return ScopeProject
	default:
		return ScopeGlobal
	}
}

// ListAll returns every skill entry across all roots (enabled + disabled), each
// carrying its scope, source and rel path. Entries are sorted by scope then name
// for stable page rendering.
func (m *Manager) ListAll(ctx context.Context) ([]ManageEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []ManageEntry
	for _, root := range m.fs.roots {
		entries, err := m.scanRootRecursive(ctx, root, root.path, "", 0)
		if err != nil {
			return nil, fmt.Errorf("skill: scan %s root %s: %w", root.source, root.path, err)
		}
		out = append(out, entries...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// maxRecurseDepth bounds the recursive traversal against symlink loops (the
// plugin's MAX_RECURSE_DEPTH).
const maxRecurseDepth = 8

// skipDirNames are hidden/dependency directories the scanner never descends
// into (mirrors the plugin and the provider's discovery posture).
var skipDirNames = map[string]bool{
	"node_modules": true, ".git": true, ".hg": true, ".svn": true,
}

// scanRootRecursive walks a root directory and returns its skill entries: a
// directory holding SKILL.md (or *.disabled) is a bundle and is not descended
// into; any other directory is a 分类 folder and is traversed (rel grows);
// flat *.md files are collected only at the root level.
func (m *Manager) scanRootRecursive(ctx context.Context, root fsRoot, dir, rel string, depth int) ([]ManageEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if depth >= maxRecurseDepth {
		return nil, nil
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })

	var out []ManageEntry
	for _, item := range items {
		name := item.Name()
		if strings.HasPrefix(name, ".") || skipDirNames[name] {
			continue
		}
		full := filepath.Join(dir, name)
		if item.IsDir() {
			md := filepath.Join(full, "SKILL.md")
			disabled := md + DISABLED_SUFFIX
			mdExists := fileExists(md)
			disabledExists := fileExists(disabled)
			if mdExists || disabledExists {
				file := md
				if !mdExists {
					file = disabled
				}
				parsed := readManageFile(file, name)
				out = append(out, ManageEntry{
					Name:           parsedName(parsed, name),
					Description:    parsedDescription(parsed),
					WhenToUse:      parsedWhenToUse(parsed),
					Enabled:        mdExists,
					Kind:           "bundle",
					File:           file,
					DirBundle:      true,
					Source:         root.source,
					Scope:          rootScope(root.source),
					Rel:            joinRel(rel, name),
					ModelInvocable: parsed.modelInvocable,
					UserInvocable:  parsed.userInvocable,
				})
				continue
			}
			sub, err := m.scanRootRecursive(ctx, root, full, joinRel(rel, name), depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
			continue
		}
		if rel != "" || !strings.HasSuffix(name, ".md") {
			continue
		}
		if strings.HasSuffix(name, ".md"+DISABLED_SUFFIX) {
			parsed := readManageFile(full, strings.TrimSuffix(name, ".md"+DISABLED_SUFFIX))
			out = append(out, ManageEntry{
				Name:           parsedName(parsed, strings.TrimSuffix(name, ".md"+DISABLED_SUFFIX)),
				Description:    parsedDescription(parsed),
				WhenToUse:      parsedWhenToUse(parsed),
				Enabled:        false,
				Kind:           "flat",
				File:           full,
				DirBundle:      false,
				Source:         root.source,
				Scope:          rootScope(root.source),
				Rel:            "",
				ModelInvocable: parsed.modelInvocable,
				UserInvocable:  parsed.userInvocable,
			})
			continue
		}
		if strings.HasSuffix(name, ".md") {
			parsed := readManageFile(full, strings.TrimSuffix(name, ".md"))
			out = append(out, ManageEntry{
				Name:           parsedName(parsed, strings.TrimSuffix(name, ".md")),
				Description:    parsedDescription(parsed),
				WhenToUse:      parsedWhenToUse(parsed),
				Enabled:        true,
				Kind:           "flat",
				File:           full,
				DirBundle:      false,
				Source:         root.source,
				Scope:          rootScope(root.source),
				Rel:            "",
				ModelInvocable: parsed.modelInvocable,
				UserInvocable:  parsed.userInvocable,
			})
		}
	}
	return out, nil
}

// joinRel joins a rel path segment, empty-safe.
func joinRel(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

// fileExists reports whether path exists as a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// parsedManage is a lenient frontmatter read for list display.
type parsedManage struct {
	name           string
	description    string
	whenToUse      string
	modelInvocable bool
	userInvocable  bool
}

// readManageFile reads a skill file for display; unreadable or unparseable
// content yields an entry with the directory/file name and no description
// (disabled entries must stay manageable).
func readManageFile(path, fallback string) parsedManage {
	raw, err := os.ReadFile(path)
	if err != nil {
		return parsedManage{name: fallback}
	}
	return parseManageText(string(raw), fallback)
}

// parseManageText parses skill text leniently for display. A missing frontmatter
// falls back to the entry name and the first body line (like parseSkillFile).
func parseManageText(raw, fallback string) parsedManage {
	out := parsedManage{name: fallback, modelInvocable: true, userInvocable: true}
	body := strings.TrimPrefix(raw, "\uFEFF")
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	front := ""
	if f, b, ok := splitFrontmatter(body); ok {
		front, body = f, b
	}
	meta := map[string]any{}
	if front != "" {
		if err := yamlUnmarshal([]byte(front), &meta); err != nil {
			return out
		}
	}
	if n, ok := stringField(meta, "name"); ok && IsSkillName(n) {
		out.name = n
	}
	if d, ok := stringField(meta, "description"); ok {
		out.description = d
	}
	if w, ok := stringField(meta, "whenToUse"); ok {
		out.whenToUse = w
	}
	if out.description == "" {
		out.description = firstBodyLine(strings.TrimSpace(body))
	}
	if v, present := frontmatterBool(meta, "disable-model-invocation"); present && v {
		out.modelInvocable = false
	}
	if v, present := frontmatterBool(meta, "user-invocable"); present && !v {
		out.userInvocable = false
	}
	return out
}

func parsedName(p parsedManage, fallback string) string {
	if p.name != "" {
		return p.name
	}
	return fallback
}

func parsedDescription(p parsedManage) string { return p.description }
func parsedWhenToUse(p parsedManage) string   { return p.whenToUse }

// GetEntry returns the entry for name in the given scope, or nil. Same-name
// skills may exist in both scopes; scoped operations must target the exact one
// (作用域化管理). It is the exported read used by the web content view.
func (m *Manager) GetEntry(ctx context.Context, name, scope string) (*ManageEntry, error) {
	return m.entryInScope(ctx, name, scope)
}

// entryInScope returns the entry for name in the given scope, or nil. Same-name
// skills may exist in both scopes; scoped operations must target the exact one
// (作用域化管理).
func (m *Manager) entryInScope(ctx context.Context, name, scope string) (*ManageEntry, error) {
	all, err := m.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name && all[i].Scope == scope {
			return &all[i], nil
		}
	}
	return nil, nil
}

// Content reads the full skill text (the raw file, disabled or not) for name in
// scope. Returns an empty string and nil when the skill is absent.
func (m *Manager) Content(ctx context.Context, name, scope string) (string, error) {
	entry, err := m.entryInScope(ctx, name, scope)
	if err != nil {
		return "", err
	}
	if entry == nil {
		return "", nil
	}
	raw, err := os.ReadFile(entry.File)
	if err != nil {
		return "", fmt.Errorf("skill: read %s: %w", entry.File, err)
	}
	return string(raw), nil
}

// SetEnabled hot-enables/disables a skill in the exact scope by renaming its
// file *.disabled (or back). Disabling the same-name copy in another scope is
// never touched.
func (m *Manager) SetEnabled(ctx context.Context, name, scope string, enabled bool) error {
	entry, err := m.entryInScope(ctx, name, scope)
	if err != nil {
		return err
	}
	if entry == nil {
		return fmt.Errorf("技能 %q 在作用域 %q 中不存在", name, scope)
	}
	if entry.Enabled == enabled {
		return nil
	}
	var target string
	if enabled {
		if !strings.HasSuffix(entry.File, DISABLED_SUFFIX) {
			return fmt.Errorf("技能 %q 无法启用：文件路径异常", name)
		}
		target = strings.TrimSuffix(entry.File, DISABLED_SUFFIX)
	} else {
		target = entry.File + DISABLED_SUFFIX
	}
	if fileExists(target) {
		return fmt.Errorf("目标文件已存在：%s", target)
	}
	if err := os.Rename(entry.File, target); err != nil {
		return fmt.Errorf("技能 %q %s失败：%w", name, map[bool]string{true: "启用", false: "停用"}[enabled], err)
	}
	return nil
}

// Delete permanently removes a skill's files (bundle → whole directory, flat →
// the file) in the exact scope.
func (m *Manager) Delete(ctx context.Context, name, scope string) error {
	entry, err := m.entryInScope(ctx, name, scope)
	if err != nil {
		return err
	}
	if entry == nil {
		return fmt.Errorf("技能 %q 在作用域 %q 中不存在", name, scope)
	}
	if entry.DirBundle {
		if err := os.RemoveAll(filepath.Dir(entry.File)); err != nil {
			return fmt.Errorf("删除技能 %q 失败：%w", name, err)
		}
		return nil
	}
	if err := os.Remove(entry.File); err != nil {
		return fmt.Errorf("删除技能 %q 失败：%w", name, err)
	}
	return nil
}

// rootForScope returns the target skill root for a scope: global → the first
// user-dsh root (falling back to the first custom dir), project → the first
// project-dsh root. The returned source labels the migrated entries.
func (m *Manager) rootForScope(scope string) (string, string) {
	userDSH, custom, projectDSH, projectAgents := "", "", "", ""
	for _, root := range m.fs.roots {
		switch root.source {
		case SourceUserDSH:
			if userDSH == "" {
				userDSH = root.path
			}
		case SourceCustom:
			if custom == "" {
				custom = root.path
			}
		case SourceProjectDSH:
			if projectDSH == "" {
				projectDSH = root.path
			}
		case SourceProjectAgents:
			if projectAgents == "" {
				projectAgents = root.path
			}
		}
	}
	if scope == ScopeProject {
		if projectDSH != "" {
			return projectDSH, SourceProjectDSH
		}
		if projectAgents != "" {
			return projectAgents, SourceProjectAgents
		}
		return "", ""
	}
	if userDSH != "" {
		return userDSH, SourceUserDSH
	}
	if custom != "" {
		return custom, SourceCustom
	}
	return "", ""
}

// Migrate moves or copies a skill from its current scope to a target scope.
// mode is "copy" (keep the source) or "move" (delete the source after a
// successful landing; a failed source delete rolls the target back so no
// duplicate survives).
func (m *Manager) Migrate(ctx context.Context, name, from, to, mode string) error {
	if mode != "copy" && mode != "move" {
		return fmt.Errorf("迁移模式必须是 copy 或 move")
	}
	entry, err := m.entryInScope(ctx, name, from)
	if err != nil {
		return err
	}
	if entry == nil {
		return fmt.Errorf("技能 %q 在作用域 %q 中不存在", name, from)
	}
	targetRoot, _ := m.rootForScope(to)
	if targetRoot == "" {
		return fmt.Errorf("作用域 %q 没有可写的技能目录", to)
	}
	sourceDir := entry.File
	if entry.DirBundle {
		sourceDir = filepath.Dir(entry.File)
	}
	// The target keeps the source file's disabled state: a disabled entry
	// migrates as its *.disabled file so the catalog still hides it.
	targetName := filepath.Base(entry.File)
	target := filepath.Join(targetRoot, targetName)
	if entry.DirBundle {
		target = filepath.Join(targetRoot, entry.Name)
		if !entry.Enabled {
			target += DISABLED_SUFFIX
		}
		targetName = filepath.Base(target)
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return err
	}
	if fileExists(target) || dirExists(target) {
		return fmt.Errorf("目标位置已存在同名技能：%s", target)
	}
	// Move fast path: same-volume rename is atomic and cheap.
	if mode == "move" {
		if err := os.Rename(sourceDir, target); err == nil {
			return nil
		}
		// Cross-volume or busy: fall through to copy + delete.
	}
	// Copy path: land the entity, then (move) delete the source with rollback.
	if err := copySkillTree(sourceDir, target, entry.DirBundle); err != nil {
		return fmt.Errorf("复制技能 %q 失败（已回滚）：%w", name, err)
	}
	if mode == "move" {
		if err := os.RemoveAll(sourceDir); err != nil {
			_ = os.RemoveAll(target) // roll back the copy
			return fmt.Errorf("技能 %q 已复制到目标但无法删除源文件（可能被占用），已回滚新副本：%w", name, err)
		}
	}
	return nil
}

// dirExists reports whether path is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// copySkillTree copies a bundle directory or a single file to target.
func copySkillTree(source, target string, bundle bool) error {
	if bundle {
		return copyDir(source, target)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}

// copyDir recursively copies a directory tree.
func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	items, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, item := range items {
		s := filepath.Join(src, item.Name())
		d := filepath.Join(dst, item.Name())
		if item.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(s)
			if err != nil {
				return err
			}
			if err := os.WriteFile(d, data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- groups (plugin 显示配置, not skill files) ------------------------------

// groupFile returns the groups config path under the user-dsh root
// (<root>/.system/skill-viewer/groups.json, mirroring the plugin's location).
func (m *Manager) groupFile() string {
	root, _ := m.rootForScope(ScopeGlobal)
	if root == "" {
		return ""
	}
	return filepath.Join(root, ".system", "skill-viewer", "groups.json")
}

// GroupRow is one wire group: an id, a display name, and per-scope member lists.
type GroupRow struct {
	ID     string              `json:"id"`
	Name   string              `json:"name"`
	Scopes map[string][]string `json:"scopes"`
}

// Groups returns the persisted group config, sorted by name.
func (m *Manager) Groups() ([]GroupRow, error) {
	groups, err := m.loadGroups()
	if err != nil {
		return nil, err
	}
	rows := make([]GroupRow, 0, len(groups))
	for id, g := range groups {
		rows = append(rows, GroupRow{ID: id, Name: g.name, Scopes: g.scopes})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// groupEntry is the persisted shape of one group.
type groupEntry struct {
	name   string
	scopes map[string][]string
}

// loadGroups reads the group config; a missing file yields an empty map.
func (m *Manager) loadGroups() (map[string]groupEntry, error) {
	path := m.groupFile()
	if path == "" {
		return map[string]groupEntry{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]groupEntry{}, nil
		}
		return nil, err
	}
	var stored map[string]struct {
		Name   string              `json:"name"`
		Scopes map[string][]string `json:"scopes"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("skill: read groups %s: %w", path, err)
	}
	out := make(map[string]groupEntry, len(stored))
	for id, g := range stored {
		if g.Scopes == nil {
			g.Scopes = map[string][]string{}
		}
		out[id] = groupEntry{name: g.Name, scopes: g.Scopes}
	}
	return out, nil
}

// saveGroups persists the group config atomically.
func (m *Manager) saveGroups(groups map[string]groupEntry) error {
	path := m.groupFile()
	if path == "" {
		return errors.New("skill: no global root to store groups")
	}
	stored := make(map[string]struct {
		Name   string              `json:"name"`
		Scopes map[string][]string `json:"scopes"`
	}, len(groups))
	for id, g := range groups {
		if g.scopes == nil {
			g.scopes = map[string][]string{}
		}
		stored[id] = struct {
			Name   string              `json:"name"`
			Scopes map[string][]string `json:"scopes"`
		}{Name: g.name, Scopes: g.scopes}
	}
	raw, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SaveGroup creates or updates a group: a new id is minted (timestamp-based,
// stable within a session), the scope key normalizes to "global" when empty,
// and the member list replaces that scope's members.
func (m *Manager) SaveGroup(id, name, scope string, names []string) ([]GroupRow, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("分组名称不能为空")
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "global"
	}
	groups, err := m.loadGroups()
	if err != nil {
		return nil, err
	}
	if id == "" {
		id = fmt.Sprintf("g-%d", time.Now().UnixMilli())
	}
	g, ok := groups[id]
	if !ok {
		g = groupEntry{name: name, scopes: map[string][]string{}}
	}
	g.name = name
	if g.scopes == nil {
		g.scopes = map[string][]string{}
	}
	g.scopes[scope] = append([]string(nil), names...)
	groups[id] = g
	if err := m.saveGroups(groups); err != nil {
		return nil, err
	}
	return m.Groups()
}

// DeleteGroup removes a group by id.
func (m *Manager) DeleteGroup(id string) ([]GroupRow, error) {
	groups, err := m.loadGroups()
	if err != nil {
		return nil, err
	}
	if _, ok := groups[id]; !ok {
		return nil, fmt.Errorf("分组 %q 不存在", id)
	}
	delete(groups, id)
	if err := m.saveGroups(groups); err != nil {
		return nil, err
	}
	return m.Groups()
}

// ---- add (bundle / flat / zip) ---------------------------------------------

// AddFile is one uploaded skill file for AddSkill (path relative to the skill
// root + base64 data). It is exported so the composition root can build the
// add payload from the wire SkillFile.
type AddFile struct {
	Path   string `json:"path"`
	Base64 string `json:"base64"`
}

// AddSkill imports new skills into a scope (global default). kind is "bundle"
// (one skill folder with a top-level SKILL.md), "flat" (one or more .md files,
// each a skill) or "zip" (auto-detected). Uploads are validated strictly before
// anything is written; a failed write rolls back every landing target.
func (m *Manager) AddSkill(ctx context.Context, kind string, files []AddFile, scope string) (string, error) {
	if len(files) == 0 {
		return "", errors.New("没有可添加的文件")
	}
	if len(files) > 200 {
		return "", errors.New("文件数量过多（最多 200 个）")
	}
	decoded := make([]struct {
		path string
		data []byte
	}, 0, len(files))
	for _, f := range files {
		data, err := decodeBase64(f.Base64)
		if err != nil {
			return "", fmt.Errorf("文件内容解码失败：%s", f.Path)
		}
		decoded = append(decoded, struct {
			path string
			data []byte
		}{path: strings.ReplaceAll(f.Path, "\\", "/"), data: data})
	}

	// zip mode: unpack and auto-detect — a single top-level folder with SKILL.md
	// is a bundle, flat .md files are separate skills; anything else is refused.
	if kind == "zip" {
		unzipped := make([]struct {
			path string
			data []byte
		}, 0, len(decoded))
		for _, f := range decoded {
			if !strings.HasSuffix(strings.ToLower(f.path), ".zip") {
				unzipped = append(unzipped, f)
				continue
			}
			entries, err := unzipSkillArchive(f.data)
			if err != nil {
				return "", fmt.Errorf("无法解析压缩包 %s：%w", f.path, err)
			}
			unzipped = append(unzipped, entries...)
		}
		if len(unzipped) == 0 {
			return "", errors.New("压缩包中没有可用的文件")
		}
		if len(unzipped) > 200 {
			return "", errors.New("压缩包内文件数量过多（最多 200 个）")
		}
		tops := map[string]bool{}
		hasNested := false
		for _, f := range unzipped {
			first := f.path
			if i := strings.IndexByte(f.path, '/'); i >= 0 {
				first = f.path[:i]
				hasNested = true
			}
			tops[first] = true
		}
		if len(tops) == 1 && hasNested {
			top := ""
			for t := range tops {
				top = t
			}
			hasSkill := false
			for _, f := range unzipped {
				if f.path == top+"/SKILL.md" {
					hasSkill = true
				}
			}
			if !hasSkill {
				return "", errors.New("压缩包结构无法识别：唯一的顶层文件夹缺少 SKILL.md 文件")
			}
			kind = "bundle"
		} else if !hasNested {
			kind = "flat"
		} else {
			return "", errors.New("压缩包结构无法识别：应为「一个技能文件夹（内含 SKILL.md）」或「若干 .md 文件」")
		}
		decoded = unzipped
	}

	var total int
	for _, f := range decoded {
		total += len(f.data)
	}
	if total > 8*1024*1024 {
		return "", errors.New("技能总大小超过 8MB 上限（含解压后内容）")
	}
	// Refuse unsafe relative paths before anything is written.
	for _, f := range decoded {
		if strings.HasPrefix(f.path, "/") {
			return "", fmt.Errorf("非法文件路径：%s", f.path)
		}
		for _, seg := range strings.Split(f.path, "/") {
			if seg == ".." || seg == "." {
				return "", fmt.Errorf("非法文件路径：%s", f.path)
			}
		}
	}

	targetRoot, _ := m.rootForScope(scope)
	if targetRoot == "" {
		return "", fmt.Errorf("作用域 %q 没有可写的技能目录", scope)
	}

	// Determine the canonical skills to write.
	type skillWrite struct {
		name   string
		writes []struct {
			relative string
			data     []byte
		}
		target string
	}
	var skills []skillWrite
	if kind == "bundle" {
		tops := map[string]bool{}
		for _, f := range decoded {
			first := f.path
			if i := strings.IndexByte(f.path, '/'); i >= 0 {
				first = f.path[:i]
			}
			tops[first] = true
		}
		if len(tops) != 1 {
			return "", errors.New("技能文件夹结构不正确：所有文件应位于同一个文件夹内")
		}
		top := ""
		for t := range tops {
			top = t
		}
		var skillFile *struct {
			path string
			data []byte
		}
		for i := range decoded {
			if decoded[i].path == top+"/SKILL.md" {
				skillFile = &decoded[i]
			}
		}
		if skillFile == nil {
			return "", errors.New("技能文件夹缺少顶层的 SKILL.md 文件")
		}
		validation, err := validateFrontmatterStrict(string(skillFile.data))
		if err != nil {
			return "", fmt.Errorf("技能格式不符合要求：%w", err)
		}
		writes := make([]struct {
			relative string
			data     []byte
		}, 0, len(decoded))
		for _, f := range decoded {
			rel := strings.TrimPrefix(f.path, top+"/")
			data := f.data
			if strings.HasSuffix(strings.ToLower(rel), ".md") {
				data = []byte(normalizeSkillText(string(data)))
			}
			writes = append(writes, struct {
				relative string
				data     []byte
			}{relative: rel, data: data})
		}
		skills = append(skills, skillWrite{name: validation, writes: writes})
	} else {
		for _, f := range decoded {
			flatName := f.path
			if i := strings.LastIndexByte(f.path, '/'); i >= 0 {
				flatName = f.path[i+1:]
			}
			if !strings.HasSuffix(strings.ToLower(flatName), ".md") {
				return "", errors.New("技能文件必须是 .md 文件")
			}
			validation, err := validateFrontmatterStrict(string(f.data))
			if err != nil {
				return "", fmt.Errorf("技能格式不符合要求（%s）：%w", flatName, err)
			}
			skills = append(skills, skillWrite{
				name: validation,
				writes: []struct {
					relative string
					data     []byte
				}{{relative: flatName, data: []byte(normalizeSkillText(string(f.data)))}},
			})
		}
	}
	if len(skills) == 0 {
		return "", errors.New("没有可添加的技能")
	}

	// Batch validation: duplicate names in the batch and against existing skills
	// are all refused before any write.
	batchNames := map[string]bool{}
	for _, s := range skills {
		if batchNames[s.name] {
			return "", fmt.Errorf("本次选择的技能中有重名：%q", s.name)
		}
		batchNames[s.name] = true
	}
	existing, err := m.ListAll(ctx)
	if err != nil {
		return "", err
	}
	for _, s := range skills {
		for _, e := range existing {
			if e.Name == s.name {
				state := "已启用"
				if !e.Enabled {
					state = "已停用"
				}
				return "", fmt.Errorf("同名技能 %q 已存在（%s）", s.name, state)
			}
		}
	}
	// Landing-path collisions are also refused up front.
	planned := map[string]bool{}
	for i := range skills {
		target := filepath.Join(targetRoot, skills[i].name)
		if kind != "bundle" {
			target = filepath.Join(targetRoot, skills[i].writes[0].relative)
		}
		key := strings.ToLower(target)
		if planned[key] {
			return "", fmt.Errorf("本次选择的技能落盘路径相同：%s", target)
		}
		planned[key] = true
		if fileExists(target) || dirExists(target) {
			return "", fmt.Errorf("目标路径已存在：%s", target)
		}
		skills[i].target = target
	}

	// Write each skill into its own staging dir, then rename into place; any
	// failure rolls back every target written so far.
	stagingBase := filepath.Join(targetRoot, fmt.Sprintf(".dsh-skill-staging-%d", time.Now().UnixNano()))
	var written []string
	for i := range skills {
		s := &skills[i]
		staging := filepath.Join(stagingBase, fmt.Sprint(i))
		for _, w := range s.writes {
			p := filepath.Join(staging, w.relative)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				cleanupWritten(stagingBase, written)
				return "", fmt.Errorf("写入技能文件失败（已回滚）：%w", err)
			}
			if err := os.WriteFile(p, w.data, 0o644); err != nil {
				cleanupWritten(stagingBase, written)
				return "", fmt.Errorf("写入技能文件失败（已回滚）：%w", err)
			}
		}
		if kind == "bundle" {
			if err := os.Rename(staging, s.target); err != nil {
				cleanupWritten(stagingBase, written)
				return "", fmt.Errorf("写入技能文件失败（已回滚）：%w", err)
			}
		} else {
			staged := filepath.Join(staging, s.writes[0].relative)
			if err := os.Rename(staged, s.target); err != nil {
				cleanupWritten(stagingBase, written)
				return "", fmt.Errorf("写入技能文件失败（已回滚）：%w", err)
			}
			_ = os.RemoveAll(staging)
		}
		written = append(written, s.target)
	}
	_ = os.RemoveAll(stagingBase)
	// No registry polling needed: the catalog provider re-reads the directory
	// every turn, so a successful write is a discovered skill (replaces the
	// plugin's waitForDiscovery with our immediate-consistency model).
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.name
	}
	return strings.Join(names, ", "), nil
}

// cleanupWritten removes the staging base and any targets written so far.
func cleanupWritten(stagingBase string, written []string) {
	_ = os.RemoveAll(stagingBase)
	for _, target := range written {
		_ = os.RemoveAll(target)
	}
}

// decodeBase64 decodes a base64 string; an empty input with a non-empty source
// is a decode failure.
func decodeBase64(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return decodeBase64Std(s)
}

// unzipSkillArchive unpacks a zip archive into (path, data) rows, skipping
// directory entries and macOS packaging junk.
func unzipSkillArchive(data []byte) ([]struct {
	path string
	data []byte
}, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	var out []struct {
		path string
		data []byte
	}
	for _, f := range r.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if name == "" || strings.HasSuffix(name, "/") {
			continue
		}
		if strings.HasPrefix(name, "__MACOSX/") || containsSeg(name, "__MACOSX") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(io.LimitReader(rc, 8*1024*1024+1))
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, struct {
			path string
			data []byte
		}{path: name, data: content})
	}
	return out, nil
}

// containsSeg reports whether path has segment among its '/'-separated parts.
func containsSeg(path, segment string) bool {
	for _, s := range strings.Split(path, "/") {
		if s == segment {
			return true
		}
	}
	return false
}

// normalizeSkillText strips a UTF-8 BOM and normalizes CRLF/CR to LF (the
// frontmatter splitter and YAML parser reject a stray \r).
func normalizeSkillText(text string) string {
	text = strings.TrimPrefix(text, "\uFEFF")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

// validateFrontmatterStrict validates a new skill's frontmatter exactly like the
// filesystem provider's acceptance rules (mirrors the plugin): a leading ---
// block, name (kebab-case, non-empty), description (non-empty), whenToUse as a
// string, no legacy invocation fields, boolean-only invocation flags, metadata
// as an object. Returns the canonical skill name.
func validateFrontmatterStrict(raw string) (string, error) {
	text := normalizeSkillText(raw)
	text = strings.TrimLeft(text, " ")
	if !strings.HasPrefix(text, "---") {
		return "", errors.New("缺少 YAML frontmatter（文件必须以 --- 开头）")
	}
	firstEnd := strings.IndexByte(text, '\n')
	if firstEnd == -1 {
		return "", errors.New("frontmatter 未闭合")
	}
	closing := strings.Index(text[firstEnd+1:], "\n---")
	var fm, body string
	if closing == -1 {
		fm = text[firstEnd+1:]
	} else {
		closing += firstEnd + 1
		fm = text[firstEnd+1 : closing]
		at := strings.IndexByte(text[closing+3:], '\n')
		if at != -1 {
			body = text[closing+3+at+1:]
		}
	}
	meta := map[string]any{}
	if err := yamlUnmarshal([]byte(fm), &meta); err != nil {
		return "", fmt.Errorf("frontmatter 不是合法的 YAML：%w", err)
	}
	for _, key := range []string{"disableModelInvocation", "modelInvocable", "userInvocable"} {
		if _, ok := meta[key]; ok {
			return "", fmt.Errorf("不支持旧字段 %q，请改用 disable-model-invocation / user-invocable", key)
		}
	}
	name, ok := stringField(meta, "name")
	if !ok {
		return "", errors.New("frontmatter 缺少 name（必须是非空字符串）")
	}
	if !IsSkillName(name) {
		return "", fmt.Errorf("技能名 %q 不符合命名规则（仅小写字母、数字与连字符，如 my-skill）", name)
	}
	desc, ok := stringField(meta, "description")
	if !ok {
		return "", errors.New("frontmatter 缺少 description（必须是非空字符串）")
	}
	if desc == "" {
		return "", errors.New("frontmatter 缺少 description（必须是非空字符串）")
	}
	if w, present := meta["whenToUse"]; present {
		if _, ok := w.(string); !ok {
			return "", errors.New("whenToUse 必须是字符串")
		}
	}
	for _, key := range []string{"disable-model-invocation", "user-invocable"} {
		if v, present := meta[key]; present {
			lower := ""
			switch b := v.(type) {
			case bool:
				continue
			case string:
				lower = strings.ToLower(strings.TrimSpace(b))
			case int:
				continue
			case float64:
				continue
			}
			if !isBoolSpelling(lower) {
				return "", fmt.Errorf("%s 必须是布尔值", key)
			}
		}
	}
	if md, present := meta["metadata"]; present {
		if _, ok := md.(map[string]any); !ok {
			return "", errors.New("metadata 必须是对象")
		}
	}
	_ = body
	return name, nil
}

// isBoolSpelling reports whether lower is a recognized boolean spelling.
func isBoolSpelling(lower string) bool {
	switch lower {
	case "true", "false", "yes", "no", "on", "off", "1", "0":
		return true
	}
	return false
}
