package daemon

import "sync"

// prLoopDrives is the set of tickets whose review→fix loop is being driven
// RIGHT NOW in this process. The loop has two drivers reading the same thread —
// the reviewer's Stop event on the live hook path, and reconcilePRLoops on its
// timer — and one event reached both within a minute: each read the approved
// verdict before the other had recorded it, each posted pr-review-passed, and
// each ran the merge, the second finding nothing to ship or failing to un-draft
// a pull request that was already merged and redding a finished card (SC-5089,
// campaign LOC-3/LOC-4). The thread cannot arbitrate this: the marker that
// would tell the second driver to stop is posted by the first one only after
// it has decided. So the arbitration is in memory, where both drivers live —
// a second drive for a ticket already being driven does nothing, and the
// reconcile pass's next tick re-reads a thread the first drive has finished
// writing.
var prLoopDrives sync.Map

// beginPRLoopDrive claims the ticket's loop for one drive; false means another
// drive holds it and this one must stand down.
func beginPRLoopDrive(pmKey string) bool {
	_, held := prLoopDrives.LoadOrStore(pmKey, struct{}{})
	return !held
}

func endPRLoopDrive(pmKey string) { prLoopDrives.Delete(pmKey) }
