# AGENTS

## Language

- Use latest Go v1.27.x and its full capabilities
- Always prefer stdlib if available and it make sense

## Orchestration

- Use Temporal + Go SDK to handle the workflows
- Use Temporal testsuite as much as possible to have widest test coverage
- Use advanced techique like registerdelayedcallback to simulate long running system
# - Ensure use Temporal Worker version to ensure multiple workflow versions can co-exists

## Runtime

- Whole system should be fully testable standalone with Go binary
- Use modern Go capabilities (when needed): generics, structured log, built-in http
routing, testing/synctest
- Prefer concrete structs with first-class function fields and anonymous function method replacement; avoid Go interfaces unless a dependency or protocol boundary truly requires one
- Use function replacement and `testing/synctest` to keep behavior deterministic

## Testing

- All new cases should be at least 80% coverage
- Unit tests and inetgration tests MUST be comepleted without needing to spin up any
 external dependencies
- Only Full End-to-End should need to stand up Temporal Test Server

## Tools

- Use mise to run tasks, set env variables, automate
- Setup env like CGO_ENABLED=0, PATH append to use ~/go/bin and loading from .env
- Tools available: ripgrep, fzf, air, goreleaser, watchexec
- Use overmind to start temporal-cli + air
- Each session has its own `Procfile.<session>` at workspace root and its own
  `.air.toml` under `cmd/<session>/`
- Use `overmind start -f Procfile.<session>` via `mise run <session>:dev`

## Specification (MVP)

- Follow `PRD.md` for product behavior and `TECHSPEC.md` for implementation choices and milestone order; neither overrides this file
- `EXAMPLE/` is the canonical Java reference imported from `redplanetlabs/rama-demo-gallery`; use its `README.md`, `src/main/java/`, and `src/test/java/`
- MVP is not complete until all six gallery modules and their test behavior pass in `cmd/session-1` through `cmd/session-6`, followed by the complete Tutorial 1–6/RamaSpace implementation in `cmd/quick-tutorial`
- The MVP deployment target is single-host standalone: local Pebble is authoritative storage and a local standalone Temporal server is the orchestration/control plane; no cloud or object-storage backend is required
- Unit and integration tests remain process-independent and use in-memory/Pebble plus the Temporal testsuite; only local full end-to-end tests may start the standalone Temporal server
- Ask if anything is unsure or contradictory
- Use mise to launch every workshop session and the quick tutorial

## Specification (Advanced)
- Finally CI/CD will use End-to-End Tests
- Implement this ONLY after Unit/Integration tests are passing

## Implementation Status & Learnings

### Current checkpoint — gallery modules complete, quick tutorial UI complete, session commands pending

Completed and verified:

- Typed module/depot/PState/topology identities, deterministic power-of-two partitioning, ordered depots, acknowledged append/retry, contiguous checkpoints, replay, and PState rebuild
- In-memory conformance storage, durable Pebble storage/reopen and snapshot support, stream and microbatch runtimes, and exactly-once mutation/checkpoint commits
- All six gallery module packages and deterministic behavior tests: Profile, TimeSeries, TopUsers, BankTransfer, REST integration, and MusicCatalog migration
- Reusable Temporal lifecycle control-plane helper with heartbeat/liveness workflow; Sessions 1 and 2 are wired to it
- Quick Tutorial 1–5 interactive DataStar pages and in-memory Stage 6 RamaSpace capstone walkthrough, plus mounted JSON API
- Pebble crash-boundary / snapshot durability tests in `internal/storage` and `internal/microbatch`
- Temporal end-to-end tests for Sessions 1–2 and the control-plane lifecycle in `internal/sessionapp` and `internal/controlplane`
- Concrete structs with replaceable function fields; no project-defined interfaces
- `go test -race ./...`, `go vet ./...`, and `go build ./...` pass at this checkpoint
- Pebble is pinned to `v2.1.6`; Go 1.27 requires the pinned `cockroachdb/swiss` compatibility commit used by Pebble `v2.1.7`

Remaining work, which may proceed in parallel:

1. Session 3–6 standalone commands, Temporal wiring, Procfiles, air configs, and mise tasks (owned by another agent)
2. Bring total coverage across `./internal/...` above the 80% gate. The `mise run coverage-gate` task is wired and currently reports 78.1%; targeted tests or broader integration coverage are needed to push it over the line.

### MVP done condition

- Every Java gallery behavior is represented by passing deterministic Go tests for all six sessions
- Tutorial 1–6 and the full RamaSpace acceptance suite pass
- Every session and quick tutorial runs standalone with local Pebble and under the local standalone Temporal control plane
- Unit/integration tests require no external process; local full end-to-end tests start only the local Temporal server
- All new code meets the coverage rule and `mise run check` passes

### Overmind + air per-session convention

- **One Procfile per session** at workspace root: `Procfile.session-1`, `Procfile.session-2`, …
- **Two standard processes** per session Procfile:
  - `temporal`: `temporal server start-dev --db-filename .temporal/temporal.db --metrics-port 8077`
  - `app`: `air -c cmd/session-N/.air.toml`
- **One `.air.toml` per session** at `cmd/session-N/.air.toml`; build output goes to `.air-session-N/`
- **mise task** `session-N:dev` runs `overmind start -f Procfile.session-N`
- `.temporal/` and `.air-session-*/` are git-ignored

## Concise learnings and parallel work bundles

The foundation is in place for multiple agents to work in parallel. What has landed:

- **Storage layer**: in-memory conformance store and durable Pebble store with module/state/partition keying, snapshots, replay, and rebuild.
- **Partitioning scheme**: power-of-two logical tasks, big-endian byte partition keys, and a `partition.New` constructor.
- **Runtime primitives**: `internal/stream` for per-record stream topologies and `internal/microbatch` for multi-source batched topologies with deterministic commit and exactly-once PState updates.
- **Control plane**: `internal/controlplane` lifecycle workflow and `internal/sessionapp/controlplane.go` reusable `StartControlPlane` helper.
- **All six gallery modules**: `profile`, `timeseries`, `topusers`, `banktransfer`, `restapi`, `musiccatalog` have deterministic Go tests matching the Java reference behavior.
- **Build/test gates**: `go build ./...`, `go vet ./...`, and `go test -race ./...` pass.

Independent tracks that can be picked up in parallel:

1. **Session 3–6 commands and dev tooling** (`cmd/session-3` through `cmd/session-6`, `Procfile.session-N`, `cmd/session-N/.air.toml`, and `mise` `session-N:dev`/`session-N:run`). Use the existing module package and `sessionapp.StartControlPlane`; follow the Session 2 pattern. No new runtime work is required. **Another agent owns this track.**
2. **Quick tutorial stages 1–6** (`cmd/quick-tutorial`). ✅ Done — interactive DataStar pages, SSE fragments, mounted RamaSpace API, and HTTP/SSE tests are in place.
3. **Pebble crash-boundary and snapshot tests** (`internal/storage` and `internal/microbatch/runtime_test.go`). ✅ Done — durable-reopen tests verify acknowledged records are not lost and committed effects are not duplicated across process restarts and snapshots.
4. **Temporal end-to-end tests and `mise` hardening**. ✅ Done for tests — Sessions 1–2 and the control-plane lifecycle run against the standalone Temporal test server (`internal/sessionapp`, `internal/controlplane`). The `mise run coverage-gate` task is wired but still reports 78.1%, so the final ≥80% coverage pass remains open.

No hard blockers remain. The design cautions below do not block the parallel tracks.

## Quick tutorial UI checkpoint

The interactive tutorial server is available through `mise run quick-tutorial:tutorial:run` and `mise run quick-tutorial:tutorial:dev`. It uses the existing `internal/tutorial` Stage 1–5 modules and DataStar SSE responses.

### Concise learnings from this track

- **Server-rendered fragments beat text signals for structured output.** HTML lists, definition lists, and cards are patched via `datastar.PatchElements(... WithModeInner())` into a stable container. This avoids needing client-side list iteration while keeping the UI reactive.
- **Every stage owns its own result container ID.** Centralizing error handling through `writeSSEErrorWithResult(w, r, selector, err)` clears the current stage's fragment on failure so stale success state never survives an error.
- **Microbatch teaching requires deterministic time.** Tests and UI that record a timestamp and later query by hour bucket must agree on the bucket; a fixed test timestamp removes hour-boundary flakes.
- **Mount an existing JSON API with `http.StripPrefix`.** The RamaSpace capstone handler was reused unchanged under `/stage6/api/`; only the teaching page and a small snapshot glue function were new code.
- **Keep the DataStar pin explicit and conservative.** `datastar-go` v1.2.0 is paired with the v1.0.2 browser bundle; reassess only when a stable DataStar v2 release exists.

Completed:

- Stage 1 renders the latest acknowledged greeting as a server-rendered result fragment.
- Stage 2 renders scalar, map, set, list, and fixed-key-record PState results plus the server-side transform, including bounded HTML lists.
- Stage 3 renders task ownership, repartitioning results, sent count, and inbox entries.
- Stage 4 renders every emitted immutable binding set as a structured card with deterministic key ordering.
- Stage 5 renders stream and microbatch counts side by side and explicitly distinguishes a pending depot append from a committed microbatch checkpoint.
- Stage 6 hosts a live in-memory RamaSpace module, mounts its existing JSON API below `/stage6/api/`, and provides an interactive registration, friendship, post, profile-view, and explicit-microbatch-advance walkthrough.
- HTTP/SSE tests cover the rendered Stage 1–5 concepts, the complete Stage 6 walkthrough, pre-advance invisibility of microbatch effects, and the mounted RamaSpace API.
- `errorStatus` truncates on rune boundaries, and `writeSSEErrorWithResult` clears the current stage result fragment on failure so stale success state never survives an error.
- Stage 6 uses a fixed microbatch hour bucket for profile views, eliminating a wall-clock hour-boundary flake in both UI and tests.
- `quick-tutorial --help` exits cleanly without a scary error log line.
- The browser currently uses the stable DataStar v1.0.2 bundle with `datastar-go` v1.2.0; reassess the pin when a stable DataStar v2 release is available rather than using an unstable branch silently.

The Stage 6 instance inside the teaching server is deliberately in-memory and manually advanced. The default `cmd/quick-tutorial` mode remains the durable Pebble/Temporal RamaSpace deployment. Do not modify Sessions 3–6 from this tutorial UI track; another workstream owns those commands.

## Design issues to revisit later

- `internal/topusers` recomputes the global top-spending list by scanning every `UserTotalSpend` partition. Replace this with a true global-partition aggregation before distributed/multi-worker execution.
- `internal/banktransfer.GetFunds` returns `int`; change balances and public APIs to `int64` before targeting 32-bit platforms or very large balances.
- The fixed `1<<20` limits in TimeSeries, TopUsers, BankTransfer, and MusicCatalog scans require pagination or streaming cursors for indexes that may exceed one million entries.


