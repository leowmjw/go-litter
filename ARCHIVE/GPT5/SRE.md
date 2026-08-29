# SRE Runbook

## Objective

Here’s a tight, actionable **SRE Runbook** you (or an automated agent) can follow to detect and remediate “hitting Pebble limits,” plus exact ways to observe every signal.

---

## Pebble SRE Runbook

### 0) What “hitting the limit” means (map of failure signals)

**Primary pressure signals**

* **Write stall events**: Pebble emits “write stall begin/end” events via `EventListener.WriteStallBegin` / `WriteStallEnd`. Use these as hard “backpressure started/stopped” signals. ([Go Packages][1])
* **L0 pressure**: watch **L0 sublevels** and **L0 file count** (`Metrics.Levels[0].Sublevels`, `Metrics.Levels[0].NumFiles`). More sublevels/files ⇒ worse read/compaction amp & higher stall risk. ([Go Packages][1])

  * CockroachDB’s admission defaults have historically treated **~20 L0 sublevels** and **~1000 L0 files** as backpressure points; useful starting thresholds. ([GitHub][2])
* **Compaction debt**: `Metrics.Compact.EstimatedDebt` (bytes Pebble estimates must be compacted to return to a stable state). Sustained high debt = compactions falling behind. ([Go Packages][1])

**Supporting signals**

* **WAL I/O health**: `Metrics.LogWriter.FsyncLatency` (Prometheus histogram) and WAL sizes/counts (`Metrics.WAL.*`). Spikes indicate device pressure. ([Go Packages][1])
* **Snapshot pinning**: `Metrics.Snapshots.Count`, `PinnedKeys`, `PinnedSize`—kept-high values impede compaction. ([Go Packages][1])
* **Obsolete/zombie files**: `Metrics.Table.Obsolete*` / `Zombie*` (cleanup/iterator pressure). ([Go Packages][1])
* **Disk-slow / stall events**: subscribe via `EventListener` (e.g., `DiskSlow`/write stall hooks). ([Go Packages][1])

---

### 1) Observability: exact metrics to expose

Pebble provides a rich **`pebble.Metrics`** struct via `db.Metrics()` (per-level metrics, compaction debt, WAL, table cache, snapshots, etc.). It also exposes **write stall begin/end** via `Options.AddEventListener`. ([Go Packages][1])

#### 1.1 Minimal Prometheus exporter (Go)

Expose these gauges/counters/histograms (names below are **exact**, so your agents can depend on them):

```go
// scrape every 5s
pebble_l0_sublevels{db}                // gauge        -> db.Metrics().Levels[0].Sublevels
pebble_l0_num_files{db}                // gauge        -> db.Metrics().Levels[0].NumFiles
pebble_compaction_estimated_debt_bytes{db}
                                        // gauge       -> db.Metrics().Compact.EstimatedDebt
pebble_compactions_in_progress{db}     // gauge        -> db.Metrics().Compact.NumInProgress
pebble_compaction_bytes_read_total{db,level}
                                        // counter     -> db.Metrics().Levels[i].BytesRead
pebble_compaction_bytes_compacted_total{db,level}
                                        // counter     -> db.Metrics().Levels[i].BytesCompacted
pebble_wal_size_bytes{db}              // gauge        -> db.Metrics().WAL.Size
pebble_wal_files{db}                   // gauge        -> db.Metrics().WAL.Files
pebble_write_stall_total{db}           // counter      -> increment in EventListener.WriteStallBegin
pebble_write_stall_seconds_total{db}   // counter(sec) -> sum durations between Begin/End
pebble_disk_slow_total{db}             // counter      -> increment EventListener.DiskSlow (if used)
pebble_logwriter_fsync_latency_seconds_bucket|sum|count{db}
                                        // histogram   -> register Metrics.LogWriter.FsyncLatency
pebble_snapshots_open{db}              // gauge        -> db.Metrics().Snapshots.Count
pebble_snapshots_pinned_keys{db}       // counter      -> db.Metrics().Snapshots.PinnedKeys
pebble_snapshots_pinned_size_bytes{db} // counter      -> db.Metrics().Snapshots.PinnedSize
pebble_table_obsolete_bytes{db}        // gauge        -> db.Metrics().Table.ObsoleteSize
```

Sources for fields & the EventListener hooks: ([Go Packages][1])

> Notes
>
> * The `FsyncLatency` field is already a Prometheus histogram you can register directly. ([Go Packages][1])
> * Everything else: poll `db.Metrics()` on a fixed interval and set gauges/counters.

---

## 2) PromQL queries the agent can run

> These are **copy/paste ready** and refer to the exact metric names above.

**A. Write stall detection**

```promql
increase(pebble_write_stall_total[5m]) > 0
```

**B. L0 pressure**

```promql
pebble_l0_sublevels > 20
pebble_l0_num_files > 1000
```

(20 sublevels / 1000 files are pragmatic defaults taken from CockroachDB admission control discussion; tune per hardware.) ([GitHub][2])

**C. Compaction debt saturation time (seconds)**

```promql
pebble_compaction_estimated_debt_bytes
  / clamp_max(rate(pebble_compaction_bytes_compacted_total[15m]), 1)
```

Alert if > 900s (15m) for 5m — the engine is not catching up. (Debt definition: “bytes that need compaction to reach a stable state.”) ([GitHub][3])

**D. WAL health**

```promql
histogram_quantile(0.99, sum by (le, db) (rate(pebble_logwriter_fsync_latency_seconds_bucket[5m]))) > 0.010
```

(p99 fsync > 10ms usually indicates device pressure.)

**E. Snapshot pinning**

```promql
(pebble_snapshots_open > 0)
and (increase(pebble_snapshots_pinned_keys[10m]) > 0 or increase(pebble_snapshots_pinned_size_bytes[10m]) > 0)
```

**F. Obsolete buildup**

```promql
rate(pebble_table_obsolete_bytes[10m]) > 0 and pebble_table_obsolete_bytes > 5e9
```

---

### 3) Alert rules (Prometheus)

```yaml
groups:
- name: pebble-limits
  rules:
  - alert: PebbleWriteStall
    expr: increase(pebble_write_stall_total[2m]) > 0
    for: 1m
    labels: { severity: critical }
    annotations:
      summary: "Pebble write stall detected"
      runbook_url: "pebble://runbook#stall"

  - alert: PebbleL0PressureHigh
    expr: (pebble_l0_sublevels > 20) or (pebble_l0_num_files > 1000)
    for: 5m
    labels: { severity: warning }
    annotations:
      summary: "High L0 pressure (sublevels/files)"
      runbook_url: "pebble://runbook#l0"

  - alert: PebbleCompactionDebtNotCatchingUp
    expr: (pebble_compaction_estimated_debt_bytes
           / clamp_max(rate(pebble_compaction_bytes_compacted_total[15m]), 1)) > 900
    for: 5m
    labels: { severity: critical }
    annotations:
      summary: "Compaction debt not catching up"
      runbook_url: "pebble://runbook#debt"

  - alert: PebbleWALFsyncP99High
    expr: histogram_quantile(0.99, sum by (le, db) (rate(pebble_logwriter_fsync_latency_seconds_bucket[5m]))) > 0.010
    for: 10m
    labels: { severity: warning }
    annotations:
      summary: "WAL fsync p99 > 10ms"
      runbook_url: "pebble://runbook#wal"

  - alert: PebbleSnapshotPinning
    expr: (pebble_snapshots_open > 0)
          and (increase(pebble_snapshots_pinned_keys[10m]) > 0 or increase(pebble_snapshots_pinned_size_bytes[10m]) > 0)
    for: 10m
    labels: { severity: warning }
    annotations:
      summary: "Snapshots are pinning data"
      runbook_url: "pebble://runbook#snapshots"
```

---

### 4) Automated self-check contract (machine-readable)

Agents can call Prometheus or scrape `/metrics`, then emit this JSON:

```json
{
  "component": "pebble",
  "status": "OK|WARN|CRIT",
  "checks": [
    {"name":"write_stall_recent","ok": true, "evidence":"increase(pebble_write_stall_total[5m])=0"},
    {"name":"l0_sublevels","ok": false, "value": 27, "threshold": 20},
    {"name":"l0_files","ok": true, "value": 612, "threshold": 1000},
    {"name":"compaction_debt_seconds","ok": false, "value": 1540},
    {"name":"wal_fsync_p99_seconds","ok": true, "value": 0.004},
    {"name":"snapshot_pinning","ok": true}
  ],
  "advise": [
    "Reduce producer rate 30% for 5m",
    "Raise compaction concurrency by +2",
    "Investigate long-lived snapshots"
  ]
}
```

---

### 5) Runbook actions (what to do when an alert fires)

#### A) On **write stall** (critical)

1. **Throttle producers** immediately (Temporal side):

   * Lower per-queue **TaskQueueActivitiesPerSecond** or reduce worker concurrency (`MaxConcurrentActivityExecutionSize`) to drain backlog. ([docs.temporal.io][4])
   * If using Temporal Cloud, verify you’re not also hitting namespace/task-queue limits (RPS/APS). ([docs.temporal.io][5])
2. **Increase compaction capacity**: bump **`MaxConcurrentCompactions`** (Pebble option), then restart the process with the higher setting if needed. This is a common practice in high-ingest deployments (e.g., geth). ([GitHub][6])
3. **Check L0**: if `Sublevels > 20` or `L0 files >> 1000` persist for >5–10m, consider **temporary ingest slow-start**, then re-evaluate. (Thresholds from CockroachDB admission defaults.) ([GitHub][2])
4. **Confirm media health**: WAL p99 fsync ≤ 10ms; otherwise move WAL to faster storage or investigate node I/O. ([Go Packages][1])

#### B) **Compaction debt not catching up** (critical)

* Treat as “compaction behind write rate.”
  Actions: raise compaction concurrency; verify there’s disk headroom (compactions can transiently double bytes) and watch `InProgressBytes`. ([Go Packages][1])

#### C) **Snapshot pinning** (warning)

* Identify long-lived snapshots; ensure app closes them; if they’re intentional, consider **checkpoint + truncate** strategy in low-traffic windows. (Use `Snapshots.EarliestSeqNum` to see how far back pinning reaches.) ([Go Packages][1])

#### D) **Obsolete buildup**

* This usually clears once iterators release; verify no long-lived iterators; if needed, schedule maintenance compaction.

---

### 6) Operator dashboard (what to plot)

* **Health row**: `pebble_l0_sublevels` (L0), `pebble_l0_num_files`, `pebble_compaction_estimated_debt_bytes`.
* **Throughput row**: rate of `pebble_compaction_bytes_compacted_total` vs write rate (your app).
* **WAL row**: p99 `pebble_logwriter_fsync_latency_seconds`, `pebble_wal_size_bytes`.
* **Stalls**: `increase(pebble_write_stall_total[1h])`.
* **Snapshots**: `pebble_snapshots_open`, `increase(pebble_snapshots_pinned_size_bytes[1h])`.

(Each field is sourced from Pebble’s public metrics API.) ([Go Packages][1])

---

### 7) Why these signals/thresholds? (Evidence & justification)

* **Write stall events** are first-class in Pebble’s `EventListener` API (subscribe and count). They represent the engine’s own backpressure decision, so we escalate immediately when they appear. ([Go Packages][1])
* **L0 sublevels/files** directly drive read/compaction amplification; Pebble exposes them per-level. The CockroachDB team has discussed defaults of **20 sublevels** and **1000 files** for admission control; these make sensible **warning** thresholds for generic deployments. ([Go Packages][1])
* **Compaction debt** is Pebble’s own definition of “how far behind we are,” so we alert on **debt / recent compaction throughput** to estimate “seconds to catch up.” ([GitHub][3])
* **WAL fsync latency** comes from a native Prometheus histogram in Pebble (`LogWriter.FsyncLatency`), so it’s a reliable I/O barometer. ([Go Packages][1])
* **Snapshot pinning** is quantified (`PinnedKeys`, `PinnedSize`) and a known cause of compaction inefficiency; exposing and alerting on growth catches snapshot/iterator leaks. ([Go Packages][1])

---

### 8) Temporal knobs to pair with Pebble backpressure (to avoid cascading failures)

When Pebble alarms fire, use Temporal to **shape traffic**:

* **Reduce per-queue start rate**: `TaskQueueActivitiesPerSecond`. ([docs.temporal.io][4])
* **Lower worker concurrency** temporarily (e.g., `MaxConcurrentActivityExecutionSize`) and/or **worker pollers** to drop load. ([docs.temporal.io][4])
* **Watch server-side limits** (namespace RPS/APS) to ensure your throttling won’t hit Temporal’s rate gates instead. ([docs.temporal.io][5])

---

### 9) Sanity checks / investigations

* **Is the DB inverted (high read amp / L0 bloat)?**—if L0 sublevels/files stay high with debt up, you’re inverted and compactions aren’t keeping up; prioritize compaction capacity. ([Cockroach Labs][7])
* **Does increasing `MaxConcurrentCompactions` help?**—many high-ingest users do this (e.g., geth); re-check p99 fsync after changes. ([GitHub][6])
* **Benchmark headroom**—Pebble itself measures “optimal write throughput until a heuristic fails (L0 sublevels/files or write stall).” If your production hotspots resemble this, you’re capacity-limited rather than misconfigured. ([cockroachdb.github.io][8])

---

#### Appendix A — Minimal exporter skeleton (pseudo-code)

(For your platform team to adapt as needed.)

```go
// Wire EventListener for stalls, register Prom metrics, and poll db.Metrics() every 5s.
// Sources for fields/events: Pebble package docs.
```

([Go Packages][1])

---

If you want, I can drop a ready-to-run Prometheus exporter (single Go file) using these exact names and a tiny Grafana JSON with the dashboard rows above.

[1]: https://pkg.go.dev/github.com/cockroachdb/pebble "pebble package - github.com/cockroachdb/pebble - Go Packages"
[2]: https://github.com/cockroachdb/cockroach/issues/79159?utm_source=chatgpt.com "storage: reconsider the L0-sublevels / L0 files default limits"
[3]: https://github.com/cockroachdb/pebble/blob/master/docs/rocksdb.md?utm_source=chatgpt.com "pebble/docs/rocksdb.md at master · cockroachdb/pebble"
[4]: https://docs.temporal.io/develop/worker-performance?utm_source=chatgpt.com "Worker performance | Temporal Platform Documentation"
[5]: https://docs.temporal.io/cloud/limits?utm_source=chatgpt.com "System limits - Temporal Cloud | Temporal Platform Documentation"
[6]: https://github.com/ethereum/go-ethereum/blob/master/ethdb/pebble/pebble.go?utm_source=chatgpt.com "go-ethereum/ethdb/pebble/pebble.go at master - GitHub"
[7]: https://www.cockroachlabs.com/docs/stable/architecture/storage-layer?utm_source=chatgpt.com "Storage Layer - CockroachDB Docs"
[8]: https://cockroachdb.github.io/pebble/?utm_source=chatgpt.com "Pebble Benchmarks - GitHub Pages"

