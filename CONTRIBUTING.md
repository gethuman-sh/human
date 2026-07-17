# Contributing to human

Thanks for contributing! Pull requests are welcome.

## Contributor License Agreement

Before a pull request can be merged, you must sign our
[Contributor License Agreement](CLA.md) (adapted from the Apache Individual
CLA v2.2). You keep the copyright to your contribution; the CLA grants the
project a broad license to use it.

Signing is fully automated: when you open your first pull request, the CLA
Assistant bot posts a comment with instructions. Reply with

```
I have read the CLA Document and I hereby sign the CLA
```

and the check turns green. You only sign once — the signature covers all your
future contributions and is recorded in the `cla-signatures` branch.

## Pull requests

- Open an issue first for anything non-trivial so the approach can be agreed on.
- Reference the issue from the PR description (e.g. `Closes #123`).
- Run `make check` before pushing — it runs tests, lint, and coverage.
- Keep PRs focused: one logical change per PR.

## Development

```sh
make build   # build the human binary
make test    # run tests
make lint    # run linters
make check   # everything CI runs
make hooks   # install the commit-msg hook (issue-ref enforcement)
```
