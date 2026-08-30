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

## Alternative Runtime — Numaflow (design study, no implementation)

- `NUMAFLOW/` holds a hybrid design that runs the streaming/ETL half on
  [Numaflow](https://numaflow.numaproj.io/) and keeps a Go + Temporal layer for the
  parts Numaflow does not provide (queryable PStates, cross-partition transactions,
  ordering, atomic conditional writes, migrations).
- Start at [`NUMAFLOW/README.md`](NUMAFLOW/README.md); behavior in
  [`NUMAFLOW/PRD.md`](NUMAFLOW/PRD.md); Go/Temporal architecture in
  [`NUMAFLOW/TECHSPEC.md`](NUMAFLOW/TECHSPEC.md); per-scenario pipeline configs in
  `NUMAFLOW/pipelines/*.yaml`.
- Storage tiering for the PState service (`stated`): **Pebble** is the MVP engine
  (pure Go, atomic synced-batch commit, stream/hot PStates); **IsleDB** on
  Cloudflare R2 / AWS S3 is the pure-Go scale-out tier for microbatch/large/range
  PStates (SlateDB is rejected as it is Rust/CGO). See `NUMAFLOW/TECHSPEC.md` §3.1.
- This is a study, not the mandated runtime; the parent `PRD.md`/`TECHSPEC.md`
  (Temporal + custom Go dataflow runtime) remain the primary path. It does not
  override this file.
- Dev: `mise run numaflow:dev` (Go services via `Procfile.numaflow`);
  `mise run numaflow:apply` / `numaflow:validate` for the pipeline manifests.

## Implementation Status & Learnings

### Current checkpoint — gallery modules complete, commands/tutorial pending

Completed and verified:

- Typed module/depot/PState/topology identities, deterministic power-of-two partitioning, ordered depots, acknowledged append/retry, contiguous checkpoints, replay, and PState rebuild
- In-memory conformance storage, durable Pebble storage/reopen and snapshot support, stream and microbatch runtimes, and exactly-once mutation/checkpoint commits
- All six gallery module packages and deterministic behavior tests: Profile, TimeSeries, TopUsers, BankTransfer, REST integration, and MusicCatalog migration
- Reusable Temporal lifecycle control-plane helper with heartbeat/liveness workflow; Sessions 1 and 2 are wired to it
- Concrete structs with replaceable function fields; no project-defined interfaces
- `go test -race ./...`, `go vet ./...`, and `go build ./...` pass at this checkpoint
- Pebble is pinned to `v2.1.6`; Go 1.27 requires the pinned `cockroachdb/swiss` compatibility commit used by Pebble `v2.1.7`

Remaining work, which may proceed in parallel:

1. Session 3–6 standalone commands, Temporal wiring, Procfiles, air configs, and mise tasks
2. Quick Tutorial 1–5 progression and Tutorial 6 RamaSpace acceptance suite
3. Process/crash-boundary durability tests for acknowledged appends and exactly-once commits
4. Standalone Temporal end-to-end tests, current coverage measurement, and final MVP hardening/release gate

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

1. **Session 3–6 commands and dev tooling** (`cmd/session-3` through `cmd/session-6`, `Procfile.session-N`, `cmd/session-N/.air.toml`, and `mise` `session-N:dev`/`session-N:run`). Use the existing module package and `sessionapp.StartControlPlane`; follow the Session 2 pattern. No new runtime work is required.
2. **Quick tutorial stages 1–6** (`cmd/quick-tutorial`). This is a learning progression using the same runtime; it can be implemented and tested against `EXAMPLE` tutorial reference independently of the gallery commands.
3. **Pebble crash-boundary and snapshot tests** (`internal/storage` and `internal/microbatch/runtime_test.go`). Add durable-reopen tests that verify acknowledged records are not lost and committed effects are not duplicated across process restarts.
4. **Temporal end-to-end and `mise` hardening**: add tests that start the standalone Temporal test server for Session 1 and Session 2, wire `mise run check` to include end-to-end where appropriate, and bring coverage back above the 80% rule.

No hard blockers remain. The design cautions below do not block the parallel tracks.

## Design issues to revisit later

- `internal/topusers` recomputes the global top-spending list by scanning every `UserTotalSpend` partition. Replace this with a true global-partition aggregation before distributed/multi-worker execution.
- `internal/banktransfer.GetFunds` returns `int`; change balances and public APIs to `int64` before targeting 32-bit platforms or very large balances.
- The fixed `1<<20` limits in TimeSeries, TopUsers, BankTransfer, and MusicCatalog scans require pagination or streaming cursors for indexes that may exceed one million entries.


