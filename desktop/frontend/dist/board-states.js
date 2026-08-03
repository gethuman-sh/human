// The card states the daemon forwards for the frontend to render (mirrors
// internal/daemon BoardState, minus the idle default ""). A Go test
// (board_states_contract_test.go) asserts this list equals the daemon's
// forwardable states; a frontend test asserts badgeInfo covers every entry —
// so the two halves cannot drift apart in silence (SC-3024).
export const DAEMON_FORWARDED_STATES = ["running", "queued", "done", "failed", "resolved", "outage"];
