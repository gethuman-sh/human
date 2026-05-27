package cmdtui

import (
	"context"
	"os/exec"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/gethuman-sh/human/internal/platform"
)

// soundPlayerTimeout bounds a notification player invocation. Hung
// playback binaries (afplay/paplay/powershell.exe) must not leak
// goroutines across state transitions.
const soundPlayerTimeout = 5 * time.Second

// soundPlayerSlots caps concurrent in-flight player invocations so
// rapid state transitions cannot spawn an unbounded goroutine pool.
var soundPlayerSlots int32

const maxSoundPlayerSlots = 2

// playNotificationSound plays a platform-appropriate notification sound
// in the background. Errors are silently ignored. A context timeout
// kills hung playback processes and a small semaphore drops extra
// invocations rather than queueing goroutines.
func playNotificationSound() {
	if atomic.AddInt32(&soundPlayerSlots, 1) > maxSoundPlayerSlots {
		atomic.AddInt32(&soundPlayerSlots, -1)
		return
	}
	go func() {
		defer atomic.AddInt32(&soundPlayerSlots, -1)
		name, args := notificationCommand()
		if name == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), soundPlayerTimeout)
		defer cancel()
		_ = exec.CommandContext(ctx, name, args...).Run() // #nosec G204 -- name and args are static per-platform constants
	}()
}

func notificationCommand() (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "afplay", []string{"/System/Library/Sounds/Glass.aiff"}
	case "linux":
		if platform.IsWSL() {
			return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", "(New-Object System.Media.SoundPlayer 'C:\\Windows\\Media\\chimes.wav').PlaySync()"}
		}
		return "paplay", []string{"/usr/share/sounds/freedesktop/stereo/complete.oga"}
	default:
		return "", nil
	}
}
