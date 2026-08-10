// The card states the daemon forwards for the frontend to render (mirrors
// internal/daemon BoardState, minus the idle default ""). A Go test
// (board_states_contract_test.go) asserts this list equals the daemon's
// forwardable states; a frontend test asserts badgeInfo covers every entry —
// so the two halves cannot drift apart in silence (SC-3024).
export const DAEMON_FORWARDED_STATES = ["running", "queued", "done", "failed", "resolved", "outage"];
// The pipeline-level flow states the daemon forwards (mirrors the daemon.BoardFlow*
// constants in internal/daemon/protocol.go). A Go test (board_flow_contract_test.go)
// asserts this list equals the daemon's set and a frontend test asserts flowNotice
// handles every entry — so, exactly like DAEMON_FORWARDED_STATES, neither half can
// gain a state the other has never heard of (SC-3577).
export const FLOW_STATES = ["flowing", "idle", "stalled", "unknown"];
