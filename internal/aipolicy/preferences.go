package aipolicy

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

// Preferences manages generated launch overlays, never native base settings.
// Explicit scopes all state to Home and deliberately ignores environment homes.
type Preferences struct {
	Home         string
	Explicit     bool
	originalHome string
}

type PreferenceChange struct {
	TargetID    string   `json:"target_id"`
	Agent       string   `json:"agent"`
	Path        string   `json:"path,omitempty"`
	Status      string   `json:"status"`
	LaunchArgs  []string `json:"launch_args,omitempty"`
	Constraint  string   `json:"constraint"`
	BaseManaged bool     `json:"base_managed"`
	content     []byte
}

type preferenceReceipt struct {
	Entries []preferenceEntry `json:"entries"`
}
type preferenceEntry struct {
	TargetID     string `json:"target_id"`
	Agent        string `json:"agent"`
	Hash         string `json:"hash"`
	PreviousHash string `json:"previous_hash,omitempty"`
	RelativePath string `json:"relative_path"`
}

// canonicalDeclaredHome resolves the caller's declared boundary (including OS
// aliases such as /var), without resolving any managed descendant below it.
func canonicalDeclaredHome(home string) (string, error) {
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("preference home must be absolute")
	}
	existing := filepath.Clean(home)
	tail := []string{}
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", err
		}
		tail = append(tail, filepath.Base(existing))
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	for i := len(tail) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, tail[i])
	}
	return resolved, nil
}
func (p Preferences) canonicalized() (Preferences, error) {
	if p.originalHome == "" {
		p.originalHome = p.Home
	}
	home, err := canonicalDeclaredHome(p.Home)
	if err != nil {
		return p, err
	}
	p.Home = home
	return p, nil
}
func (p Preferences) canonicalRuntimePath(path string) string {
	if p.originalHome != "" {
		rel, err := filepath.Rel(p.originalHome, path)
		if err == nil && filepath.IsLocal(rel) {
			return filepath.Join(p.Home, rel)
		}
	}
	return path
}

// namespace separates receipts for native profile homes and selection modes.
// Include the namespace in Codex filenames too: two namespaces may share a
// native home while differing in Claude home or explicit selection mode.
func (p Preferences) namespace() (string, error) {
	normalized, err := p.canonicalized()
	if err != nil {
		return "", err
	}
	p = normalized
	identity := fmt.Sprintf("explicit=%t", p.Explicit)
	for _, agent := range []string{"claude", "codex"} {
		nativeHome := filepath.Join(p.Home, "."+agent)
		mode := "default"
		envKey := "CODEX_HOME"
		if agent == "claude" {
			envKey = "CLAUDE_CONFIG_DIR"
		}
		if !p.Explicit && os.Getenv(envKey) != "" {
			nativeHome = p.canonicalRuntimePath(os.Getenv(envKey))
			mode = "environment"
		}
		if !filepath.IsAbs(nativeHome) {
			return "", fmt.Errorf("%s profile home must be absolute", agent)
		}
		relative, relErr := filepath.Rel(p.Home, nativeHome)
		if relErr != nil || !filepath.IsLocal(relative) {
			return "", fmt.Errorf("%s profile home must remain within preference home", agent)
		}
		identity += "\n" + agent + "=" + relative + ";" + mode
	}
	hash := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(hash[:12]), nil
}
func (p Preferences) root() (string, error) {
	normalized, err := p.canonicalized()
	if err != nil {
		return "", err
	}
	p = normalized
	namespace, err := p.namespace()
	if err != nil {
		return "", err
	}
	return filepath.Join(p.Home, ".local", "share", "dotfiles", "ai", "policy", "profiles", namespace), nil
}
func (p Preferences) filename(id, agent string) string {
	namespace, _ := p.namespace()
	return preferenceName(namespace+"-"+id, agent)
}
func preferenceName(id, agent string) string {
	sum := sha256.Sum256([]byte(id))
	if agent == "codex" {
		return "dot-policy-" + hex.EncodeToString(sum[:12]) + ".config.toml"
	}
	return agent + "-" + hex.EncodeToString(sum[:12]) + ".json"
}
func preferenceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// safePreferencePath rejects symlinks in every existing path component, including
// ancestors. This is a cooperative local-writer boundary, not a hostile-user sandbox.
func safePreferencePath(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || (current != path && !info.IsDir())) {
			return fmt.Errorf("unsafe preference path %s", current)
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}
func (p Preferences) readReceipt() (preferenceReceipt, error) {
	root, err := p.root()
	if err != nil {
		return preferenceReceipt{}, err
	}
	path := filepath.Join(root, "receipt.json")
	if err = safePreferencePath(path); err != nil {
		return preferenceReceipt{}, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return preferenceReceipt{Entries: []preferenceEntry{}}, nil
	}
	if err != nil {
		return preferenceReceipt{}, err
	}
	var receipt preferenceReceipt
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&receipt); err != nil {
		return receipt, fmt.Errorf("invalid preference receipt: %w", err)
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return receipt, fmt.Errorf("invalid trailing receipt content")
	}
	seen := map[string]bool{}
	for _, entry := range receipt.Entries {
		if entry.TargetID == "" || (entry.Agent != "claude" && entry.Agent != "codex") || len(entry.Hash) != 64 || seen[entry.TargetID] {
			return receipt, fmt.Errorf("invalid preference receipt entry")
		}
		if err := p.validateReceiptPath(entry); err != nil {
			return receipt, err
		}
		seen[entry.TargetID] = true
	}
	return receipt, nil
}

// validateReceiptPath restricts even corrupted receipts to generated destinations.
func (p Preferences) validateReceiptPath(entry preferenceEntry) error {
	rel := entry.RelativePath
	if !filepath.IsLocal(rel) || filepath.Clean(rel) != rel {
		return fmt.Errorf("invalid receipt path")
	}
	root, err := p.root()
	if err != nil {
		return err
	}
	expected := filepath.Join(root, "overlays", p.filename(entry.TargetID, entry.Agent))
	if entry.Agent == "codex" {
		nativeHome := filepath.Join(p.Home, ".codex")
		if !p.Explicit && os.Getenv("CODEX_HOME") != "" {
			nativeHome = p.canonicalRuntimePath(os.Getenv("CODEX_HOME"))
		}
		homeRel, relErr := filepath.Rel(p.Home, nativeHome)
		if relErr != nil || !filepath.IsAbs(nativeHome) || !filepath.IsLocal(homeRel) {
			return fmt.Errorf("invalid Codex receipt home")
		}
		expected = filepath.Join(nativeHome, p.filename(entry.TargetID, entry.Agent))
	}
	if filepath.Join(p.Home, rel) != expected {
		return fmt.Errorf("receipt path is outside its generated destination")
	}
	return nil
}

// Plan is read-only, including when Home does not yet exist. Unsupported targets
// report their limitation rather than silently writing a weaker permission mode.
func (p Preferences) Plan(policy *config.AIPolicyConfig, inventory []Runtime) ([]PreferenceChange, error) {
	normalized, normalizeErr := p.canonicalized()
	if normalizeErr != nil {
		return nil, normalizeErr
	}
	p = normalized
	if err := config.ValidateAIPolicy(policy); err != nil {
		return nil, err
	}
	root, err := p.root()
	if err != nil {
		return nil, err
	}
	receipt, err := p.readReceipt()
	if err != nil {
		return nil, err
	}
	owned := map[string]preferenceEntry{}
	for _, e := range receipt.Entries {
		owned[e.TargetID] = e
	}
	out := []PreferenceChange{}
	if policy == nil || !policy.Enabled {
		return out, nil
	}
	for _, target := range policy.Targets {
		change := PreferenceChange{TargetID: target.ID, Agent: target.Agent, Status: "unsupported", Constraint: "native base settings remain unmanaged; use dot ai session start"}
		var runtime *Runtime
		for i := range inventory {
			if inventory[i].Agent == target.Agent {
				copyRuntime := inventory[i]
				copyRuntime.Home = p.canonicalRuntimePath(copyRuntime.Home)
				runtime = &copyRuntime
				break
			}
		}
		if !target.Validated || runtime == nil || !runtime.Available || runtime.Version != target.Version || !runtime.AutoReview {
			change.Constraint = "requires available, version-matched, validated automatic-review runtime"
			out = append(out, change)
			continue
		}
		if target.Billing == "subscription" && runtime.SubscriptionConflict {
			change.Constraint = "subscription runtime has provider or credential overrides"
			out = append(out, change)
			continue
		}
		if _, _, permissionErr := permissionArgs(*runtime, true); permissionErr != nil {
			change.Constraint = permissionErr.Error()
			out = append(out, change)
			continue
		}
		if target.Agent != "claude" && target.Agent != "codex" {
			change.Constraint = "persistent overlay unavailable for this runtime; launch policy manages supported flags; native base remains unmanaged"
			out = append(out, change)
			continue
		}
		knowledgeRuntime := *runtime
		if p.Explicit {
			knowledgeRuntime.Home = filepath.Join(p.Home, "."+target.Agent)
		}
		grants, knowledgeErr := KnowledgeApprovals(knowledgeRuntime)
		if knowledgeErr != nil {
			return nil, knowledgeErr
		}
		conflicts, conflictErr := KnowledgeConflicts(knowledgeRuntime, "", grants)
		if conflictErr != nil {
			return nil, conflictErr
		}
		if len(conflicts) > 0 {
			change.Constraint = strings.Join(conflicts, "; ")
			out = append(out, change)
			continue
		}
		grantArgs, knowledgeErr := KnowledgeArgs(knowledgeRuntime, grants)
		if knowledgeErr != nil {
			return nil, knowledgeErr
		}
		permissions := map[string]any{"defaultMode": "auto"}
		if target.Agent == "claude" && len(grantArgs) == 2 {
			permissions["allow"] = strings.Split(grantArgs[1], ",")
		}
		settings := map[string]any{"permissions": permissions, "model": target.Model}
		if target.Effort != "" {
			effort := target.Effort
			if effort == "balanced" {
				effort = "medium"
			}
			settings["effortLevel"] = effort
			settings["modelSettings"] = map[string]any{target.Model: map[string]string{"effortLevel": effort}}
		}
		change.content, err = json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return nil, err
		}
		change.content = append(change.content, '\n')
		change.Path = filepath.Join(root, "overlays", p.filename(target.ID, target.Agent))
		change.LaunchArgs = []string{"--settings", change.Path}
		if target.Agent == "codex" {
			nativeHome := runtime.Home
			if p.Explicit {
				nativeHome = filepath.Join(p.Home, ".codex")
			}
			if !filepath.IsAbs(nativeHome) {
				return nil, fmt.Errorf("codex home must be absolute")
			}
			relative, relErr := filepath.Rel(p.Home, nativeHome)
			if relErr != nil || strings.HasPrefix(relative, "..") {
				return nil, fmt.Errorf("codex home must be within preference home")
			}
			base := filepath.Join(nativeHome, "config.toml")
			if err = safePreferencePath(base); err != nil {
				return nil, err
			}
			if file, openErr := os.Open(base); openErr == nil {
				data, readErr := io.ReadAll(io.LimitReader(file, 1024*1024))
				_ = file.Close()
				if readErr != nil {
					return nil, readErr
				}
				for _, line := range strings.Split(string(data), "\n") {
					trimmed := strings.TrimSpace(line)
					if strings.HasPrefix(trimmed, "[profiles.") || strings.HasPrefix(trimmed, "[profiles]") {
						change.Constraint += "; legacy inline Codex profiles detected; migration remains pending"
						break
					}
				}
			} else if !os.IsNotExist(openErr) {
				return nil, openErr
			}
			change.Path = filepath.Join(nativeHome, p.filename(target.ID, target.Agent))
			change.LaunchArgs = []string{"--profile", strings.TrimSuffix(filepath.Base(change.Path), ".config.toml")}
			change.content = []byte(fmt.Sprintf("model = %q\napproval_policy = \"on-request\"\napprovals_reviewer = \"auto_review\"\nsandbox_mode = \"workspace-write\"\n", target.Model))
			if target.Effort != "" {
				effort := target.Effort
				if effort == "balanced" {
					effort = "medium"
				}
				change.content = append(change.content, []byte(fmt.Sprintf("model_reasoning_effort = %q\n", effort))...)
			}
			for _, grant := range grants {
				change.content = append(change.content, []byte(knowledgeKey(grant)+" = \"approve\"\n")...)
			}
		}
		rel, relErr := filepath.Rel(p.Home, change.Path)
		if relErr != nil {
			return nil, relErr
		}
		if err = p.validateReceiptPath(preferenceEntry{TargetID: target.ID, Agent: target.Agent, RelativePath: rel}); err != nil {
			return nil, err
		}
		if err = safePreferencePath(change.Path); err != nil {
			return nil, err
		}
		current, readErr := os.ReadFile(change.Path)
		if readErr != nil && !os.IsNotExist(readErr) {
			return nil, readErr
		}
		entry, exists := owned[target.ID]
		switch {
		case exists && (entry.Agent != target.Agent || filepath.Join(p.Home, entry.RelativePath) != change.Path):
			change.Status = "conflict"
		case os.IsNotExist(readErr):
			change.Status = "create"
		case !exists || entry.Agent != target.Agent || filepath.Join(p.Home, entry.RelativePath) != change.Path:
			change.Status = "conflict"
		case preferenceHash(current) != entry.Hash && preferenceHash(current) != entry.PreviousHash:
			change.Status = "conflict"
		case bytes.Equal(current, change.content):
			change.Status = "current"
		default:
			change.Status = "update"
		}
		out = append(out, change)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TargetID < out[j].TargetID })
	return out, nil
}

func (p Preferences) Status(policy *config.AIPolicyConfig, inventory []Runtime) ([]PreferenceChange, error) {
	return p.Plan(policy, inventory)
}

// writeFile keeps publication confined to Home even if a directory is replaced
// after path validation. The cooperative lock cannot serialize external editors.
func (p Preferences) writeFile(path string, data []byte) error {
	rel, err := filepath.Rel(p.Home, path)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("write outside preference home")
	}
	if err = safePreferencePath(path); err != nil {
		return err
	}
	root, err := os.OpenRoot(p.Home)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.MkdirAll(filepath.Dir(rel), 0700); err != nil {
		return err
	}
	temp := filepath.Join(filepath.Dir(rel), ".dot-preference-"+rand.Text())
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(temp) }()
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return root.Rename(temp, rel)
}
func (p Preferences) writeReceipt(receipt preferenceReceipt) error {
	sort.Slice(receipt.Entries, func(i, j int) bool { return receipt.Entries[i].TargetID < receipt.Entries[j].TargetID })
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	root, err := p.root()
	if err != nil {
		return err
	}
	return p.writeFile(filepath.Join(root, "receipt.json"), append(data, '\n'))
}

// Apply journals ownership before publishing generated files. A failed or
// interrupted write can be safely retried or rolled back using either hash.
func (p Preferences) Apply(policy *config.AIPolicyConfig, inventory []Runtime, dryRun bool) ([]PreferenceChange, error) {
	normalized, normalizeErr := p.canonicalized()
	if normalizeErr != nil {
		return nil, normalizeErr
	}
	p = normalized
	changes, err := p.Plan(policy, inventory)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return changes, nil
	}
	need := false
	for _, c := range changes {
		if c.Status == "conflict" {
			return nil, fmt.Errorf("foreign preference edit: %s", c.Path)
		}
		if c.Status == "create" || c.Status == "update" {
			need = true
		}
	}
	if !need {
		return changes, nil
	}
	root, _ := p.root()
	if err = safePreferencePath(filepath.Join(root, ".lock")); err != nil {
		return nil, err
	}
	release, err := fileutil.AcquirePIDLock(filepath.Join(root, ".lock"), fileutil.LockOptions{Label: "AI preference update running"})
	if err != nil {
		return nil, err
	}
	defer release()
	changes, err = p.Plan(policy, inventory)
	if err != nil {
		return nil, err
	}
	receipt, err := p.readReceipt()
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
		if c.Status == "conflict" {
			return nil, fmt.Errorf("foreign preference edit: %s", c.Path)
		}
		if c.Status != "create" && c.Status != "update" {
			continue
		}
		relative, _ := filepath.Rel(p.Home, c.Path)
		next := preferenceEntry{TargetID: c.TargetID, Agent: c.Agent, Hash: preferenceHash(c.content), RelativePath: relative}
		found := false
		for i, e := range receipt.Entries {
			if e.TargetID == c.TargetID {
				current, readErr := os.ReadFile(c.Path)
				if readErr != nil && !os.IsNotExist(readErr) {
					return nil, readErr
				}
				if readErr == nil {
					if preferenceHash(current) != e.Hash && preferenceHash(current) != e.PreviousHash {
						return nil, fmt.Errorf("foreign preference edit: %s", c.Path)
					}
					next.PreviousHash = preferenceHash(current)
				}
				receipt.Entries[i] = next
				found = true
				break
			}
		}
		if !found {
			receipt.Entries = append(receipt.Entries, next)
		}
	}
	if err = p.writeReceipt(receipt); err != nil {
		return nil, err
	}
	for i, c := range changes {
		if c.Status != "create" && c.Status != "update" {
			continue
		}
		if err = safePreferencePath(c.Path); err != nil {
			return nil, err
		}
		current, readErr := os.ReadFile(c.Path)
		if readErr != nil && !os.IsNotExist(readErr) {
			return nil, readErr
		}
		for _, e := range receipt.Entries {
			if e.TargetID == c.TargetID && !os.IsNotExist(readErr) && preferenceHash(current) != e.Hash && preferenceHash(current) != e.PreviousHash {
				return nil, fmt.Errorf("foreign preference edit: %s", c.Path)
			}
		}
		if err = p.writeFile(c.Path, c.content); err != nil {
			return nil, err
		}
		changes[i].Status = "applied"
	}
	for i := range receipt.Entries {
		receipt.Entries[i].PreviousHash = ""
	}
	if err = p.writeReceipt(receipt); err != nil {
		return nil, err
	}
	return changes, nil
}

// Rollback removes only untouched generated overlays. Native files and unrelated
// overlays are preserved; ownership conflicts stop the operation before deletion.
func (p Preferences) Rollback(dryRun bool) ([]PreferenceChange, error) {
	normalized, normalizeErr := p.canonicalized()
	if normalizeErr != nil {
		return nil, normalizeErr
	}
	p = normalized
	root, err := p.root()
	if err != nil {
		return nil, err
	}
	receipt, err := p.readReceipt()
	if err != nil {
		return nil, err
	}
	if len(receipt.Entries) == 0 {
		return []PreferenceChange{}, nil
	}
	if !dryRun {
		if err = safePreferencePath(filepath.Join(root, ".lock")); err != nil {
			return nil, err
		}
		release, lockErr := fileutil.AcquirePIDLock(filepath.Join(root, ".lock"), fileutil.LockOptions{Label: "AI preference update running"})
		if lockErr != nil {
			return nil, lockErr
		}
		defer release()
		receipt, err = p.readReceipt()
		if err != nil {
			return nil, err
		}
	}
	changes := []PreferenceChange{}
	for _, e := range receipt.Entries {
		path := filepath.Join(p.Home, e.RelativePath)
		if err = safePreferencePath(path); err != nil {
			return nil, err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil && !os.IsNotExist(readErr) {
			return nil, readErr
		}
		if readErr == nil && preferenceHash(data) != e.Hash && preferenceHash(data) != e.PreviousHash {
			return nil, fmt.Errorf("foreign preference edit: %s", path)
		}
		changes = append(changes, PreferenceChange{TargetID: e.TargetID, Agent: e.Agent, Path: path, Status: "remove", Constraint: "native base settings preserved"})
	}
	if dryRun {
		return changes, nil
	}
	mutationRoot, openErr := os.OpenRoot(p.Home)
	if openErr != nil {
		return nil, openErr
	}
	defer mutationRoot.Close()
	for _, c := range changes {
		relative, relErr := filepath.Rel(p.Home, c.Path)
		if relErr != nil || !filepath.IsLocal(relative) {
			return nil, fmt.Errorf("rollback outside preference home")
		}
		if err = mutationRoot.Remove(relative); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	if err = p.writeReceipt(preferenceReceipt{Entries: []preferenceEntry{}}); err != nil {
		return nil, err
	}
	return changes, nil
}

// OverlayArgs returns only a current overlay. Missing or drifted preferences do
// not silently override an invocation's explicit permission and model arguments.
func (p Preferences) OverlayArgs(policy *config.AIPolicyConfig, inventory []Runtime, targetID string) ([]string, error) {
	changes, err := p.Plan(policy, inventory)
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
		if c.TargetID == targetID {
			if c.Status == "current" {
				return c.LaunchArgs, nil
			}
			if strings.Contains(c.Status, "conflict") {
				return nil, fmt.Errorf("foreign preference edit: %s", c.Path)
			}
			return nil, nil
		}
	}
	return nil, nil
}
