#!/usr/bin/env bash
#
# govulncheck wrapper that filters known false positives.
#
# Why this exists:
#   govulncheck reports Moby/dockerd vulnerabilities whenever the
#   `github.com/docker/docker` module is imported, and it cannot do symbol-level
#   reachability for that `+incompatible` module — so it flags on import alone,
#   not on the specific API we call. We use the module as a client library to
#   talk to a remote Docker Engine API (see internal/devcontainer/docker_engine.go);
#   we never run dockerd, never run AuthZ plugins, and never accept inbound
#   Docker API requests. Each suppressed finding below is either unreachable in
#   our threat model, or reachable only behind a trust boundary we already
#   assume (a hostile container image — which, given the daemon holds host
#   Docker access by design, is already outside our boundary). The rationale is
#   documented per-ID; the archive-PUT one (GO-2026-5746) is an explicit
#   residual-risk acceptance, NOT a "not reachable" claim.
#
#   All findings are unfixed upstream (Fixed in: N/A) so there is no version
#   bump that resolves them. We suppress them here with explicit IDs and a
#   review date so they don't quietly persist forever.
#
# Suppressed findings:
#   GO-2026-4883 (CVE-2026-33997, GHSA-pxq6-2prw-chj9)
#     "Off-by-one error in plugin privilege validation"
#     Server-side: dockerd plugin install path. We don't install Docker plugins.
#
#   GO-2026-4887 (CVE-2026-34040, GHSA-x744-4wpc-v9h2)
#     "AuthZ plugin bypass when provided oversized request bodies"
#     Server-side: dockerd AuthZ plugin enforcement. We don't run AuthZ plugins.
#
#   GO-2026-5668 ("Race condition in docker cp allows creation of arbitrary
#                 empty files on the host via symlink swap")
#     Host-side `docker cp` copy-OUT path that writes extracted entries to a
#     host directory. human never copies container content out to a host path:
#     it only streams an in-memory tar INTO containers via CopyToContainer (the
#     archive PUT API) — never CopyFromContainer and never the `docker cp` CLI.
#     The vulnerable host-write path is unreachable.
#
#   GO-2026-5617 ("Race condition in docker cp allows bind mount redirection to
#                 host path")
#     Same host-side copy-OUT path as GO-2026-5668; human performs no
#     container->host copy, so it is unreachable.
#
#   GO-2026-5746 ("PUT /containers/{id}/archive executes container binary on the
#                 host")
#     REACHABLE — unlike the entries above, human does exercise this API:
#     internal/devcontainer/feature.go copies a devcontainer-feature tar into a
#     freshly created container via CopyToContainer. The exploit requires a
#     malicious container IMAGE (it manipulates archive-extraction paths). human
#     only copies into containers it just created from images the operator
#     declares in devcontainer.json, and the daemon already holds full host
#     Docker access by design — a hostile base image is already outside this
#     trust boundary. Accepted as RESIDUAL RISK pending an upstream fix;
#     re-evaluate if human ever copies into images from untrusted sources.
#
# Review reminder:
#   Re-evaluate this allow-list every time `make upgrade-deps` is run, or at
#   the next quarterly review (next: 2026-07-01). If Moby ships a fixed
#   release, drop the corresponding entry and bump the dependency.
#
# Behaviour:
#   1. Runs `govulncheck -format text ./...` first so the full human-readable
#      report (including any new, unsuppressed findings) appears in build logs.
#   2. Runs `govulncheck -format json ./...` and uses jq to count findings
#      whose OSV id is NOT in the allow-list. Exits 0 only if that count is
#      zero. Exits 1 (and re-prints the offending findings) otherwise.
#   3. The text run's exit code is intentionally ignored — the JSON run is the
#      authoritative gate.

set -uo pipefail

SUPPRESSED=(
  "GO-2026-4883"
  "GO-2026-4887"
  # docker cp / archive-PUT findings — see per-ID rationale in the header.
  # 5668/5617: unreachable (no container->host copy). 5746: accepted residual risk.
  "GO-2026-5668"
  "GO-2026-5617"
  "GO-2026-5746"
)

# Build a jq array literal like ["GO-2026-4883","GO-2026-4887"]
suppressed_jq="["
for id in "${SUPPRESSED[@]}"; do
  suppressed_jq+="\"${id}\","
done
suppressed_jq="${suppressed_jq%,}]"

echo "==> govulncheck (full report)"
go tool govulncheck -format text ./... || true
echo

echo "==> govulncheck (gating, suppressing: ${SUPPRESSED[*]})"

json=$(go tool govulncheck -format json ./... 2>&1)
gv_status=$?
if [ $gv_status -ne 0 ]; then
  # govulncheck itself failed (e.g. network, build error). The text run above
  # already surfaced the message; bubble the failure up.
  echo "govulncheck exited with status $gv_status" >&2
  exit $gv_status
fi

# Pull every finding's OSV id, drop the suppressed ones, dedupe.
unsuppressed=$(echo "$json" \
  | jq -r --argjson suppressed "$suppressed_jq" \
      'select(.finding != null) | .finding.osv | select(. as $id | $suppressed | index($id) | not)' \
  | sort -u)

if [ -z "$unsuppressed" ]; then
  echo "OK: no vulnerabilities outside the suppression list."
  exit 0
fi

echo "FAIL: unsuppressed vulnerabilities found:" >&2
echo "$unsuppressed" >&2
echo >&2
echo "If any of these are also false positives for our threat model, add them" >&2
echo "to the SUPPRESSED list in scripts/govulncheck.sh with justification." >&2
exit 1
