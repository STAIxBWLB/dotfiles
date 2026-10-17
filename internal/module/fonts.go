package module

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

var nerdFontPins = []fontAssetPin{
	{Family: "FiraCode", Tag: "v3.5.1", AssetName: "FiraCode.zip", URL: "https://github.com/ryanoasis/nerd-fonts/releases/download/v3.5.1/FiraCode.zip", SHA256: "239395baf60c89b2eaf4862b6b09db0ef95605cd3e8eef51c00345822a81a665"},
	{Family: "JetBrainsMono", Tag: "v3.5.1", AssetName: "JetBrainsMono.zip", URL: "https://github.com/ryanoasis/nerd-fonts/releases/download/v3.5.1/JetBrainsMono.zip", SHA256: "fab782a66f7d3019da64f6572db9fc5d3a4bcb19f9fa13e2d8a62e3693d6396e"},
	{Family: "Hack", Tag: "v3.5.1", AssetName: "Hack.zip", URL: "https://github.com/ryanoasis/nerd-fonts/releases/download/v3.5.1/Hack.zip", SHA256: "fa24da7de7cefe7766614d27762570b20453c852fc1d5b657111666df9a5e449"},
}

var downloadFontFile = fileutil.DownloadFile

type FontsModule struct{}

func (m *FontsModule) Name() string { return "fonts" }

func (m *FontsModule) fontFamily(rc *RunContext) string {
	if rc.Config.Modules.Fonts.Family != "" {
		return rc.Config.Modules.Fonts.Family
	}
	return "FiraCode"
}

func (m *FontsModule) fontDir(rc *RunContext) string {
	if rc.Config.System != nil && rc.Config.System.OS == "linux" {
		return filepath.Join(rc.HomeDir, ".local", "share", "fonts")
	}
	return filepath.Join(rc.HomeDir, "Library", "Fonts")
}

func (m *FontsModule) familyDir(rc *RunContext, family string) string {
	return filepath.Join(m.fontDir(rc), family)
}

func resolveNerdFontPin(family string) (fontAssetPin, error) {
	for _, pin := range nerdFontPins {
		if pin.Family == family {
			return pin, nil
		}
	}
	return fontAssetPin{}, fmt.Errorf("unsupported Nerd Font family %s; supported manifest is FiraCode, JetBrainsMono, Hack at v3.5.1. Update the source manifest, fixtures, and release procedure before selecting a new family", escapeControl(family))
}

func fontDigestMatches(expected, observed string) bool { return expected == observed }

func (m *FontsModule) Check(_ context.Context, rc *RunContext) (*CheckResult, error) {
	family := m.fontFamily(rc)
	pin, err := resolveNerdFontPin(family)
	if err != nil {
		return nil, err
	}
	dir := m.familyDir(rc, family)
	if !rc.Runner.IsDir(dir) || !fontPinMatches(rc, dir, pin) {
		return &CheckResult{Changes: []Change{{Description: fmt.Sprintf("install pinned Nerd Font %s", family), Command: fmt.Sprintf("download %s (%s)", pin.URL, pin.SHA256)}}}, nil
	}
	return &CheckResult{Satisfied: true}, nil
}

func (m *FontsModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	family := m.fontFamily(rc)
	pin, err := resolveNerdFontPin(family)
	if err != nil {
		return nil, err
	}
	destination := m.familyDir(rc, family)
	if rc.Runner.IsDir(destination) && fontPinMatches(rc, destination, pin) {
		return &ApplyResult{}, nil
	}
	if err := activateFontWithLegacyMigration(ctx, rc, destination, pin); err != nil {
		return nil, err
	}
	messages := []string{fmt.Sprintf("installed pinned Nerd Font %s to %s", family, destination)}
	if rc.Config.System != nil && strings.EqualFold(rc.Config.System.OS, "linux") {
		if _, err := rc.Runner.Run(ctx, "fc-cache", "-f"); err != nil {
			rc.Runner.Logger.Warn("fc-cache failed", "err", err)
		} else {
			messages = append(messages, "ran fc-cache -f")
		}
	}
	return &ApplyResult{Changed: true, Messages: messages}, nil
}

// activateFontWithLegacyMigration leaves root-level legacy files in place.
// A refresh timestamp has no per-file inventory or digest, so it cannot prove
// that a same-prefix font belongs to dot rather than the operator.
func activateFontWithLegacyMigration(ctx context.Context, rc *RunContext, destination string, pin fontAssetPin) error {
	return activateFontComponent(ctx, rc, destination, pin)
}

func fontPinMatches(rc *RunContext, root string, pin fontAssetPin) bool {
	marker, err := readComponentPinMarker(filepath.Join(root, markerFileName("nerd-font-"+pin.Family)))
	if err != nil {
		return false
	}
	desired := componentPinMarker{Schema: componentPinMarkerSchema, Component: marker.Component, Source: pin.URL, Owned: marker.Owned, Files: marker.Files}
	return verifyInstalledComponent(rc.Runner, root, desired) == nil
}

func activateFontComponent(ctx context.Context, rc *RunContext, destination string, pin fontAssetPin) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return fmt.Errorf("preparing font parent: %w", err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	archive := filepath.Join(stage, pin.AssetName)
	got, err := downloadFontFile(ctx, rc.Runner, pin.URL, archive)
	if err != nil {
		return fmt.Errorf("downloading font %s: %w", pin.Family, err)
	}
	if !fontDigestMatches(pin.SHA256, got) {
		return fmt.Errorf("font %s source %s expected SHA-256 %s, observed %s; retry the pinned release or bump the source manifest in a new release", pin.Family, pin.URL, pin.SHA256, escapeControl(got))
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	reader, err := zip.NewReader(f, info.Size())
	if err != nil {
		return fmt.Errorf("opening font archive: %w", err)
	}
	if err := fileutil.ExtractZip(reader, stage); err != nil {
		return fmt.Errorf("extracting font %s: %w", pin.Family, err)
	}
	owned, err := fontOwnedFiles(stage)
	if err != nil {
		return err
	}
	files, err := hashManagedFiles(stage, owned)
	if err != nil {
		return err
	}
	component := "nerd-font-" + pin.Family
	marker := componentPinMarker{Schema: componentPinMarkerSchema, Component: component, Source: pin.URL, Owned: owned, Files: files}
	markerName := markerFileName(component)
	if err := writeComponentPinMarker(filepath.Join(stage, markerName), marker); err != nil {
		return err
	}
	activationOwned := append(append([]string(nil), owned...), markerName)
	if err := fileutil.ActivateOwnedComponent(rc.Runner, fileutil.ActivationOptions{DestinationRoot: destination, StagedRoot: stage, OwnedEntries: activationOwned, Validate: func(root string) error { return validateFontLayout(root, owned) }}); err != nil {
		return fmt.Errorf("activating font %s: %w", pin.Family, err)
	}
	return nil
}

func fontOwnedFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".ttf" && ext != ".otf" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, errors.New("font archive contains no .ttf or .otf files")
	}
	return files, nil
}

func validateFontLayout(root string, owned []string) error {
	if len(owned) == 0 {
		return errors.New("font inventory is empty")
	}
	for _, path := range owned {
		if _, err := os.Lstat(filepath.Join(root, path)); err != nil {
			return fmt.Errorf("font file %q unavailable: %w", path, err)
		}
	}
	return nil
}
