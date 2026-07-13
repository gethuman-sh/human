//go:build wailsapp

package main

import (
	"os"
	"path/filepath"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/starter"
)

// humanconfigNames are the accepted project-config filenames (the same set
// internal/config resolves via viper); any of them marks the project root.
var humanconfigNames = []string{".humanconfig.yaml", ".humanconfig.yml", ".humanconfig"}

// StartProjectInfo tells the frontend whether the Start Project wizard applies
// and which templates it can offer. Error is soft (FeatureDoc.Error convention):
// a failed probe means "don't show the wizard", never a broken app.
type StartProjectInfo struct {
	EmptyProject bool               `json:"emptyProject"`
	Templates    []starter.Template `json:"templates"`
	Error        string             `json:"error,omitempty"`
}

// StartProjectResult reports the scaffolding outcome for the wizard's success
// message.
type StartProjectResult struct {
	FilesCreated int `json:"filesCreated"`
}

// StartProjectStatus probes the project root for source files. Like Features()
// it is a plain local-FS read — no daemon, no credentials — because the answer
// must reflect the directory the desktop app actually runs in, not whatever
// namespace the daemon lives in.
func (a *App) StartProjectStatus() (StartProjectInfo, error) {
	info := StartProjectInfo{Templates: starter.Templates()}
	dir, err := projectRoot()
	if err != nil {
		info.Error = err.Error()
		return info, nil
	}
	empty, err := starter.IsEmptyProject(dir)
	if err != nil {
		info.Error = err.Error()
		return info, nil
	}
	info.EmptyProject = empty
	return info, nil
}

// StartProject scaffolds the chosen starter template into the project root.
func (a *App) StartProject(projectType, language string) (StartProjectResult, error) {
	tpl, ok := starter.Lookup(projectType, language)
	if !ok {
		return StartProjectResult{}, errors.WithDetails("unknown starter template", "type", projectType, "language", language)
	}
	dir, err := projectRoot()
	if err != nil {
		return StartProjectResult{}, err
	}
	res, err := starter.Fetch(a.ctx, tpl, dir)
	if err != nil {
		return StartProjectResult{}, err
	}
	return StartProjectResult{FilesCreated: res.Created}, nil
}

// projectRoot resolves the directory to scaffold into: the nearest ancestor
// holding a .humanconfig (so launching from a subdirectory still targets the
// project root, consistent with Features()' findUp), falling back to the
// working directory when no config exists yet.
func projectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", errors.WrapWithDetails(err, "resolving working directory")
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		for _, name := range humanconfigNames {
			if _, statErr := os.Stat(filepath.Join(dir, name)); statErr == nil {
				return dir, nil
			}
		}
		if filepath.Dir(dir) == dir {
			return cwd, nil
		}
	}
}
