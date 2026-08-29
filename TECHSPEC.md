# TECHSPEC — Go/Temporal Rama Workshop

## Purpose

This document defines implementation choices and delivery order. `AGENTS.md` is mandatory and takes precedence; `PRD.md` defines product behavior and acceptance requirements.

The standalone profile and all unit/integration tests MUST run independently as a Go binary without cloud services, containers, MinIO, or a separately running Temporal service. The optional Temporal development profile still starts the sibling Temporal process required by `AGENTS.md`.

## Architecture Principles

- Keep the dataflow runtime independent from storage and orchestration implementations.
- Keep Temporal in the control plane; do not turn individual dataflow nodes into Workflows or Activities.
- Preserve depot replay, deterministic partitioning, ordered processing within a partition, and exactly-once visible microbatch effects.
- Use pure-Go components compatible with `CGO_ENABLED=0`.
- Prefer a small semantic storage contract over exposing Pebble- or IsleDB-specific APIs to modules.
- Implement and verify the single-process model before adding distributed coordination.
- Scale compute independently from durable state where practical.

## Runtime Profiles

### Standalone

- One Go binary.
- In-process coordinator, workers, depots, PStates, queries, and HTTP API.
- In-memory or local Pebble storage.
- No network dependency except loopback listeners started by the binary itself.
- Used by workshops, unit tests, integration tests, and fast local development.

### Temporal Development

- Same dataflow workers and storage adapters as standalone mode.
- Temporal Workflows provide module lifecycle, worker assignment, liveness, batch scheduling, recovery, and updates.
- Launched with the per-session `mise`/`overmind` setup required by `AGENTS.md`.
- Temporal Go testsuite remains the default automated Workflow test environment.

### End-to-End

- Temporal Test Server or development server plus independently running dataflow workers.
- Local object storage may be introduced only in future scale-out tests.
- This profile is not required until unit and integration tests pass.

## Storage Decisions

### In-Memory Adapter

Use a first-party in-memory adapter as the executable specification for storage semantics.

It MUST provide:

- Ordered append positions per depot partition.
- Deterministic scans and snapshots.
- Atomic application of PState mutations, input high-watermarks, batch checkpoint, and deduplication marker.
- Failure injection before and after every commit boundary.
- Replay from a checkpoint.
- Concurrency safety without timing-dependent tests.

The in-memory adapter is the first workshop implementation, but it is not sufficient for restart recovery.

### Durable Local Adapter — Pebble

Use [CockroachDB Pebble](https://github.com/cockroachdb/pebble) as the durable local MVP store.

Reasons:

- Pure Go and compatible with `CGO_ENABLED=0`.
- Production-proven ordered LSM implementation.
- Point reads, bounded iterators, snapshots, and atomic indexed batches match the required PState operations; Pebble's filesystem checkpoint feature is reserved for backup/test snapshots.
- A synced batch can durably publish PState writes and the logical topology-checkpoint key together; this commit protocol does not use `DB.Checkpoint()`.
- One local engine can namespace depot logs, PStates, schema metadata, idempotency markers, and checkpoints.

Constraints:

- Pebble does not provide general distributed transactions; MVP atomicity is limited to one local database commit.
- The first durable implementation uses one Pebble database per module deployment, with task/partition IDs encoded into keys. This permits one atomic microbatch commit across local task partitions.
- Pebble format-major upgrades are permanent. Record the selected library version and format version, test reopen/upgrade behavior, and never ratchet the format implicitly.
- Add Pebble only when the durable milestone begins. Pin a stable release that has been public for at least seven days rather than automatically selecting the newest release.

### Future Object-Storage Adapter — IsleDB Candidate

Evaluate [IsleDB](https://github.com/ankur-anand/isledb) as the preferred pure-Go object-storage candidate for MinIO, Cloudflare R2, and AWS S3.

It aligns with this project because it provides ordered KV access, range scans, snapshots, a fenced writer, independent readers, local reader caches, and separate maintenance over object storage without requiring Rust or CGO.

It is not yet an approved production backend:

- It is a young `v0.x` project.
- One writer owns a database prefix at a time.
- A flush is its durability and reader-visibility boundary.
- Readers may require refresh and second-scale freshness is the expected fit.
- It does not claim general multi-key ACID transactions or compare-and-swap application updates.
- Compaction, retention, and garbage collection require a maintenance process.

IsleDB MUST remain behind the storage interface until a compatibility spike proves the project's commit, replay, fencing, range-query, and recovery invariants. Do not make the scale-out design depend on unverified IsleDB behavior.

SlateDB is not the default alternative because its engine is Rust exposed through Go bindings, which works against the pure-Go and `CGO_ENABLED=0` goals. It may be reconsidered only if those constraints change.

## Storage Contract

Define contracts around required semantics, not backend mechanics.

### Depot Store

The depot store MUST support:

- Append typed encoded records to a named module/depot/partition.
- Assign a strictly increasing position within that partition.
- Read a bounded ordered range from a position.
- Return the durable high-watermark.
- Retain source records needed for replay according to policy.
- Detect duplicate append/idempotency keys where the API requires it.

For the local MVP, encode depot records as ordered Pebble keys. An in-memory implementation uses the same position and ordering rules.

### PState Store

The PState store MUST support:

- Point get.
- Ordered bounded range scan in both required directions.
- Snapshot-consistent reads.
- Atomic local mutation batches.
- Namespaced typed schema and schema-version metadata.
- Idempotent migration markers.
- Rebuild into a new keyspace before atomically switching the active schema/version.

### Microbatch Commit

A commit request contains:

- Stable module and topology identity.
- Module/schema version.
- Batch ID.
- Input high-watermark for every consumed depot partition.
- PState mutations.
- Optional emitted records.

For local memory and Pebble, one atomic commit MUST publish:

1. PState mutations.
2. Deduplication marker for the batch ID.
3. Consumed input high-watermarks.
4. Committed topology checkpoint.

Retrying an already committed batch MUST return its committed result without applying mutations again.

### Key Layout

Use explicit versioned binary key encodings. Logical prefixes should distinguish:

- Module metadata and active version.
- Depot records and partition high-watermarks.
- PState schema/version metadata.
- PState data by state name and task partition.
- Stream acknowledgement/deduplication records.
- Microbatch checkpoints and batch IDs.
- Migration progress and active keyspace.

Keys MUST preserve the intended lexical order for range scans. Do not use ad hoc delimiter-joined user strings without escaping or length-prefixing.

## Serialization and Compatibility

- Use strongly typed Go event and state values.
- Define deterministic versioned binary encodings at storage boundaries.
- Do not use Go `gob` as a long-term persisted format because type/package changes make schema evolution fragile.
- Persist an encoding version with each logical record family.
- Readers MUST either decode supported older versions or return a typed migration-required error.
- Golden compatibility tests MUST open data written by the previous supported module/storage version.

The exact encoding may be selected during the foundation milestone. Prefer the standard library when it meets deterministic evolution needs; otherwise evaluate an existing dependency already compatible with the project rules before adding one.

## Temporal Control-Plane Boundary

Temporal owns orchestration metadata only:

- Desired module version and task count.
- Worker identity, capabilities, and compatible versions.
- Task leases/assignments.
- Heartbeat and liveness status.
- Scheduled microbatch epochs.
- Reported durable checkpoints.
- Recovery and update progress.

Temporal does not own:

- Depot payload history.
- PState values or indexes.
- Per-node dataflow scope.
- Large query results.
- Per-record materialized state.

Workflow tests MUST use the Temporal Go testsuite and `RegisterDelayedCallback` for 30-second batch windows, one-minute liveness checks, worker loss, retries, and version transitions.

## MVP Roadmap

Milestones are sequential. A milestone is complete only when its exit criteria pass; creating files or demonstrating a happy path is not sufficient.

Every milestone also has these cumulative gates:

- Relevant unit and integration tests pass without an external process.
- New and changed code maintains at least 80% coverage.
- Concurrent and timer-driven Go behavior uses `testing/synctest` where applicable.
- Temporal long-running behavior uses the testsuite and `RegisterDelayedCallback`.
- Tests use no wall-clock sleeps.
- The milestone's build, tests, coverage, and static analysis run through `mise`.

### M0 — Contracts and Deterministic Harness

Build:

- Package boundaries for module definitions, runtime, storage, orchestration, and test harness.
- Typed module/depot/PState/topology identities.
- Deterministic partition hashing and power-of-two task validation.
- In-memory depot and PState storage contracts.
- Failure-injection points and virtual clock boundaries.
- Baseline `mise` tasks for tests and coverage plus a reusable template for each workshop session's dev task.

Exit criteria:

- One standalone binary starts and stops cleanly without external services.
- Ordered append, range read, atomic commit, duplicate commit, and replay contract tests pass.
- Race-enabled tests pass for the in-memory adapter.
- No test uses wall-clock sleeps.

### M1 — Stream Runtime and Session 1

Build:

- Task event loops and deterministic routing.
- Stream source/operation/PState pipeline.
- Acknowledged append and record-level retry policy.
- Point and bounded range query primitives.
- Temporal module lifecycle skeleton tested entirely with the testsuite.
- `ProfileModule` workshop in `cmd/session-1`.
- `Procfile.session-1`, `cmd/session-1/.air.toml`, and `mise run session-1:dev`.

Exit criteria:

- All behaviors in the upstream `ProfileModuleTest` are represented by deterministic Go assertions.
- Same-key events preserve order under concurrent appends.
- An acknowledged append does not return before its visible PState commit.
- A simulated worker failure retries only unacknowledged stream work.
- The session runs in standalone and Temporal development profiles.

### M2 — Durable Local Storage and Restart Recovery

Build:

- Pebble-backed depot and PState adapters.
- Synced atomic PState/checkpoint commits.
- Startup schema/version validation.
- Restart from committed checkpoints and replay of unacknowledged input.
- PState rebuild from retained depot records.
- Local snapshot/checkpoint tooling needed by tests.

Exit criteria:

- The shared storage contract passes unchanged against memory and Pebble.
- Kill/reopen tests at every commit boundary show no lost acknowledged append and no duplicated committed effect.
- A PState can be rebuilt from its depot and produces the same query results.
- The standalone binary remains fully functional with no Temporal or object-store process.

### M3 — Microbatch Runtime and Sessions 2–4

Build:

- Configurable 30-second microbatch scheduler with virtual-time tests.
- Coordinated input high-watermarks across local task partitions.
- Exactly-once commit/deduplication protocol.
- Compound aggregation, sub-batches, global partition/state, query topology, and server-side reductions required by the examples.
- `TimeSeriesModule`, `TopUsersModule`, and `BankTransferModule` workshops.
- Per-session Procfiles, air configuration, and `mise run session-N:dev` tasks for sessions 2–4.

Exit criteria:

- All observable assertions from the three upstream Java test classes are ported.
- Failure before commit replays the batch; failure after commit does not duplicate state.
- Range and aggregate queries are bounded and do not load unbounded PStates into clients.
- Bank transfer invariants hold under retry and concurrent unrelated partitions.

### M4 — External I/O, Versions, and Sessions 5–6

Build:

- Task-scoped resource lifecycle and cancellable asynchronous operations.
- Injected HTTP client/server boundary.
- Versioned module registration and compatible worker routing.
- Idempotent PState migrations and active-version switch.
- `RestAPIIntegrationModule` and `MusicCatalogModules` workshops.
- Per-session Procfiles, air configuration, and `mise run session-N:dev` tasks for sessions 5–6.

Exit criteria:

- REST tests use only standard-library fakes or `httptest.Server`, with no public network.
- Resource startup, cancellation, retry, and shutdown are asserted.
- Old stored records migrate exactly once and new records use the new schema.
- Old and new compatible worker versions can coexist during the update test.

### M5 — Quick Tutorial and RamaSpace

Build:

- `cmd/quick-tutorial` in the six tutorial stages required by `PRD.md`.
- Quick-tutorial Procfile, air configuration, and `mise run quick-tutorial:dev` task.
- Complete RamaSpace users, profiles, friendships, posts, analytics, and resolve-posts query.
- Race-free registration with the client registration UUID/idempotency token persisted only for the winning attempt.
- Temporal liveness detection and task recovery using one-minute virtual time.

Exit criteria:

- Every RamaSpace minimum acceptance case in `PRD.md` passes.
- Concurrent duplicate registration attempts produce one winner, and each client can determine its result from its registration UUID.
- Twenty-four posts page as 20 plus 4 and resolve author profile data.
- Friendship request/accept/cancel/remove operations preserve all bidirectional indexes.
- Dead-worker recovery replays only unacknowledged work.
- Standalone and Temporal testsuite paths produce equivalent application results.

### M6 — MVP Hardening and Release Gate

Build:

- Structured logs, runtime metrics, health/readiness endpoints, and diagnostic checkpoint visibility.
- Storage corruption/error propagation tests.
- Graceful shutdown and restart behavior.
- Benchmarks for append, batch commit, point query, and range query.
- Final validation of every per-session `Procfile`, `.air.toml`, and `mise` task created with its session.

Exit criteria:

- Unit and integration tests pass with at least 80% coverage for new code.
- `go test -race ./...`, static analysis, and build pass through `mise`.
- No unit or integration test requires an external process.
- Each session and quick tutorial launches through its documented `mise` command.
- End-to-end CI/CD remains disabled until this gate is satisfied.

## MVP Scope

Included:

- Single-host standalone runtime with multiple logical tasks.
- In-memory and Pebble storage profiles.
- Stream and 30-second microbatch semantics.
- Deterministic partitioning, replay, exactly-once local microbatch effects, migrations, and module versions.
- Temporal control-plane behavior tested in-process.
- Six gallery sessions and the final quick tutorial.

Excluded:

- Online task-count changes and resharding.
- Cross-host atomic transactions.
- Cloud object storage.
- Multi-region replication.
- Automated production deployment.
- Production SLO claims before benchmark and failure-test evidence exists.

## Future Scale-Out Roadmap

These are post-MVP planning gates, not current implementation scope. Do not add IsleDB, MinIO, cloud SDKs, distributed commit machinery, or deployment dependencies during MVP milestones solely for these phases. MVP abstractions should remain limited to semantics already required by the in-memory and Pebble adapters.

### S1 — IsleDB Compatibility Spike

Use IsleDB's file-backed object store first; this keeps the spike within one Go process and requires no MinIO.

Prove:

- Ordered binary key scans used by PStates.
- Stable writer fencing and safe takeover after simulated death.
- Flush durability and reader visibility boundaries.
- Duplicate batch detection after reopen.
- PState mutation and checkpoint publication with no partially visible committed batch.
- Snapshot/replay behavior and migration keyspace switching.
- Bounded cache behavior and reader refresh semantics.
- Compatibility with `CGO_ENABLED=0` and the selected Go version.

Gate:

- If one flush cannot provide the required batch visibility semantics, add an explicit immutable commit-manifest/epoch protocol above IsleDB or reject IsleDB for authoritative PState commits.
- Adopt only a pinned release that has been public for at least seven days and has passed the project's failure suite.

### S2 — Local Object-Storage End-to-End

Build:

- IsleDB adapter against MinIO using one isolated prefix per module or task partition.
- Durable depot segments and PState SSTs/manifests in object storage.
- Dedicated maintenance/compaction process.
- Reader cache and explicit refresh policy.
- Temporal-controlled writer lease ownership mapped to IsleDB fencing.

Gate:

- MinIO is used only by full end-to-end tests and manual development, never unit or integration tests.
- Process-kill, network-fault, stale-reader, compaction, garbage-collection, and writer-takeover tests pass.
- Storage cost/amplification and recovery-time measurements are recorded.

### S3 — Multi-Process Compute Scale-Out

Build:

- Fixed logical task set assigned across multiple worker processes.
- Lease epochs that reject stale workers.
- Task handoff from committed checkpoints.
- Cross-task routing and bounded backpressure.
- Independent query readers with local caches.

Keep compute scaling separate from resharding: adding workers initially redistributes existing tasks without changing partition hashes or task count.

Gate:

- Worker replacement causes no split-brain writer and no duplicate visible batch effect.
- A stale worker cannot commit after its lease epoch changes.
- Query correctness is preserved across reader refresh and task movement.

### S4 — Distributed Microbatch Epochs and Resharding

Build only after fixed-task multi-process operation is stable:

- Stage per-task outputs under a batch epoch.
- Publish a global committed epoch only after every required task commit is durable.
- Recover or discard incomplete epochs deterministically.
- Add power-of-two task-count migration with dual-read/controlled cutover or offline rebuild.

Gate:

- Failure at every coordinator and task commit point preserves exactly-once visible results.
- Resharding retains depot order per key and produces query-equivalent PStates.
- Rollback is possible before final cutover; after cutover, the transition is explicitly irreversible and tested as such.

### S5 — Cloud Object Storage

Promote the proven object-storage profile to Cloudflare R2 and AWS S3.

Build:

- Provider-specific configuration through environment/secrets, never repository files.
- Bucket-prefix isolation, least-privilege IAM, encryption, lifecycle, and retention policies.
- Retry/backoff for provider throttling and transient failures.
- Region-aware worker placement and cache sizing.
- Backup/restore and disaster-recovery exercises.

Gate:

- The same conformance suite passes against MinIO, R2, and S3.
- Recovery-point and recovery-time behavior is measured.
- Provider consistency, conditional-write, and checksum assumptions are explicitly tested.

### S6 — Production Operations and CI/CD

Build:

- End-to-end CI pipeline.
- Versioned deployment and rollback automation.
- Separate compaction/maintenance scaling.
- Metrics for depot lag, batch duration, checkpoint age, retry count, writer fencing, compaction debt, cache hit rate, object-store requests, and query latency.
- Alerts and runbooks based on measured service objectives.
- Capacity and failure testing before declaring supported scale.

This phase begins only after all unit and integration gates are green, as required by `AGENTS.md`.

## Open Decisions

Resolve these through milestone evidence rather than up-front preference:

- Exact deterministic persisted encoding.
- One Pebble database per module versus per worker after the local MVP.
- Whether object-storage depots use IsleDB ordered keys or separate immutable event segments plus a manifest.
- Whether IsleDB flush publication is sufficient for a complete microbatch commit.
- Cache sizing and freshness policy for stream-oriented reads on object storage.
- Online resharding protocol versus offline PState rebuild for the first scale-out release.
