# Replay corpus

`replay-corpus.json` is a recorded set of real ticket marker histories, oldest
first, marker **types** only — no bodies, no titles. It is the evidence half of
the pipeline machine: `docs/pipeline-fsm.json` says how an item moves, and these
are threads the pipeline itself wrote while moving them.

`replay-baseline.json` is every disagreement between the two that the corpus
currently shows. It is a worklist, not an allowlist — see the file's own `doc`.

## Refreshing it

The corpus deliberately does not refresh itself: a fixture that re-fetches is a
fixture that changes under you, and a test whose input moves cannot tell you the
machine moved. Refresh by hand when you want newer evidence, and read the diff —
a sequence shape that has never appeared before is the interesting part.

```sh
mkdir -p /tmp/markers
./bin/human shortcut issues list --all \
  | jq -r 'sort_by(.updated_at) | reverse | .[0:80] | .[].key' \
  | while read -r k; do ./bin/human marker list "$k" > "/tmp/markers/$k.json"; done

for f in /tmp/markers/*.json; do
  jq -c --arg k "$(basename "$f" .json)" \
     'select(length>0) | {key: $k, markers: ([.[].type] | reverse)}' "$f"
done | jq -s 'sort_by(.key)' > internal/pipelinefsm/testdata/replay-corpus.json
```

`human marker list` returns newest first, hence the `reverse`.
