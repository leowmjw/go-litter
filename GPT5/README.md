# Ramaspace (High‑Volume Edition)

- Temporal **Updates** (PartitionWorkflowV2) with **Continue-As-New** rotation.
- **Pebble sharded per process**; append-only feed + retention helper.
- In-memory and Pebble stores, aggregator counters (Merge), resolvePosts.
- Unit tests for stores and a tiny load test.
- A simple demo worker/client that starts a workflow and sends a couple of updates.

## Run
```bash
go test ./...
go run ./cmd/ramaspace
```
