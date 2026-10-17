package aitooling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
)

type Operation string

const (
	Inspect Operation = "inspect"
	Ensure  Operation = "ensure"
	Update  Operation = "update"
)

type Options struct {
	HomeDir                         string
	ExplicitHome, DryRun, Scheduled bool
	Out                             io.Writer
	Only                            []string
}
type ItemResult struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Installed string `json:"installed,omitempty"`
	Latest    string `json:"latest,omitempty"`
	Detail    string `json:"detail,omitempty"`
}
type Report struct {
	Items    []ItemResult `json:"items"`
	Failed   int          `json:"failed"`
	Deferred int          `json:"deferred"`
}
type Engine struct {
	opts        Options
	exec        executor
	http        *http.Client
	acquire     func(context.Context, resourceguard.Options) (func(), error)
	waitAcquire func(context.Context, resourceguard.Options, time.Duration) (func(), error)
	receipts    map[string]receipt
}
type receipt struct {
	Provider  string    `json:"provider"`
	Integrity string    `json:"integrity,omitempty"`
	Path      string    `json:"path,omitempty"`
	Version   string    `json:"version,omitempty"`
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checkedAt"`
}

func New(o Options) *Engine {
	if o.HomeDir == "" {
		o.HomeDir, _ = os.UserHomeDir()
	}
	return &Engine{opts: o, exec: execute, http: &http.Client{Timeout: 20 * time.Second}, acquire: resourceguard.Acquire, waitAcquire: resourceguard.WaitAcquire, receipts: map[string]receipt{}}
}
func (e *Engine) Run(ctx context.Context, s config.AIToolingConfig, op Operation) (Report, error) {
	r := Report{Items: []ItemResult{}}
	if err := ValidateSelection(s); err != nil {
		return r, err
	}
	if op != Inspect && op != Ensure && op != Update {
		return r, fmt.Errorf("unsupported operation %q", op)
	}
	if !filepath.IsAbs(e.opts.HomeDir) {
		return r, errors.New("tooling home must be absolute")
	}
	if !filepath.IsAbs(config.ConfigHome(e.opts.HomeDir, e.opts.ExplicitHome)) {
		return r, errors.New("tooling configuration home must be absolute")
	}
	if len(s.Agents) == 0 && len(s.Tools) == 0 && len(s.Skills) == 0 {
		return r, nil
	}
	if op != Inspect && !e.opts.DryRun {
		var release func()
		var err error
		if e.opts.Scheduled {
			release, err = e.waitAcquire(ctx, resourceguard.Options{HomeDir: e.opts.HomeDir, Purpose: "selected AI tooling", ScopeKey: "tooling"}, 6*time.Minute)
		} else {
			release, err = e.acquire(ctx, resourceguard.Options{HomeDir: e.opts.HomeDir, Purpose: "selected AI tooling", ScopeKey: "tooling"})
		}
		if err != nil {
			var deferred *resourceguard.DeferredError
			if errors.As(err, &deferred) {
				r.Items = append(r.Items, ItemResult{ID: "admission", Kind: "resource", Status: "deferred-resource-pressure", Detail: deferred.Reason})
				r.Deferred++
				return r, nil
			}
			return r, err
		}
		defer release()
	}
	if err := e.loadReceipts(); err != nil {
		return r, err
	}
	for _, entry := range Catalog() {
		if len(e.opts.Only) > 0 && !slices.Contains(e.opts.Only, entry.ID) {
			continue
		}
		selected := s.Tools
		if entry.Kind == "agent" {
			selected = s.Agents
		}
		if !slices.Contains(selected, entry.ID) {
			continue
		}
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		var items []ItemResult
		if entry.Kind == "agent" || entry.ID == "ripwire" || entry.ID == "ocr" {
			items = []ItemResult{e.binary(ctx, entry, s.Pins[entry.ID], op)}
			if entry.ID == "ripwire" {
				if op == Inspect || e.opts.DryRun || items[0].Installed != "" && (items[0].Status == "installed" || items[0].Status == "updated" || items[0].Status == "up-to-date" || items[0].Status == "pinned") {
					items = append(items, e.ripwireSkills(ctx, s, op)...)
				} else {
					items = append(items, ItemResult{ID: "ripwire/skills", Kind: "integration", Status: "deferred-artifact", Detail: "binary reconciliation did not establish a usable version; staged skills left unchanged"})
				}
			}
			if entry.ID == "ocr" {
				items = append(items, e.addon(ctx, entry, s, op)...)
			}
		} else {
			items = e.addon(ctx, entry, s, op)
		}
		r.Items = append(r.Items, items...)
	}
	if len(s.Skills) > 0 && len(e.opts.Only) == 0 {
		r.Items = append(r.Items, e.skills(ctx, s, op))
	}
	for _, item := range r.Items {
		if item.Status == "failed" {
			r.Failed++
		}
		if strings.HasPrefix(item.Status, "deferred") || item.Status == "unknown" || item.Status == "partial" || strings.HasPrefix(item.Status, "pending-") {
			r.Deferred++
		}
	}
	if op != Inspect && !e.opts.DryRun {
		if err := e.saveReceipts(); err != nil {
			return r, err
		}
	}
	if r.Failed > 0 {
		return r, fmt.Errorf("%d selected tooling operation(s) failed", r.Failed)
	}
	return r, nil
}
func (e *Engine) statePath() string {
	return filepath.Join(e.opts.HomeDir, ".local", "share", "dotfiles", "ai", "tooling-state.json")
}
func (e *Engine) loadReceipts() error {
	data, err := os.ReadFile(e.statePath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read tooling receipts: %w", err)
	}
	if err = json.Unmarshal(data, &e.receipts); err != nil {
		return fmt.Errorf("invalid tooling receipts; preserve and repair before adoption: %w", err)
	}
	if e.receipts == nil {
		e.receipts = map[string]receipt{}
	}
	return nil
}

func (e *Engine) saveReceipts() error {
	p := e.statePath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(e.receipts, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tooling-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}
func (e *Engine) record(id, provider, path, version, status string) {
	e.receipts[id] = receipt{Provider: provider, Path: path, Version: version, Status: status, CheckedAt: time.Now().UTC()}
}
func (e *Engine) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metadata unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	return b, nil
}
func (e *Engine) latest(ctx context.Context, source string) (string, error) {
	return e.latestVersion(ctx, source, false)
}

// latestVersion resolves the newest stable version from a metadata endpoint.
// allowSuffix also accepts prerelease-suffixed versions (e.g. 1.18.32-gencode.1)
// for publishers that never ship plain X.Y.Z tags.
func (e *Engine) latestVersion(ctx context.Context, source string, allowSuffix bool) (string, error) {
	b, err := e.get(ctx, source)
	if err != nil {
		return "", err
	}
	var doc struct {
		Version    string `json:"version"`
		Tag        string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
	}
	v := strings.TrimSpace(string(b))
	if strings.HasPrefix(v, "{") {
		if err = json.Unmarshal(b, &doc); err != nil {
			return "", err
		}
		if doc.Prerelease || doc.Draft {
			return "", errors.New("stable release unavailable")
		}
		v = doc.Version
		if v == "" {
			v = doc.Tag
		}
	}
	if allowSuffix {
		if versionInOutput.FindString(v) != v {
			return "", errors.New("stable version metadata is unknown")
		}
		return strings.TrimPrefix(v, "v"), nil
	}
	if !stableVersion.MatchString(v) {
		return "", errors.New("stable version metadata is unknown")
	}
	return strings.TrimPrefix(v, "v"), nil
}
