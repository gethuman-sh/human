import { test } from "node:test";
import assert from "node:assert/strict";
import { runGuardedAction } from "../build/board-actions.js";

// SC-637 regression: a board action's error banner must survive the reconcile
// that used to run unconditionally right after it. runGuardedAction is the
// shared seam every call site (transition, fixBug, deployFixedBugs,
// createMocks, the idea-column drop, closeTicket) goes through so the mistake
// fixed once for closeTicket() (SC-195) can't be reintroduced elsewhere. This
// fails before board-actions.ts exists (the import resolves to nothing).

test("SC-637: a shown error survives — onSuccess (reconcile) never runs after a failure", async () => {
  const state = { error: "" };
  const showError = (msg) => {
    state.error = msg;
  };
  const reconcile = async () => {
    state.error = "";
  };

  await runGuardedAction(
    () => Promise.reject(new Error("daemon command failed: launch blocked by failing agent skills check")),
    (err) => showError(err instanceof Error ? err.message : String(err)),
    reconcile,
  );

  assert.equal(state.error, "daemon command failed: launch blocked by failing agent skills check");
});

test("a resolved action runs onSuccess and never calls onError", async () => {
  let onErrorCalls = 0;
  let onSuccessCalls = 0;
  await runGuardedAction(
    () => Promise.resolve(),
    () => {
      onErrorCalls++;
    },
    async () => {
      onSuccessCalls++;
    },
  );
  assert.equal(onErrorCalls, 0);
  assert.equal(onSuccessCalls, 1);
});

test("a rejected action never runs onSuccess, whatever the rejection's shape", async () => {
  for (const rejection of [new Error("boom"), "plain string", { code: 500 }]) {
    let onSuccessCalls = 0;
    let received;
    await runGuardedAction(
      () => Promise.reject(rejection),
      (err) => {
        received = err;
      },
      async () => {
        onSuccessCalls++;
      },
    );
    assert.equal(onSuccessCalls, 0);
    assert.equal(received, rejection);
  }
});
