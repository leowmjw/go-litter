# NUMAFLOW — Hybrid mapping of the Rama gallery onto Numaflow + Go/Temporal

This folder documents how the six Rama gallery examples and the six-page quick
tutorial (culminating in RamaSpace) can be run on **[Numaflow](https://numaflow.numaproj.io/)**
for the streaming / ETL portion, with a **Go + Temporal** control-and-state layer
covering everything Numaflow deliberately does not provide.

> Status: **design + configuration only. No Go implementation in this folder yet.**
> The `pipelines/*.yaml` files are Numaflow `Pipeline` / `ServingPipeline`
> manifests (configuration, not code). Container images referenced by them are
> placeholders; the Go UDF/UDSink/query/state code is specified in
> [`TECHSPEC.md`](./TECHSPEC.md) but not implemented here.

## Why hybrid (the one-paragraph justification)

Numaflow is a Kubernetes-native **stream processor**: a DAG of `Source`, `UDF`
(map/reduce), and `Sink` vertices connected by durable Inter-Step Buffers, with a
controller that handles lifecycle, autoscaling, and rolling updates
([pipeline](https://numaflow.numaproj.io/core-concepts/pipeline/),
[vertex](https://numaflow.numaproj.io/core-concepts/vertex/)). It intentionally
"decouples event sources and sinks from the processing logic"
([home](https://numaflow.numaproj.io/)) — meaning **state lives in sinks, not in
Numaflow**. Rama's power is the opposite: `Depot → ETL → PState` where the
**PState is a durable, indexed, queryable, partitioned materialized view**, plus
cross-partition transactionality and per-partition ordering. Numaflow gives us the
depot+ETL half for free; the Go/Temporal layer supplies the PState, queries,
transactions, ordering, and migrations.

## Division of responsibilities

| Concern | Owner | Evidence / rationale |
|---|---|---|
| Event transport (depots) | **Numaflow** Source + Inter-Step Buffer (Kafka/JetStream) | Durable buffers, backpressure ([pipeline](https://numaflow.numaproj.io/core-concepts/pipeline/)) |
| Stream ETL | **Numaflow** map UDF + conditional forwarding | [vertex](https://numaflow.numaproj.io/core-concepts/vertex/) |
| Windowed/microbatch aggregation | **Numaflow** reduce UDF (fixed/sliding/session/accumulator) | [reduce](https://numaflow.numaproj.io/user-guide/user-defined-functions/reduce/reduce/) |
| Module lifecycle / liveness / autoscale / rolling update | **Numaflow controller** | replaces most of the Temporal "control plane" role in the parent PRD |
| **PState** (durable, indexed, queryable, partitioned view) | **Go State service** (`stated`); **Pebble** (MVP/hot) → **IsleDB** on R2/S3 (scale-out) | Numaflow has no queryable state store; reduce PVC is replay-only ([reduce → Storage](https://numaflow.numaproj.io/user-guide/user-defined-functions/reduce/reduce/)). Storage tiering: [TECHSPEC §3.1](./TECHSPEC.md) |
| Point / range / paginated **queries** | **Go query service** (+ optional Numaflow `ServingPipeline`) | [serving](https://numaflow.numaproj.io/core-concepts/serving/) |
| **Cross-partition transactions** (bank transfer, friend-accept) | **Temporal** saga workflows | Numaflow has no distributed/multi-key atomicity |
| **Atomic conditional writes** (unique registration, idempotency) | **Go State service** (compare-and-set) coordinated by Temporal where multi-step | Numaflow map/reduce cannot do atomic CAS |
| **Per-partition ordering** guarantee | **Go layer** (keyed routing + sequence numbers + reorder buffer) | Numaflow: "Preserving order is not required" ([home](https://numaflow.numaproj.io/)) |
| **Exactly-once effects** | **Numaflow** (unbounded sources) + **Go** idempotent-by-eventID sink | [home](https://numaflow.numaproj.io/) |
| **Schema/PState migration** + version coexistence | **Temporal** migration workflow + Numaflow update strategy / blue-green pipelines | Numaflow doesn't own state, so migration is external |

## Scenario → manifest index

| Scenario | Numaflow manifest | Fit | Gaps handled by Go/Temporal |
|---|---|---|---|
| Session 1 — Profiles | [`pipelines/session-1-profiles.yaml`](./pipelines/session-1-profiles.yaml) | 🟡 | atomic unique registration, `profiles` PState, queries |
| Session 2 — TimeSeries | [`pipelines/session-2-timeseries.yaml`](./pipelines/session-2-timeseries.yaml) | 🟢 agg / 🔴 query | sorted range-query index + query service |
| Session 3 — TopUsers | [`pipelines/session-3-topusers.yaml`](./pipelines/session-3-topusers.yaml) | 🟡 | durable running top-N PState + query |
| Session 4 — BankTransfer | [`pipelines/session-4-banktransfer.yaml`](./pipelines/session-4-banktransfer.yaml) | 🔴 | **cross-partition transfer saga (Temporal)**, idempotency, indexes |
| Session 5 — RestAPI | [`pipelines/session-5-restapi.yaml`](./pipelines/session-5-restapi.yaml) | 🟢 | latest-response PState + query |
| Session 6 — MusicCatalog | [`pipelines/session-6-musiccatalog.yaml`](./pipelines/session-6-musiccatalog.yaml) | 🟡 | typed schema migration workflow, version coexistence |
| Capstone — RamaSpace | [`pipelines/ramaspace.yaml`](./pipelines/ramaspace.yaml) | 🟡 | all PStates, friend-accept transaction, pagination, resolve-posts query |

Tutorials 1–5 are teaching progressions rather than separate deployables; each maps
onto a subset of the above manifests and is described in [`PRD.md`](./PRD.md).

## Shared conventions (used by every manifest)

- **Event envelope** (JSON on every message): `eventId`, `type`, `key`, `eventTime`
  (ms epoch), `seq` (per-key monotonic), `payload`. See TECHSPEC §Event envelope.
- **Depots** are Kafka topics (durable + replayable). The `http` source variant is
  provided for local/dev ingest; production uses `kafka`.
- **State service** `stated` is reached by UDF/UDSink containers over gRPC at
  `$STATE_STORE_ADDR`; it is the PState layer.
- **Temporal** is reached at `$TEMPORAL_HOSTPORT`; task queues are named
  `<module>-tx`.
- Container images use the placeholder prefix `ghcr.io/rama-workshop/`.
- Reduce vertices declare `storage` (PVC) purely for **window checkpoint replay**,
  not as the queryable store.

## How the docs fit together

- [`PRD.md`](./PRD.md) — product behavior of the hybrid system and per-tutorial mapping.
- [`TECHSPEC.md`](./TECHSPEC.md) — the Go + Temporal architecture that fills the gaps,
  the Pebble→IsleDB storage tiers (§3.1), and the milestone plan including the
  **IsleDB compatibility spike (S1)** that gates the scale-out tier (§12).
- `pipelines/*.yaml` — the Numaflow configuration for each scenario.

This folder does not override `../AGENTS.md`, `../PRD.md`, or `../TECHSPEC.md`; it is
an alternative runtime study for the same requirements.
