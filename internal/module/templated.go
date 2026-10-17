package module

import (
	"fmt"
	"os"

	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

type templatedFile struct {
	templatePath string
	destPath     string
	isTemplate   bool
	perm         os.FileMode
	// postRender, when set, rewrites the rendered content before the
	// check/apply comparison — e.g. to preserve foreign keys in a file the
	// template otherwise owns wholesale.
	postRender func(rc *RunContext, rendered []byte) ([]byte, error)
}

func checkTemplatedFiles(rc *RunContext, files []templatedFile) ([]Change, error) {
	var changes []Change
	data := rc.Config.TemplateData(rc.HomeDir)

	for _, f := range files {
		content, err := renderTemplatedFile(rc, f, data)
		if err != nil {
			return nil, err
		}
		if fileutil.NeedsUpdate(rc.Runner, f.destPath, content) {
			changes = append(changes, Change{
				Description: fmt.Sprintf("write %s", f.destPath),
				Command:     fmt.Sprintf("%s %s -> %s", f.action(), f.templatePath, f.destPath),
			})
		}
	}

	return changes, nil
}

func applyTemplatedFiles(rc *RunContext, files []templatedFile) ([]string, error) {
	var messages []string
	data := rc.Config.TemplateData(rc.HomeDir)

	for _, f := range files {
		content, err := renderTemplatedFile(rc, f, data)
		if err != nil {
			return nil, err
		}
		written, err := fileutil.EnsureFile(rc.Runner, rc.HomeDir, f.destPath, content, f.mode())
		if err != nil {
			return nil, fmt.Errorf("writing %s: %w", f.destPath, err)
		}
		if written {
			messages = append(messages, fmt.Sprintf("wrote %s", f.destPath))
		}
	}

	return messages, nil
}

func renderTemplatedFile(rc *RunContext, f templatedFile, data map[string]any) ([]byte, error) {
	var content []byte
	var err error
	if f.isTemplate {
		content, err = rc.Template.Render(f.templatePath, data)
		if err != nil {
			return nil, fmt.Errorf("rendering %s: %w", f.templatePath, err)
		}
	} else {
		content, err = rc.Template.ReadStatic(f.templatePath)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f.templatePath, err)
		}
	}
	if f.postRender != nil {
		content, err = f.postRender(rc, content)
		if err != nil {
			return nil, fmt.Errorf("post-processing %s: %w", f.templatePath, err)
		}
	}
	return content, nil
}

func (f templatedFile) action() string {
	if f.isTemplate {
		return "render"
	}
	return "copy"
}

func (f templatedFile) mode() os.FileMode {
	if f.perm == 0 {
		return 0644
	}
	return f.perm
}
