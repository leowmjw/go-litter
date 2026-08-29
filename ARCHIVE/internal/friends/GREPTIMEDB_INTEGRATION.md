# GreptimeDB Integration Suggestions for Friends Package

## Current Architecture Review

The `friends` package has a clean, idiomatic Go design:
- **Relations**: Low-level data operations (function fields for flexibility)
- **Friendship**: Business logic layer (composes Relations)
- **UserFriendship**: User-scoped convenience wrapper

This design is **excellent** and should remain unchanged. The separation allows us to swap storage backends without touching business logic.

---

## GreptimeDB Integration Strategy

### 1. **New Implementation: `greptime.go`**

Create a new `greptimeRelations` that implements the same `Relations` interface pattern, using GreptimeDB as the storage backend.

#### Key Design Decisions:

**A. Time-Series Model for Friendship Events**
- Store friendship operations as **events** with timestamps
- Use GreptimeDB's time-series capabilities for analytics and audit trails
- Materialize current state via SQL queries (PState rendering)

**B. Table Schema Design**

```sql
-- Friendship events table (append-only log)
CREATE TABLE friendship_events (
    ts TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    user_a STRING TAG,           -- Always lexicographically smaller
    user_b STRING TAG,           -- Always lexicographically larger
    event_type STRING FIELD,     -- 'friend_request', 'accept', 'unfriend', 'cancel'
    initiator STRING FIELD,      -- Who initiated the action
    PRIMARY KEY (ts, user_a, user_b)
);

-- Friend requests table (current state)
CREATE TABLE friend_requests (
    ts TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    from_user STRING TAG,
    to_user STRING TAG,
    status STRING FIELD,         -- 'pending', 'accepted', 'cancelled'
    created_at TIMESTAMP FIELD,
    PRIMARY KEY (ts, from_user, to_user)
);

-- Friendships table (current state - bidirectional)
CREATE TABLE friendships (
    ts TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    user_a STRING TAG,           -- Lexicographically smaller
    user_b STRING TAG,           -- Lexicographically larger
    created_at TIMESTAMP FIELD,
    PRIMARY KEY (ts, user_a, user_b)
);
```

**C. Implementation Pattern**

```go
// greptime.go
package friends

import (
    "context"
    "time"
    
    "github.com/GreptimeTeam/greptimedb-ingester-go"
    "github.com/GreptimeTeam/greptimedb-ingester-go/table"
    "github.com/GreptimeTeam/greptimedb-ingester-go/table/types"
    
    "golitter/internal/types"
)

type greptimeRelations struct {
    client *greptime.Client
    ctx    context.Context
}

func newGreptimeRelations(client *greptime.Client) *greptimeRelations {
    return &greptimeRelations{
        client: client,
        ctx:    context.Background(),
    }
}

// Helper: Normalize user pair (always a < b lexicographically)
func normalizePair(a, b types.UserID) (string, string) {
    if a < b {
        return string(a), string(b)
    }
    return string(b), string(a)
}

func (g *greptimeRelations) addRelation(a, b types.UserID) error {
    userA, userB := normalizePair(a, b)
    now := time.Now()
    
    // Insert into friendships table (bidirectional)
    tbl, err := table.New("friendships")
    if err != nil {
        return err
    }
    
    tbl.AddTagColumn("user_a", types.STRING)
    tbl.AddTagColumn("user_b", types.STRING)
    tbl.AddFieldColumn("created_at", types.TIMESTAMP_MILLISECOND)
    tbl.AddTimestampColumn("ts", types.TIMESTAMP_MILLISECOND)
    
    err = tbl.AddRow(userA, userB, now, now)
    if err != nil {
        return err
    }
    
    // Also log as event
    eventTbl, err := table.New("friendship_events")
    if err != nil {
        return err
    }
    eventTbl.AddTagColumn("user_a", types.STRING)
    eventTbl.AddTagColumn("user_b", types.STRING)
    eventTbl.AddFieldColumn("event_type", types.STRING)
    eventTbl.AddFieldColumn("initiator", types.STRING)
    eventTbl.AddTimestampColumn("ts", types.TIMESTAMP_MILLISECOND)
    
    err = eventTbl.AddRow(userA, userB, "accept", string(a), now)
    if err != nil {
        return err
    }
    
    _, err = g.client.Write(g.ctx, tbl)
    if err != nil {
        return err
    }
    _, err = g.client.Write(g.ctx, eventTbl)
    return err
}

func (g *greptimeRelations) checkRelation(a, b types.UserID) (bool, error) {
    userA, userB := normalizePair(a, b)
    
    // Query current state
    query := `SELECT COUNT(*) as cnt 
              FROM friendships 
              WHERE user_a = ? AND user_b = ? 
              ORDER BY ts DESC 
              LIMIT 1`
    
    // Use GreptimeDB query API (if available) or direct SQL
    // For now, use ingester's query capabilities
    // Note: greptimedb-ingester-go focuses on ingestion
    // You may need to use a separate SQL client for queries
    
    // Placeholder - actual implementation depends on query API
    return false, nil
}
```

---

## 2. **PState Rendering with SQL**

Since GreptimeDB supports SQL, you can render PStates directly via SQL queries instead of maintaining separate materialized views.

### Example: Friends PState Query

```sql
-- Get all friends for a user (bidirectional query)
SELECT 
    CASE 
        WHEN user_a = ? THEN user_b 
        ELSE user_a 
    END as friend_id,
    created_at
FROM friendships
WHERE (user_a = ? OR user_b = ?)
  AND ts = (SELECT MAX(ts) FROM friendships f2 
            WHERE f2.user_a = friendships.user_a 
              AND f2.user_b = friendships.user_b)
ORDER BY created_at DESC;
```

### Example: Friend Requests PState Query

```sql
-- Get incoming requests for a user
SELECT from_user, created_at
FROM friend_requests
WHERE to_user = ? 
  AND status = 'pending'
  AND ts = (SELECT MAX(ts) FROM friend_requests f2 
            WHERE f2.from_user = friend_requests.from_user 
              AND f2.to_user = friend_requests.to_user)
ORDER BY created_at DESC;
```

---

## 3. **Key Improvements & Benefits**

### A. **Event Sourcing Pattern**
- **Benefit**: Complete audit trail of all friendship operations
- **Use Case**: Analytics, debugging, time-travel queries
- **Example**: "Show all friendship changes in the last 24 hours"

### B. **Time-Series Analytics**
- **Benefit**: Built-in time-series functions (aggregations, windowing)
- **Use Case**: "Friendship growth rate", "Most active friend requesters"
- **Example SQL**:
```sql
SELECT 
    DATE_TRUNC('hour', ts) as hour,
    COUNT(*) as friend_requests_per_hour
FROM friendship_events
WHERE event_type = 'friend_request'
  AND ts > NOW() - INTERVAL '24 hours'
GROUP BY hour
ORDER BY hour;
```

### C. **Automatic Retention Policies**
- **Benefit**: GreptimeDB can automatically expire old events
- **Use Case**: Keep events for 90 days, but maintain current state forever
- **Configuration**: Set retention policies per table

### D. **Horizontal Scalability**
- **Benefit**: GreptimeDB scales horizontally for high-volume workloads
- **Use Case**: Millions of friendship operations per day
- **Note**: Tag columns (user_a, user_b) enable efficient partitioning

---

## 4. **Implementation Considerations**

### A. **Query vs. Ingestion Split**

The `greptimedb-ingester-go` library focuses on **ingestion**. For queries, you have two options:

**Option 1: Use GreptimeDB's SQL API directly**
```go
import "database/sql"
import _ "github.com/greptime/greptimedb-go-driver" // If available

func (g *greptimeRelations) listRelations(u types.UserID) ([]types.UserID, error) {
    query := `SELECT friend_id FROM (
        SELECT CASE WHEN user_a = $1 THEN user_b ELSE user_a END as friend_id
        FROM friendships
        WHERE (user_a = $1 OR user_b = $1)
        ORDER BY ts DESC
    ) GROUP BY friend_id`
    
    rows, err := g.db.Query(query, u)
    // ... process rows
}
```

**Option 2: Hybrid Approach**
- Use `greptimedb-ingester-go` for writes (high throughput)
- Use direct SQL client for reads (low latency queries)
- Cache frequently accessed PStates in Redis/memory

### B. **Consistency Model**

**Challenge**: GreptimeDB is eventually consistent for distributed deployments.

**Solution**: 
- For critical operations (accept friend request), use synchronous writes
- For analytics (friend count), eventual consistency is acceptable
- Consider using GreptimeDB's transaction support if available

### C. **Normalization Strategy**

**Current Issue**: Bidirectional relationships need special handling.

**Solution**: Always store `(min(user_a, user_b), max(user_a, user_b))` as tags.
- Prevents duplicate entries
- Simplifies queries
- Maintains referential integrity

### D. **Performance Optimization**

**1. Batch Writes**
```go
// Use StreamWrite for high-throughput scenarios
func (g *greptimeRelations) batchAddRelations(pairs []Pair) error {
    tbl, _ := table.New("friendships")
    // ... setup columns
    
    for _, pair := range pairs {
        userA, userB := normalizePair(pair.A, pair.B)
        _ = tbl.AddRow(userA, userB, time.Now(), time.Now())
    }
    
    return g.client.StreamWrite(g.ctx, tbl)
}
```

**2. Materialized Views (PStates)**
- Create GreptimeDB materialized views for frequently queried PStates
- Refresh periodically or on-demand
- Example: `CREATE MATERIALIZED VIEW friends_current AS SELECT ...`

**3. Indexing**
- Tag columns are automatically indexed in GreptimeDB
- Add field indexes for frequently filtered fields (status, event_type)

---

## 5. **Migration Path**

### Phase 1: Add GreptimeDB as New Backend
1. Implement `greptimeRelations` following existing pattern
2. Add `NewGreptimeDB(client *greptime.Client) *Friendship`
3. Write comprehensive tests (mirror existing test suite)

### Phase 2: Dual-Write Strategy
1. Write to both Pebble (existing) and GreptimeDB (new)
2. Validate data consistency
3. Monitor performance impact

### Phase 3: Query Migration
1. Implement SQL-based PState queries
2. Compare results with Pebble implementation
3. Gradually migrate read paths

### Phase 4: Cutover
1. Make GreptimeDB primary
2. Keep Pebble as backup/fallback
3. Eventually deprecate Pebble implementation

---

## 6. **Code Structure**

```
internal/friends/
├── friends.go          # Unchanged (Relations + Friendship)
├── mem.go              # Unchanged
├── pebble.go           # Unchanged
├── greptime.go         # NEW: GreptimeDB implementation
├── greptime_test.go    # NEW: Tests
└── greptime_sql.go     # NEW: SQL query helpers for PState rendering
```

---

## 7. **Example: Complete Implementation Skeleton**

```go
// greptime.go
package friends

import (
    "context"
    "fmt"
    "time"
    
    "github.com/GreptimeTeam/greptimedb-ingester-go"
    "github.com/GreptimeTeam/greptimedb-ingester-go/table"
    "github.com/GreptimeTeam/greptimedb-ingester-go/table/types"
    
    "golitter/internal/types"
)

type greptimeRelations struct {
    client *greptime.Client
    ctx    context.Context
}

func newGreptimeRelations(client *greptime.Client) *greptimeRelations {
    return &greptimeRelations{
        client: client,
        ctx:    context.Background(),
    }
}

func normalizePair(a, b types.UserID) (string, string) {
    if a < b {
        return string(a), string(b)
    }
    return string(b), string(a)
}

func (g *greptimeRelations) addRelation(a, b types.UserID) error {
    userA, userB := normalizePair(a, b)
    now := time.Now()
    
    tbl, err := table.New("friendships")
    if err != nil {
        return err
    }
    
    tbl.AddTagColumn("user_a", types.STRING)
    tbl.AddTagColumn("user_b", types.STRING)
    tbl.AddFieldColumn("created_at", types.TIMESTAMP_MILLISECOND)
    tbl.AddTimestampColumn("ts", types.TIMESTAMP_MILLISECOND)
    
    if err := tbl.AddRow(userA, userB, now, now); err != nil {
        return err
    }
    
    _, err = g.client.Write(g.ctx, tbl)
    return err
}

// Similar implementations for other Relations methods...
// (removeRelation, checkRelation, listRelations, etc.)

// NewGreptimeDB creates a new GreptimeDB-backed Friendship instance
func NewGreptimeDB(client *greptime.Client) *Friendship {
    greptime := newGreptimeRelations(client)
    relations := Relations{
        AddRelation:           greptime.addRelation,
        RemoveRelation:        greptime.removeRelation,
        CountRelations:        greptime.countRelations,
        CheckRelation:         greptime.checkRelation,
        ListRelations:         greptime.listRelations,
        AddOutgoingRequest:    greptime.addOutgoingRequest,
        AddIncomingRequest:    greptime.addIncomingRequest,
        RemoveOutgoingRequest: greptime.removeOutgoingRequest,
        RemoveIncomingRequest: greptime.removeIncomingRequest,
        HasIncomingRequest:    greptime.hasIncomingRequest,
        HasOutgoingRequest:    greptime.hasOutgoingRequest,
        ListOutgoingRequests:  greptime.listOutgoingRequests,
        ListIncomingRequests:  greptime.listIncomingRequests,
    }
    
    f := &Friendship{relations: relations}
    wireBusinessLogic(f)
    return f
}
```

---

## 8. **Questions & Considerations**

### A. **Query API Availability**
- **Question**: Does `greptimedb-ingester-go` support queries, or do we need a separate SQL client?
- **Action**: Check GreptimeDB Go driver availability for queries
- **Fallback**: Use HTTP REST API for queries if no native driver

### B. **Transaction Support**
- **Question**: Does GreptimeDB support transactions for multi-table operations?
- **Impact**: `AcceptFriendRequest` needs atomic: remove requests + add friendship
- **Solution**: Use batch writes or application-level idempotency

### C. **PState Materialization Strategy**
- **Question**: Should PStates be materialized views or computed on-demand?
- **Trade-off**: Materialized = faster reads, more storage. On-demand = slower reads, less storage
- **Recommendation**: Hybrid - materialize hot PStates, compute cold ones

### D. **Backward Compatibility**
- **Question**: How to maintain compatibility with existing Pebble-based code?
- **Solution**: Keep both implementations, use factory pattern to choose backend

---

## 9. **Testing Strategy**

### Unit Tests
- Mirror existing `friends_test.go` structure
- Test all Relations methods
- Test business logic (Friendship methods)

### Integration Tests
- Test against real GreptimeDB instance (Docker)
- Test SQL query correctness
- Test PState rendering accuracy

### Performance Tests
- Benchmark write throughput (events/second)
- Benchmark read latency (PState queries)
- Compare with Pebble implementation

---

## 10. **Summary**

### ✅ **Keep Unchanged**
- `Relations` struct (function fields pattern)
- `Friendship` struct (business logic)
- `UserFriendship` wrapper
- Existing `mem.go` and `pebble.go` implementations

### ➕ **Add New**
- `greptime.go` - GreptimeDB Relations implementation
- `greptime_sql.go` - SQL helpers for PState queries
- Integration tests
- Migration utilities

### 🎯 **Key Benefits**
1. **Event Sourcing**: Complete audit trail
2. **Time-Series Analytics**: Built-in aggregations
3. **SQL PState Rendering**: Flexible querying
4. **Horizontal Scalability**: Distributed architecture
5. **Backward Compatible**: Existing code unchanged

---

## Next Steps

1. **Clarify Query API**: Determine how to query GreptimeDB from Go
2. **Prototype**: Implement `greptimeRelations` for one method (e.g., `addRelation`)
3. **Test**: Verify data consistency with existing implementations
4. **Iterate**: Complete all Relations methods
5. **Document**: Add SQL examples for common PState queries

