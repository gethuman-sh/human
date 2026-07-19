//go:build wailsapp

package main

import "github.com/gethuman-sh/human/internal/daemon"

// Stats fetches the board's consolidated stats for a range ("24h"|"7d"|"30d")
// from the daemon. Like every other App method it talks only to the daemon (see
// the app.go header): all persisted stat data lives on the daemon host, so the
// daemon is the single aggregation point.
func (a *App) Stats(rng string) (daemon.StatsOverview, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return daemon.StatsOverview{}, err
	}
	ov, err := daemon.GetStatsOverview(info.Addr, info.Token, rng)
	if err != nil {
		return daemon.StatsOverview{}, daemonCause(err)
	}
	return *ov, nil
}
