# PRD — Go/Temporal Rama Workshop

## Objective

Build a workshop that ports Rama's programming model and examples to idiomatic Go while using Temporal as the high-level orchestrator and control plane.

The workshop has two phases:

1. Port all six Java examples and their observable test behavior from `EXAMPLE/` into sequential `cmd/session-N` workshops.
2. Complete `cmd/quick-tutorial`, following Rama Tutorial 1–6 and culminating in the RamaSpace social-network application.

Temporal MUST manage module lifecycle, desired worker topology, liveness, batch scheduling, checkpoints, and recovery. Temporal MUST NOT execute each dataflow operation as a Workflow or Activity. Depot ingestion, ETL/dataflow execution, PState mutation, and query execution remain inside standalone Go dataflow processes.

## Source Material and Precedence

1. `AGENTS.md` contains mandatory project rules and overrides this PRD if they conflict.
2. `EXAMPLE/` is the canonical Java gallery reference, imported from [redplanetlabs/rama-demo-gallery](https://github.com/redplanetlabs/rama-demo-gallery) at commit `7b988d986af132a12e5281107e7c0c69287ed704`.
3. The Rama tutorial is a six-page sequence:
   - [Tutorial 1 — First module](https://redplanetlabs.com/docs/~/tutorial1.html)
   - [Tutorial 2 — Depots, ETLs, and PStates](https://redplanetlabs.com/docs/~/tutorial2.html)
   - [Tutorial 3 — Distributed programming](https://redplanetlabs.com/docs/~/tutorial3.html)
   - [Tutorial 4 — Dataflow programming](https://redplanetlabs.com/docs/~/tutorial4.html)
   - [Tutorial 5 — Types of ETLs](https://redplanetlabs.com/docs/~/tutorial5.html)
   - [Tutorial 6 — Tying it all together](https://redplanetlabs.com/docs/~/tutorial6.html)

The Java sources are references for behavior and concepts, not an architecture mandate. The Go implementation should use Go and Temporal idioms while preserving the documented semantics.

## Deliverables

- Six runnable gallery workshop sessions under `cmd/session-1` through `cmd/session-6`.
- One final tutorial under `cmd/quick-tutorial`.
- A reusable Go dataflow runtime shared by the sessions and final tutorial.
- A Temporal control plane that can launch, monitor, recover, and update module processes.
- Standalone in-process test harnesses for depots, partitions, PStates, stream processing, microbatches, and queries.
- Deterministic unit and integration tests requiring no external services.
- Full end-to-end tests using a Temporal Test Server only after unit and integration tests pass.
- `mise`, `overmind`, and `air` development commands and files as specified by `AGENTS.md`.

## Gallery Workshop Sequence

Each session MUST port the corresponding Java module and the behaviors asserted by its Java tests. Use the source and test paths under `EXAMPLE/` as the acceptance reference.

### Session 1 — ProfileModule

References:

- `EXAMPLE/src/main/java/rama/gallery/profiles/`
- `EXAMPLE/src/test/java/rama/gallery/ProfileModuleTest.java`

Teach and demonstrate:

- Low-latency stream topology.
- Depot partitioning by username and user ID.
- Globally unique user ID generation.
- Atomic, duplicate-safe username registration.
- Append acknowledgement that returns the generated user ID.
- Profile creation, multi-field edits, and partial edits that preserve untouched fields.
- Repartitioning from username-keyed work to user-ID-keyed state.

### Session 2 — TimeSeriesModule

References:

- `EXAMPLE/src/main/java/rama/gallery/timeseries/`
- `EXAMPLE/src/test/java/rama/gallery/TimeSeriesModuleTest.java`

Teach and demonstrate:

- Microbatch processing and asynchronous append semantics.
- Typed combiners for cardinality, total, latest, minimum, and maximum latency.
- Minute, hour, day, and 30-day materialized aggregations.
- Sorted, subindexed range queries.
- Server-side reductions and a distributed range-query topology.
- Unit testing a custom operation independently from the runtime.

### Session 3 — TopUsersModule

References:

- `EXAMPLE/src/main/java/rama/gallery/topusers/`
- `EXAMPLE/src/test/java/rama/gallery/TopUsersModuleTest.java`

Teach and demonstrate:

- Microbatch sub-batches and two-stage aggregation.
- Per-user accumulated spend.
- Global partitions and global PState.
- Monotonic top-N computation and replacement when rankings change.
- Server-side projection of user IDs from ranked tuples.

### Session 4 — BankTransferModule

References:

- `EXAMPLE/src/main/java/rama/gallery/banktransfer/`
- `EXAMPLE/src/test/java/rama/gallery/BankTransferModuleTest.java`

Teach and demonstrate:

- Exactly-once microbatch effects.
- Idempotency by transfer ID.
- Conditional debit based on available funds.
- Cross-partition transfer processing.
- Consistent outgoing and incoming transfer indexes for both successful and failed transfers.
- Balance preservation when a transfer fails.

### Session 5 — RestAPIIntegrationModule

References:

- `EXAMPLE/src/main/java/rama/gallery/restapi/RestAPIIntegrationModule.java`
- `EXAMPLE/src/test/java/rama/gallery/RestAPIIntegrationModuleTest.java`

Teach and demonstrate:

- Process/task-scoped resource lifecycle.
- Asynchronous I/O integrated with a stream topology.
- Partitioning requests by URL and storing the latest response.
- Dependency injection around external I/O.

The upstream Java test performs a real network request and has no assertions. The Go port MUST replace that behavior with deterministic assertions and a standard-library fake or `httptest.Server`; workshop unit and integration tests MUST NOT depend on the public internet.

### Session 6 — MusicCatalogModules

References:

- `EXAMPLE/src/main/java/rama/gallery/migrations/`
- `EXAMPLE/src/test/java/rama/gallery/MusicCatalogModulesTest.java`

Teach and demonstrate:

- Multiple compatible versions of one logical module.
- Live module update under a stable module name.
- Typed PState schema migration.
- Idempotent migration functions.
- Existing records migrated to the new schema and new records written directly in the new schema.
- Coexistence and safe rollout of worker/module versions.

## Final Quick Tutorial Sequence

`cmd/quick-tutorial` MUST preserve the six-page learning progression rather than starting directly with RamaSpace.

### Tutorial 1 — First Module

Demonstrate:

- Cluster as execution environment and module as deployable executable.
- Conductor, supervisor, and worker responsibilities through the Go/Temporal mapping.
- Module launch with explicit partitions and worker resources.
- Depot append, stream ETL source, operation execution, and querying.
- A fully in-process Hello World test harness.

### Tutorial 2 — Depots, ETLs, and PStates

Demonstrate:

- Event sourcing: depots are append-only sources of truth.
- PStates are typed, partitioned materialized views derived by ETLs.
- PStates may be nested maps, sets, lists, scalars, or fixed-key records.
- ETLs are the only writers to their PStates.
- Point queries, range queries, server-side transforms, and distributed query topologies.
- Rebuilding a PState by replaying its depot.

### Tutorial 3 — Distributed Programming

Demonstrate:

- A module is split into a power-of-two number of logical tasks/partitions.
- Every task owns a partition of each depot and PState plus an event queue.
- Records on one depot partition preserve append order; no global ordering exists across partitions.
- Keyed depot partitioning is required for causally ordered updates.
- Repartitioning moves computation to the task owning a key.
- Co-location reduces network hops and enables task-local atomic updates.

### Tutorial 4 — Dataflow Programming

Demonstrate:

- Operations receive input and emit zero, one, or many outputs.
- Immutable scoped bindings between nodes.
- Linear pipelines and named output streams.
- Branches, anchors/hooks, unification, and conditionals.
- A binding used after a branch must be present on every branch reaching that point.
- Reusable custom operations and graph fragments.
- Loop state, continue, and emit semantics.

The Go API does not need to copy Rama's Java syntax, but it MUST make these semantics observable and testable.

### Tutorial 5 — Stream and Microbatch ETLs

Preserve the distinction:

| Property | Stream | Microbatch |
| --- | --- | --- |
| Processing | Record as it arrives | Coordinated accumulated batch |
| Ordering | Ordered within a depot partition | Batch covers unprocessed input across partitions |
| Throughput | Medium | High |
| Failure model | Record-level tracking and configured retry | Re-run uncommitted batch |
| Delivery/state effect | At-least-once or at-most-once policy | Exactly-once committed PState effects |
| Append integration | Append may wait for downstream completion | Append does not wait for batch completion |
| Test synchronization | Assert after acknowledged append | Advance/wait for a committed batch checkpoint |

The first implementation MAY use a configurable 30-second microbatch window. Tests MUST use virtual time rather than sleeping for 30 seconds. A near-real-time target around one second is deferred, and the configuration MUST allow later reduction without redesigning the runtime.

### Tutorial 6 — RamaSpace

Build the complete social-network capstone described below.

## Runtime Architecture

### Go Module Process

A Go module process represents one deployed Rama-style Module and hosts:

- Depot definitions and append APIs.
- Stream and microbatch topology definitions.
- Partition/task event loops.
- Typed PStates and their indexes.
- Query topologies and point-query APIs.
- Batch checkpoints and idempotency metadata.
- Health and progress reporting to the control plane.

A dataflow node is an operation inside this module process; it is not independently supervised by Temporal.

### Temporal Control Plane

Temporal Workflows and Activities manage:

- Module launch and update intent.
- Desired task/worker allocation and compatible worker versions.
- Worker registration and heartbeats.
- Liveness evaluation at least once per minute.
- Microbatch scheduling, leases, and committed checkpoints.
- Recovery of dead workers and re-execution of uncommitted batches.
- Visibility into module version, task ownership, health, and progress.

Temporal MUST NOT hold large PStates in Workflow history and MUST NOT model each dataflow operation as an Activity. Workflow state contains orchestration metadata only.

### Failure and Recovery Semantics

- A worker that misses the configured liveness threshold is considered unavailable.
- The control plane reassigns its tasks and re-runs only records or microbatches not durably acknowledged.
- Stream processing tracks completion per record and follows its configured at-least-once or at-most-once retry policy.
- Microbatch processing commits PState changes and its checkpoint atomically, or provides an equivalent batch-ID deduplication protocol, so retries have exactly-once visible effects.
- Batch replay MUST be safe after process death at every point before, during, and after state commit.
- Raw depot data remains replayable so materialized views can be rebuilt.

## Core Data Model

### Depot

- Named append-only event log.
- Configurable typed payload.
- Configurable random or deterministic key partitioner.
- Monotonic per-partition position.
- Append acknowledgement mode appropriate to the topology type.

### Task/Partition

- Stable logical identifier selected by consistent deterministic hashing.
- Owns one partition of every depot and PState in its module.
- Serializes events that require task-local ordering while allowing independent tasks to run concurrently.

### PState

- Typed partitioned materialized view.
- Supports scalar, map, set, list, fixed-key record, and nested schemas needed by the examples.
- Supports subindexed sorted maps/sets for bounded page and range reads without loading the whole collection.
- Supports local atomic transforms and aggregations.
- Supports schema versions and idempotent migrations.

### Dataflow Topology

- Directed graph of typed operations.
- Supports immutable bindings, branching, joining/unification, conditionals, custom operations, loops, repartitioning, aggregation, and local PState access.
- The same execution concepts SHOULD be reusable by ETL and query topologies.

## RamaSpace Functional Requirements

### Users and Profiles

- Register a user with a unique user ID, email, display name, and password hash.
- Reject a duplicate user ID without a check-then-append race.
- Use a registration UUID/idempotency token to let the client determine whether its attempt won.
- Update an allowed profile field without replacing unrelated fields.
- Fetch the password hash for login.
- Fetch public profile data.
- Post a comment on any user's wall.
- Return wall posts in reverse chronological pages of at most 20.
- Return the number of posts on a wall.

### Friendships

- Create and cancel a friend request.
- List incoming and outgoing requests in pages of at most 20.
- Accept a request using one event that both clears pending requests and creates the friendship.
- Maintain friendship edges bidirectionally.
- Check whether two users are friends.
- List friends in deterministic pages of at most 20.
- Return friend count.
- Remove a friendship bidirectionally.

Request and cancel events for the same initiating user MUST share a depot partition so their processing order matches append order.

### Analytics

- Record profile views.
- Aggregate counts by user and hour bucket.
- Return the sum over a requested inclusive/exclusive hour range as defined by the query API.

## RamaSpace Event Types

Use strongly typed Go values with explicit validation and deterministic serialization for:

- `UserRegistration`: user ID, email, display name, password hash, registration UUID.
- `ProfileEdit`: user ID, field, value.
- `FriendRequest`: user ID, destination user ID.
- `CancelFriendRequest`: user ID, destination user ID.
- `FriendshipAdd`: two user IDs.
- `FriendshipRemove`: two user IDs.
- `Post`: author user ID, destination user ID, content.
- Profile-view event: destination user ID and timestamp.
- `Profile`: public profile query result.
- `ResolvedPost`: author user ID, content, display name, and profile picture.

## RamaSpace Depots and Partitioning

- `userRegistrations`: hash by user ID.
- `profileEdits`: hash by user ID.
- `profileViews`: hash by viewed/destination user ID.
- `friendRequests`: carry request and cancellation events; hash by initiating user ID.
- `friendshipChanges`: carry add and remove events; hash by first user ID.
- `posts`: hash by wall/destination user ID.

Events that jointly change state MUST be represented by one depot append whenever splitting them could leave partial state after a client crash.

## RamaSpace PStates

- `profiles`: `map[userID]ProfileRecord`, where `ProfileRecord` has display name, email, profile picture, bio, location, password hash, joined-at milliseconds, and registration UUID.
- `outgoingFriendRequests`: `map[userID]sorted-set[userID]`, subindexed.
- `incomingFriendRequests`: `map[userID]sorted-set[userID]`, subindexed.
- `friends`: `map[userID]sorted-set[userID]`, subindexed and bidirectional.
- `posts`: `map[wallUserID]sorted-map[postID]Post`, subindexed and ordered for reverse chronology.
- `postID`: task-local monotonic ID state sufficient to uniquely order posts on one wall partition.
- `profileViews`: `map[userID]sorted-map[hourBucket]count`, subindexed.

## RamaSpace Topologies

### Users Stream Topology

- Atomically create a profile only when the user ID is absent.
- Persist the winning registration UUID.
- Acknowledged append completes after the profile update is visible.
- Apply valid profile field edits on the user-ID partition.

### Friends Stream Topology

- Add/remove outgoing and incoming request entries.
- On friendship add/remove, clear pending requests in both directions and update friendship edges in both directions.
- Preserve low-latency acknowledged append semantics.

### Posts Microbatch Topology

- Explode each coordinated batch into posts.
- Generate a task-local post ID ordered for reverse-chronological pagination.
- Store posts on the destination wall partition.

### Profile Views Microbatch Topology

- Bucket timestamps by hour.
- Count views by destination user and hour.
- Commit each batch exactly once.

### Resolve Posts Query Topology

- Fetch at most 20 posts from a user's wall starting at a cursor.
- Resolve each post author's display name and profile picture from `profiles`.
- Return to the origin partition and preserve post order.
- Complete in one client round trip.

## Required RamaSpace Queries

- Register user and return success/failure.
- Get password hash.
- Get public profile.
- Get incoming/outgoing friend-request pages.
- Get friend count and friendship existence.
- Get a page of friends.
- Get post count.
- Resolve a page of posts.
- Get profile-view count over an hour range.

Large nested collections MUST be paged or reduced on the server/dataflow process. Queries MUST NOT transfer an entire unbounded PState to the client to calculate size, range, or sum.

## Testing and Acceptance

### General

- All new cases require at least 80% coverage.
- Tests MUST be deterministic and MUST NOT depend on external databases, public HTTP endpoints, or a separately running Temporal service.
- Unit tests cover partitioners, dataflow operations, schemas, PState paths/transforms, aggregators, checkpointing, deduplication, and migrations.
- Integration tests run complete modules in the standalone Go in-process harness.
- Temporal Go testsuite covers orchestration, heartbeats, timeout/recovery, retries, updates, and worker versions.
- Use `RegisterDelayedCallback` to simulate the 30-second microbatch boundary, one-minute liveness checks, dead workers, and long-running workflows without real sleeps.
- Use `testing/synctest` where appropriate for concurrent Go runtime behavior.
- Full end-to-end tests may start Temporal Test Server only after unit and integration suites pass.

### Stream Assertions

After an acknowledged stream append returns, all colocated stream processing for that record is complete and its PState effects are queryable. Tests MUST also cover failure and the configured retry policy.

### Microbatch Assertions

Appending does not imply processing completion. Tests explicitly advance or wait for a committed microbatch checkpoint before querying state. Tests MUST inject failure around commit boundaries and prove retries do not duplicate visible effects.

### RamaSpace Minimum Acceptance Cases

- First registration for a user ID succeeds; a duplicate returns false.
- Password hash and profile edits are visible after acknowledged stream processing.
- Friend requests, cancellation, acceptance, pagination, counts, and unfriend maintain all bidirectional invariants.
- Twenty-four wall posts resolve into a first page of 20 and a second page of 4 with author display data.
- Profile-view hour-range queries return correct sums.
- Worker death is detected by virtual one-minute liveness time and only unacknowledged work is replayed.
- Replayed microbatches have exactly-once PState effects.
- A module schema/version update preserves existing state and allows old and new worker versions to coexist during rollout.

## MVP Boundaries

- Begin with a configurable 30-second microbatch window and deterministic virtual-time tests.
- Preserve interfaces needed for a future near-real-time setting around one second.
- Start with in-memory or local-disk PState/depot implementations behind interfaces; distributed production storage is outside the initial workshop scope.
- Temporal is required for orchestration tests and workshop control-plane examples, not for individual dataflow operations.
- Production CI/CD and full end-to-end deployment are advanced work and begin only after unit and integration requirements pass.
