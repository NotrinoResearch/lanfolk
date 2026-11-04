# Contributing to Lanfolk

Thanks for helping. Lanfolk is a virtual LAN for LAN gaming, made by NotrinoResearch with public source code.
This page explains how to work on it. For installing and using Lanfolk see
[docs/SETUP.md](docs/SETUP.md); for running a relay see
[docs/SELF_HOST.md](docs/SELF_HOST.md).

By taking part you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
Report security problems privately as described in [SECURITY.md](SECURITY.md),
not as public issues.

## Language

Everything in the repository is **English**: code, comments, commit messages,
branch names, issues, tests, logs, user interface text and documentation.

## Getting started

You need Go (the version in `go.mod`). `go test -race` needs cgo, so on
Windows install a MinGW-w64 GCC. Docker is needed only for the relay image.

```sh
git clone https://github.com/NotrinoResearch/lanfolk
cd lanfolk
go build ./...
go test -race ./...
```

Run `gofmt` and `go vet ./...` before you commit. Windows-only code is
guarded by build tags; the packages that touch the operating system also have
Linux or stub implementations so that tests run everywhere.

Do not run `go mod tidy` unless your change adds or removes a dependency.
Keep dependencies few and check their licenses: they end up in
`THIRD_PARTY_NOTICES.md` (see "Licenses" below).

## Tests come first

Write the test before the code, in every change:

1. Add a test that fails for the right reason.
2. Make it pass with the smallest change.
3. Run `go test -race ./...` **locally** and make sure it passes before you
   push. A shell pipeline can hide a failing exit code: use
   `set -o pipefail`, or run `go test` directly.

Table-driven and fuzz tests are welcome for parsers. Tests must not depend on
the internet or on a particular machine; tests that need administrator rights
or real hardware must skip themselves cleanly (see `internal/winnet`). Human
test procedures are in [docs/TESTING.md](docs/TESTING.md).

## Continuous integration

GitHub Actions workflows currently run **only when started manually**
(`workflow_dispatch`), so everything is verified locally
([docs/DECISIONS.md](docs/DECISIONS.md), D-002). Do not rely on CI to catch
failures. To enable CI again, add the `push` and `pull_request` triggers back
to the workflow files in `.github/workflows/`.

## Protocol changes

The wire formats and the local control protocol are specified in
[docs/PROTOCOL.md](docs/PROTOCOL.md). Any change that touches a packet format,
a derived key, a limit or a timing value must:

- update `docs/PROTOCOL.md` in the same pull request;
- add an entry to its change log;
- raise the protocol version if old and new clients cannot interoperate.

## Decisions

When you deviate from the implementation plan, or make a decision that others
need to know about, add an entry to [docs/DECISIONS.md](docs/DECISIONS.md):
the next free `D-nnn`, what changed, why, and what it affects.

## No game-specific rules

Lanfolk reproduces LAN behavior; it does not know games. Do not add game
names, per-game port lists or profiles. If a game does not work, find the
missing network behavior (see [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md))
and fix it in general, with a `lanprobe` test for that behavior.

## Secrets and addresses

Never commit credentials, real server addresses, host names, user names or
friend codes. Use the documentation address `203.0.113.10` (TEST-NET-3) and
the name `relay.example.org` in examples. The default relay is injected at
build time from the untracked `build.env` file (see [README.md](README.md));
`.env` and `build.env` are ignored by git.

## Commits and pull requests

- Keep commits small and focused: one logical change each.
- Write the subject in the imperative, at most about 70 characters, for
  example `Add member relay rate limit`. Add a body that explains **why** when
  it is not obvious.
- Do not mix formatting changes with behavior changes.
- In a pull request, say what changed, how you tested it (`go test -race
  ./...` and any manual steps) and link the issue.
- Add or update documentation together with the code.

## Licenses

Lanfolk is licensed under the PolyForm Noncommercial License 1.0.0, and
NotrinoResearch sells commercial licenses ([LICENSING.md](LICENSING.md)).
Before your first pull request is merged you need to agree to the
[Contributor License Agreement](CLA.md): add the sentence it gives to the pull
request description. You keep the copyright in your work.

Do not copy code from projects with incompatible licenses. In particular, do
not copy GPL code (for example WinIPBroadcast); implement the method
independently.

`THIRD_PARTY_NOTICES.md` is generated. After changing dependencies run:

```sh
go run ./scripts/gen-notices
```

and commit the result.
