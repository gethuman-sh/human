//go:build wailsapp

package main

import (
	"encoding/json"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/settings"
)

// SettingsDaemon is the read-only Daemon-section header of the settings view.
// It is composed client-side from daemon.json (like the mockups view does)
// rather than round-tripped through config-get, because reachability and
// registration are local knowledge, not file contents.
type SettingsDaemon struct {
	Running  bool     `json:"running"`
	Version  string   `json:"version,omitempty"`
	Addr     string   `json:"addr,omitempty"`
	PID      int      `json:"pid,omitempty"`
	Projects []string `json:"projects,omitempty"`
}

// SettingsData joins the daemon's masked config snapshot with daemon runtime
// info. Error is inlined instead of returned (matching BoardData.Error) so the
// view renders a banner rather than throwing.
type SettingsData struct {
	Doc    *settings.Doc  `json:"doc,omitempty"`
	Daemon SettingsDaemon `json:"daemon"`
	Error  string         `json:"error,omitempty"`
}

// Settings fetches the current settings snapshot via the daemon. Secrets are
// masked daemon-side; this method never sees a resolved credential.
func (a *App) Settings() (SettingsData, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return SettingsData{Error: "daemon not running — settings need the daemon"}, nil
	}
	data := SettingsData{Daemon: settingsDaemon(info)}
	doc, err := daemon.GetConfig(info.Addr, info.Token)
	if err != nil {
		data.Error = errors.CauseChain(err)
		return data, nil
	}
	data.Doc = &doc
	return data, nil
}

// SaveSetting writes one dotted-path settings key. valueJSON carries the new
// value JSON-encoded (string, bool, or array) so a single binding serves every
// field type. Returns the refreshed snapshot for in-place re-render.
func (a *App) SaveSetting(path string, valueJSON string) (SettingsData, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return SettingsData{Error: "daemon not running — settings need the daemon"}, nil
	}
	data := SettingsData{Daemon: settingsDaemon(info)}
	doc, err := daemon.SetConfig(info.Addr, info.Token, daemon.SetConfigRequest{
		Path:  path,
		Value: json.RawMessage(valueJSON),
	})
	if err != nil {
		data.Error = errors.CauseChain(err)
		return data, nil
	}
	data.Doc = &doc
	return data, nil
}

func settingsDaemon(info daemon.DaemonInfo) SettingsDaemon {
	projects := make([]string, 0, len(info.Projects))
	for _, p := range info.Projects {
		projects = append(projects, p.Dir)
	}
	return SettingsDaemon{
		Running:  info.IsReachable(),
		Version:  info.Version,
		Addr:     info.Addr,
		PID:      info.PID,
		Projects: projects,
	}
}
