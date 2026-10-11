# Sandbox compatibility (srt)

Gas Town agents run inside [srt](https://www.npmjs.com/package/@anthropic-ai/sandbox-runtime), a macOS seatbelt
sandbox. Under srt:

- `kill(pid, 0)` on a live process outside the sandbox returns `EPERM`, not success. `internal/pidalive` treats
  `EPERM` as "alive" and is used at every liveness-check site.
- A direct `connect()` to the tmux socket returns `EPERM`. `ensureLiveSocketSafe` in
  `internal/tmux/socket_guard_unix.go` falls through to the `list-sessions` probe when the dial is denied
  (`EPERM`/`EACCES`) instead of failing.

The `Sandbox compat` workflow (`.github/workflows/sandbox-compat.yml`) builds gt on macOS, runs the compat unit
tests, then runs two probe tests inside a real srt. It runs on pushes to `town/**`, on pull requests touching
`internal/**`, `go.mod`/`go.sum` or the workflow, weekly, and on demand. The `macos-15` / srt `0.0.79` leg is
pinned and must stay green; the `macos-latest` / srt `latest` leg detects drift.

## Probe tests

Both are ordinary Go tests that skip unless their env var is set, so `go test ./...` is unaffected.

| Test | Env var | What it checks |
|------|---------|----------------|
| `internal/pidalive` `TestPidaliveProbe` | `PIDALIVE_PROBE_PID` | `pidalive.Alive` and `pidalive.Proc` are true for a live process outside srt. Under `SANDBOX_RUNTIME=1` it also asserts the old `p.Signal(0) == nil` idiom is false (canary that srt is confining). |
| `internal/tmux` `TestSandboxSocketProbe` | `GT_SANDBOX_PROBE_SOCKET` | With a real tmux server on that socket name started outside srt, `ensureNewSessionSocketSafe()` returns nil from inside srt, where the direct dial is denied and a fake `tmux` on `PATH` answers `list-sessions`. Under `SANDBOX_RUNTIME=1` it also asserts the dial was really denied. |

## Running the probes locally

Needs macOS, `srt` (`npm i -g @anthropic-ai/sandbox-runtime`), `tmux`, ripgrep, and for building gt the keg-only
icu4c (`export CGO_CPPFLAGS="-I$(brew --prefix icu4c)/include" CGO_LDFLAGS="-L$(brew --prefix icu4c)/lib"`).
Run from the repo root, outside any sandbox:

```bash
probe=$(mktemp -d); mkdir "$probe/bin" "$probe/tmp"
cat > "$probe/probe.json" <<'JSON'
{"network":{"allowedDomains":[],"deniedDomains":[]},"filesystem":{"denyRead":[],"allowRead":[],"allowWrite":["."],"denyWrite":[]}}
JSON
printf '#!/bin/sh\ncase " $* " in *" list-sessions "*) echo probe ;; esac\nexit 0\n' > "$probe/bin/tmux"
chmod +x "$probe/bin/tmux"
go test -c -o "$probe/pidalive.test" ./internal/pidalive
go test -c -o "$probe/tmux.test" ./internal/tmux
cd "$probe"

# Smoke: fails if this machine cannot run seatbelt.
srt -s probe.json -c 'test "$SANDBOX_RUNTIME" = 1'

# pidalive: a live process outside the sandbox.
sleep 600 & sleeper=$!
srt -s probe.json -c "PIDALIVE_PROBE_PID=$sleeper TMPDIR=\$PWD/tmp ./pidalive.test -test.v -test.run '^TestPidaliveProbe\$'"
kill $sleeper

# tmux: a real server outside the sandbox, on a socket srt may not dial.
sock=gt-srt-probe-$$
tmux -L $sock new-session -d -s probe 'sleep 600'
srt -s probe.json -c "PATH=\$PWD/bin:\$PATH GT_SANDBOX_PROBE_SOCKET=$sock TMPDIR=\$PWD/tmp ./tmux.test -test.v -test.run '^TestSandboxSocketProbe\$'"
tmux -L $sock kill-server
```

Each probe must print `--- PASS`; a `--- SKIP` means the env var did not reach the test.
