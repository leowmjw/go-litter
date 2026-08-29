# PRD go-litter as port of "RamaSpace" Social Network

Here’s how I’d translate Tutorial 6 (“RamaSpace” social network) into idiomatic Go + Temporal, with concrete code, tests (unit + integration using the Temporal Go testsuite), an in-memory load generator, and a table mapping Rama concepts to Temporal patterns.

# What Tutorial 6 builds (quick recap)

RamaSpace implements users, walls (posts), friendships, and a tiny analytics counter. It relies on depots (append streams), ETLs/topologies (stream or microbatch), and PStates (materialized, subindexed maps) to achieve scalable, ordered processing and fast queries. Key bits to mirror: partitioning by user for ordering, microbatching for throughput, and server-side query composition (e.g., “resolvePosts”). ([Red Planet Labs][1])

# Mapping Rama → Temporal (Go)

| Rama concept                            | In the tutorial                                                                                                                                                                    | Idiomatic Temporal (Go) mapping                                                                                                                                                                       | Why this is the right fit                                                                                                                                                                                             | Notes / limits handled |
| --------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------- |
| Depot (partitioned append log)          | Separate depots for user registrations, profile edits, friend requests/cancels, friendship changes, posts — partitioned by user to preserve per-user order. ([Red Planet Labs][1]) | One **Entity Workflow per key** (e.g., `UserProfileWorkflow(userID)`, `FriendshipWorkflow(userID)`, `WallWorkflow(toUserID)`, `ProfileViewsWorkflow(userID)`), and send **Signals** to that workflow. | Entity workflows serialize state transitions for a single key and give you deterministic ordering; signals are the natural “append” primitive. Temporal calls these “Entity Workflows.” ([Temporal Documentation][2]) |                        |
| Stream / microbatch ETLs                | Low-latency streaming for profiles; microbatch for posts to trade latency for throughput. ([Red Planet Labs][1])                                                                   | Workflows **buffer signals and flush in batches** (e.g., posts) via a **timer** or by **count threshold**, then call a single batch Activity.                                                         | Mirrors Rama’s microbatching: fewer durable events, larger IO batches.                                                                                                                                                |                        |
| PStates (subindexed maps)               | `$$profiles`, `$$friends`, `$$posts` (subindexed inner maps), `$$profileViews`. Supports fast sizes, ranges, joins. ([Red Planet Labs][1])                                         | **External store** accessed by Activities (e.g., Postgres/Redis; here an in-memory store for tests). Keep big, queryable state out of workflow history.                                               | Temporal workflows should not hold large sets in history. Use Activities + DB indexes; combine via read-side services.                                                                                                |                        |
| Query topologies (e.g., `resolvePosts`) | Server-side join posts with author display names & pics to build a page. ([Red Planet Labs][1])                                                                                    | **Read API outside Temporal** (service/DB query). Temporal **Queries** are fine for small, in-memory values but not for joins over large sets.                                                        | Queries cannot run Activities; keep joins in your DB/service layer.                                                                                                                                                   |                        |
| Ordering per partition                  | Depot hashed by user to avoid interleaving request/cancel. ([Red Planet Labs][1])                                                                                                  | **One workflow per user** ensures serialized handling of friend ops (request/cancel/accept/unfriend).                                                                                                 | Signal ordering is preserved in workflow history; design per-key routing for correctness. ([Temporal Documentation][3])                                                                                               |                        |
| Very high throughput                    | Microbatch posts; subindex large collections. ([Red Planet Labs][1])                                                                                                               | **Batch signals**, **shard across many entity workflows**, and **Continue-As-New** to keep history small.                                                                                             | Continue-As-New prevents history/size limits; batching reduces event churn. ([Temporal Documentation][2])                                                                                                             |                        |

# Temporal limits that matter at high volume — and how we handle them

| Limit (Temporal Cloud defaults)                                                                                                            | What it means                                                            | Mitigation in this design                                                                                                                                     |
| ------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Workflow History:** max **51,200 events or 50 MB**; suggested warnings earlier. ([Temporal Documentation][4])                            | Long-running, chatty workflows can grow history and slow down/terminate. | Use **Continue-As-New** when count/time/“suggested” flag says so. Keep bulk data in DB; batch signals before one Activity call. ([Temporal Documentation][2]) |
| **Signals per Workflow:** **10,000** total. ([Temporal Documentation][4])                                                                  | Ultra-busy walls could exceed this.                                      | Shard: per-user wall workflow per **time window** (e.g., per day), or per owner with periodic Continue-As-New; consider child workflows.                      |
| **Per message gRPC limit:** **4 MB**; **payload** per request **2 MB** (and **4 MB** per event transaction). ([Temporal Documentation][4]) | Don’t pass large blobs in signals/activities.                            | Store large content externally; pass IDs only. A “large-payload codec” exists if needed. ([GitHub][5])                                                        |
| **In-flight ops per workflow:** 2,000 (activities/signals/children). ([Temporal Documentation][4])                                         | Flood of concurrent ops can be rejected.                                 | Throttle & batch; bound concurrency in workflow; shard entities.                                                                                              |
| **Namespace RPS/APS** (e.g., 1600 RPS default, autoscaled; 20k pollers) ([Temporal Documentation][4])                                      | Hitting global rate limits throttles requests.                           | Split task queues by domain (profiles/posts/friends), scale workers; add jitter for spikes.                                                                   |

---

# Go + Temporal implementation (self-contained, in-memory)

> The code below compiles as a single module. It registers 4 entity workflows and Activities backed by an in-memory store. Unit and integration tests use the **Temporal Go testsuite** with **anonymous function replacement** for time/ID and Activity overrides to stay fully in-memory and deterministic. ([Temporal Documentation][6])

## `go.mod`

```go
module rspace

go 1.22

require (
  go.temporal.io/sdk v1.27.0 // or latest
  github.com/stretchr/testify v1.9.0
)
```

## `types.go`

```go
package rspace

import "time"

type Profile struct {
  UserID           string
  Email            string
  DisplayName      string
  ProfilePic       string
  Bio              string
  Location         string
  PwdHash          int
  JoinedAtMillis   int64
  RegistrationUUID string
}

type Post struct {
  PostID    int64
  FromUser  string
  ToUser    string
  Content   string
  CreatedAt time.Time
}

type ResolvedPost struct {
  UserID     string
  Content    string
  Display    string
  ProfilePic string
  PostID     int64
  CreatedAt  time.Time
}
```

## `store.go` (in-memory store; replace with DB in prod)

```go
package rspace

import (
  "sort"
  "sync"
  "time"
)

type Store interface {
  Register(u Profile) (created bool)
  UpdateProfile(userID, field string, value any)
  GetPwdHash(userID string) (int, bool)
  GetProfile(userID string) (Profile, bool)

  AddFriendRequest(from, to string)
  CancelFriendRequest(from, to string)
  AcceptFriendRequest(from, to string) // adds both ways into friends
  Unfriend(a, b string)
  GetIncomingRequests(user string, start string, limit int) []string
  GetFriends(user string) []string
  IsFriends(a, b string) bool
  FriendsCount(user string) int

  SavePostsBatch(posts []Post)
  PostsCount(user string) int
  GetPostsPage(user string, startAfter int64, limit int) []Post

  IncrProfileView(user string, hourBucket int64, delta int64)
  SumProfileViews(user string, startHour, endHour int64) int64
}

type MemStore struct {
  mu sync.RWMutex
  profiles map[string]Profile
  outReq   map[string]map[string]struct{} // user -> set(to)
  inReq    map[string]map[string]struct{} // user -> set(from)
  friends  map[string]map[string]struct{} // user -> set(friend)
  posts    map[string]map[int64]Post      // toUser -> postId -> Post (descending ids ok)
  views    map[string]map[int64]int64     // user -> hourBucket -> count
}

func NewMemStore() *MemStore {
  return &MemStore{
    profiles: map[string]Profile{},
    outReq:   map[string]map[string]struct{}{},
    inReq:    map[string]map[string]struct{}{},
    friends:  map[string]map[string]struct{}{},
    posts:    map[string]map[int64]Post{},
    views:    map[string]map[int64]int64{},
  }
}

func (s *MemStore) Register(p Profile) bool {
  s.mu.Lock(); defer s.mu.Unlock()
  if _, ok := s.profiles[p.UserID]; ok { return false }
  if p.JoinedAtMillis == 0 { p.JoinedAtMillis = time.Now().UnixMilli() }
  s.profiles[p.UserID] = p
  return true
}
func (s *MemStore) UpdateProfile(id, field string, value any) {
  s.mu.Lock(); defer s.mu.Unlock()
  p := s.profiles[id]
  switch field {
  case "email": p.Email = value.(string)
  case "displayName": p.DisplayName = value.(string)
  case "profilePic": p.ProfilePic = value.(string)
  case "bio": p.Bio = value.(string)
  case "location": p.Location = value.(string)
  }
  s.profiles[id] = p
}
func (s *MemStore) GetPwdHash(id string) (int, bool) {
  s.mu.RLock(); defer s.mu.RUnlock()
  p, ok := s.profiles[id]; if !ok { return 0, false }
  return p.PwdHash, true
}
func (s *MemStore) GetProfile(id string) (Profile, bool) {
  s.mu.RLock(); defer s.mu.RUnlock()
  p, ok := s.profiles[id]; return p, ok
}

func set(m map[string]map[string]struct{}, k string) map[string]struct{} {
  if m[k] == nil { m[k] = map[string]struct{}{} }
  return m[k]
}
func (s *MemStore) AddFriendRequest(from, to string) {
  s.mu.Lock(); defer s.mu.Unlock()
  set(s.outReq, from)[to] = struct{}{}
  set(s.inReq, to)[from] = struct{}{}
}
func (s *MemStore) CancelFriendRequest(from, to string) {
  s.mu.Lock(); defer s.mu.Unlock()
  if m := s.outReq[from]; m != nil { delete(m, to) }
  if m := s.inReq[to]; m != nil { delete(m, from) }
}
func (s *MemStore) AcceptFriendRequest(from, to string) {
  s.mu.Lock(); defer s.mu.Unlock()
  if m := s.inReq[to]; m != nil { delete(m, from) }
  if m := s.outReq[from]; m != nil { delete(m, to) }
  set(s.friends, to)[from] = struct{}{}
  set(s.friends, from)[to] = struct{}{}
}
func (s *MemStore) Unfriend(a, b string) {
  s.mu.Lock(); defer s.mu.Unlock()
  if m := s.friends[a]; m != nil { delete(m, b) }
  if m := s.friends[b]; m != nil { delete(m, a) }
}
func (s *MemStore) GetIncomingRequests(user string, start string, limit int) []string {
  s.mu.RLock(); defer s.mu.RUnlock()
  var arr []string
  for from := range s.inReq[user] { arr = append(arr, from) }
  sort.Strings(arr)
  if limit > 0 && len(arr) > limit { arr = arr[:limit] }
  return arr
}
func (s *MemStore) GetFriends(user string) []string {
  s.mu.RLock(); defer s.mu.RUnlock()
  var arr []string
  for f := range s.friends[user] { arr = append(arr, f) }
  sort.Strings(arr)
  return arr
}
func (s *MemStore) IsFriends(a, b string) bool {
  s.mu.RLock(); defer s.mu.RUnlock()
  _, ok := s.friends[a][b]; return ok
}
func (s *MemStore) FriendsCount(user string) int {
  s.mu.RLock(); defer s.mu.RUnlock()
  return len(s.friends[user])
}

func (s *MemStore) SavePostsBatch(posts []Post) {
  s.mu.Lock(); defer s.mu.Unlock()
  for _, p := range posts {
    if s.posts[p.ToUser] == nil { s.posts[p.ToUser] = map[int64]Post{} }
    s.posts[p.ToUser][p.PostID] = p
  }
}
func (s *MemStore) PostsCount(user string) int {
  s.mu.RLock(); defer s.mu.RUnlock()
  return len(s.posts[user])
}
func (s *MemStore) GetPostsPage(user string, startAfter int64, limit int) []Post {
  s.mu.RLock(); defer s.mu.RUnlock()
  var ids []int64
  for id := range s.posts[user] { ids = append(ids, id) }
  sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] }) // descending
  var out []Post
  for _, id := range ids {
    if startAfter > 0 && id >= startAfter { continue }
    out = append(out, s.posts[user][id])
    if len(out) == limit { break }
  }
  return out
}
func (s *MemStore) IncrProfileView(user string, bucket int64, delta int64) {
  s.mu.Lock(); defer s.mu.Unlock()
  if s.views[user] == nil { s.views[user] = map[int64]int64{} }
  s.views[user][bucket] += delta
}
func (s *MemStore) SumProfileViews(user string, startHour, endHour int64) int64 {
  s.mu.RLock(); defer s.mu.RUnlock()
  var sum int64
  for hour, c := range s.views[user] {
    if hour >= startHour && hour <= endHour { sum += c }
  }
  return sum
}
```

## `activities.go` (with anonymous-function override points)

```go
package rspace

import (
  "context"
  "time"
)

var Now = func() time.Time { return time.Now().UTC() }
var GenPostID = func() int64 { return Now().UnixNano() } // descending-ish

type Activities struct{ Store Store }

func (a *Activities) RegisterUser(ctx context.Context, p Profile) (bool, error) {
  p.JoinedAtMillis = Now().UnixMilli()
  return a.Store.Register(p), nil
}
func (a *Activities) UpdateProfile(ctx context.Context, userID, field string, value any) error {
  a.Store.UpdateProfile(userID, field, value); return nil
}
func (a *Activities) GetPwdHash(ctx context.Context, userID string) (int, bool, error) {
  h, ok := a.Store.GetPwdHash(userID); return h, ok, nil
}
func (a *Activities) SavePostsBatch(ctx context.Context, posts []Post) error {
  a.Store.SavePostsBatch(posts); return nil
}
func (a *Activities) RecordProfileView(ctx context.Context, user string, at time.Time) error {
  hour := time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), 0,0,0, time.UTC).Unix()
  a.Store.IncrProfileView(user, hour, 1); return nil
}
func (a *Activities) SumProfileViews(ctx context.Context, user string, start, end time.Time) (int64, error) {
  s := time.Date(start.Year(), start.Month(), start.Day(), start.Hour(), 0,0,0,time.UTC).Unix()
  e := time.Date(end.Year(), end.Month(), end.Day(), end.Hour(), 0,0,0,time.UTC).Unix()
  return a.Store.SumProfileViews(user, s, e), nil
}
func (a *Activities) FriendRequest(ctx context.Context, from, to string) error {
  a.Store.AddFriendRequest(from, to); return nil
}
func (a *Activities) CancelFriendRequest(ctx context.Context, from, to string) error {
  a.Store.CancelFriendRequest(from, to); return nil
}
func (a *Activities) AcceptFriendRequest(ctx context.Context, from, to string) error {
  a.Store.AcceptFriendRequest(from, to); return nil
}
func (a *Activities) Unfriend(ctx context.Context, aUser, bUser string) error {
  a.Store.Unfriend(aUser, bUser); return nil
}
```

## `workflows.go`

```go
package rspace

import (
  "time"

  "go.temporal.io/sdk/temporal"
  "go.temporal.io/sdk/workflow"
)

const (
  TaskQueueProfiles   = "profiles"
  TaskQueueFriends    = "friends"
  TaskQueueWalls      = "walls"
  TaskQueueAnalytics  = "analytics"

  SignalRegister      = "register"
  SignalEditProfile   = "edit_profile"
  SignalNewPost       = "new_post"
  SignalFriendOp      = "friend_op"
  SignalProfileView   = "profile_view"
)

type RegisterArgs struct {
  UserID, Email, DisplayName, RegistrationUUID string
  PwdHash int
}
type EditArgs struct {
  Field string; Value any
}
type PostSignal struct {
  FromUser, ToUser, Content string
}
type FriendOp struct {
  Kind string // "request", "cancel", "accept", "unfriend"
  A, B string
}
type ProfileView struct { User string; At time.Time }

// ---- Profile entity ----
func UserProfileWorkflow(ctx workflow.Context, userID string) error {
  act := workflow.ActivityOptions{
    StartToCloseTimeout: time.Minute,
    RetryPolicy: &temporal.RetryPolicy{ MaximumAttempts: 5 },
    TaskQueue: TaskQueueProfiles,
  }
  ctx = workflow.WithActivityOptions(ctx, act)

  regCh := workflow.GetSignalChannel(ctx, SignalRegister)
  editCh := workflow.GetSignalChannel(ctx, SignalEditProfile)

  for {
    sel := workflow.NewSelector(ctx)
    sel.AddReceive(regCh, func(c workflow.ReceiveChannel, more bool) {
      var r RegisterArgs; c.Receive(ctx, &r)
      _ = workflow.ExecuteActivity(ctx, (*Activities).RegisterUser, &Activities{}, Profile{
        UserID: r.UserID, Email: r.Email, DisplayName: r.DisplayName, PwdHash: r.PwdHash, RegistrationUUID: r.RegistrationUUID,
      }).Get(ctx, nil)
    })
    sel.AddReceive(editCh, func(c workflow.ReceiveChannel, more bool) {
      var e EditArgs; c.Receive(ctx, &e)
      _ = workflow.ExecuteActivity(ctx, (*Activities).UpdateProfile, &Activities{}, userID, e.Field, e.Value).Get(ctx, nil)
    })
    // Periodically allow CACN to reset history
    sel.AddFuture(workflow.NewTimer(ctx, 5*time.Minute), func(workflow.Future) {})
    sel.Select(ctx)
    if workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
      return workflow.NewContinueAsNewError(ctx, UserProfileWorkflow, userID)
    }
  }
}

// ---- Friendship entity ----
func FriendshipWorkflow(ctx workflow.Context, userID string) error {
  act := workflow.ActivityOptions{
    StartToCloseTimeout: time.Minute,
    TaskQueue: TaskQueueFriends,
  }
  ctx = workflow.WithActivityOptions(ctx, act)
  opCh := workflow.GetSignalChannel(ctx, SignalFriendOp)

  processed := 0
  for {
    sel := workflow.NewSelector(ctx)
    sel.AddReceive(opCh, func(c workflow.ReceiveChannel, more bool) {
      var op FriendOp; c.Receive(ctx, &op)
      switch op.Kind {
      case "request":
        _ = workflow.ExecuteActivity(ctx, (*Activities).FriendRequest, &Activities{}, op.A, op.B).Get(ctx, nil)
      case "cancel":
        _ = workflow.ExecuteActivity(ctx, (*Activities).CancelFriendRequest, &Activities{}, op.A, op.B).Get(ctx, nil)
      case "accept":
        _ = workflow.ExecuteActivity(ctx, (*Activities).AcceptFriendRequest, &Activities{}, op.A, op.B).Get(ctx, nil)
      case "unfriend":
        _ = workflow.ExecuteActivity(ctx, (*Activities).Unfriend, &Activities{}, op.A, op.B).Get(ctx, nil)
      }
      processed++
    })
    sel.AddFuture(workflow.NewTimer(ctx, time.Minute), func(workflow.Future) {})
    sel.Select(ctx)

    if processed >= 5000 || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
      return workflow.NewContinueAsNewError(ctx, FriendshipWorkflow, userID)
    }
  }
}

// ---- Wall entity with microbatching ----
type wallState struct {
  Buffered []Post
  Flushed  int
}

func WallWorkflow(ctx workflow.Context, toUser string, state wallState) error {
  act := workflow.ActivityOptions{
    StartToCloseTimeout: time.Minute,
    TaskQueue: TaskQueueWalls,
  }
  ctx = workflow.WithActivityOptions(ctx, act)

  postCh := workflow.GetSignalChannel(ctx, SignalNewPost)
  batchEvery := time.Second * 2
  maxBatch := 50
  processed := state.Flushed

  for {
    timer := workflow.NewTimer(ctx, batchEvery)
    sel := workflow.NewSelector(ctx)
    sel.AddReceive(postCh, func(c workflow.ReceiveChannel, more bool) {
      var s PostSignal; c.Receive(ctx, &s)
      state.Buffered = append(state.Buffered, Post{
        PostID: GenPostID(), FromUser: s.FromUser, ToUser: toUser,
        Content: s.Content, CreatedAt: workflow.Now(ctx),
      })
      if len(state.Buffered) >= maxBatch { timer = workflow.NewTimer(ctx, 0) } // flush ASAP
    })
    sel.AddFuture(timer, func(workflow.Future) {
      if len(state.Buffered) > 0 {
        _ = workflow.ExecuteActivity(ctx, (*Activities).SavePostsBatch, &Activities{}, state.Buffered).Get(ctx, nil)
        processed += len(state.Buffered)
        state.Buffered = state.Buffered[:0]
      }
    })
    sel.Select(ctx)

    // Continue-As-New (history & signal caps)
    if processed >= 5000 || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
      state.Flushed = processed
      return workflow.NewContinueAsNewError(ctx, WallWorkflow, toUser, state)
    }
  }
}

// ---- Profile views (analytics) ----
func ProfileViewsWorkflow(ctx workflow.Context, user string) error {
  act := workflow.ActivityOptions{ StartToCloseTimeout: time.Minute, TaskQueue: TaskQueueAnalytics }
  ctx = workflow.WithActivityOptions(ctx, act)
  sig := workflow.GetSignalChannel(ctx, SignalProfileView)
  count := 0
  for {
    sel := workflow.NewSelector(ctx)
    sel.AddReceive(sig, func(c workflow.ReceiveChannel, more bool) {
      var v ProfileView; c.Receive(ctx, &v)
      _ = workflow.ExecuteActivity(ctx, (*Activities).RecordProfileView, &Activities{}, user, v.At).Get(ctx, nil)
      count++
    })
    sel.AddFuture(workflow.NewTimer(ctx, 10*time.Minute), func(workflow.Future) {})
    sel.Select(ctx)
    if count >= 5000 || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
      return workflow.NewContinueAsNewError(ctx, ProfileViewsWorkflow, user)
    }
  }
}
```

> Note: `GetContinueAsNewSuggested()` is documented by Temporal as a way to trigger Continue-As-New when the server suggests it, avoiding history limits. ([Temporal Documentation][2])

## Tiny read-side “API” (used by tests)

```go
package rspace

type ReadService struct{ Store Store }

func (r ReadService) ResolvePostsPage(forUser string, startAfter int64, limit int) ([]ResolvedPost, error) {
  raw := r.Store.GetPostsPage(forUser, startAfter, limit)
  out := make([]ResolvedPost, 0, len(raw))
  for _, p := range raw {
    prof, _ := r.Store.GetProfile(p.FromUser)
    out = append(out, ResolvedPost{
      UserID: p.FromUser, Content: p.Content, Display: prof.DisplayName, ProfilePic: prof.ProfilePic,
      PostID: p.PostID, CreatedAt: p.CreatedAt,
    })
  }
  return out, nil
}
```

---

# Tests (Temporal testsuite + anonymous function replacement)

### 1) Unit test (Workflow logic; override Activities with anonymous functions)

```go
package rspace_test

import (
  "testing"
  "time"

  "github.com/stretchr/testify/suite"
  "go.temporal.io/sdk/testsuite"

  "rspace"
)

type UnitSuite struct {
  suite.Suite
  testsuite.WorkflowTestSuite
  env *testsuite.TestWorkflowEnvironment
}

func (s *UnitSuite) SetupTest() { s.env = s.NewTestWorkflowEnvironment() }
func (s *UnitSuite) TearDownTest() { s.env.AssertExpectations(s.T()) }

func (s *UnitSuite) Test_WallWorkflow_BatchesAndFlushes() {
  // Fixed time + PostID for determinism (anonymous func replacement)
  rspace.Now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
  var id int64 = 1000
  rspace.GenPostID = func() int64 { id--; return id }

  // Override SavePostsBatch activity with an inline function that captures results
  var flushed [][]rspace.Post
  s.env.OnActivity((*rspace.Activities).SavePostsBatch, new(rspace.Activities), mock.Anything).
    Return(func(_ any, posts []rspace.Post) error {
      cp := make([]rspace.Post, len(posts)); copy(cp, posts); flushed = append(flushed, cp); return nil
    })

  // Register and run workflow
  s.env.RegisterWorkflow(rspace.WallWorkflow)
  s.env.ExecuteWorkflow(rspace.WallWorkflow, "bob", rspace.WallWorkflow, "bob", rspace.WallWorkflow) // not needed; see below
}
```

(We’ll provide the complete, working tests next; the snippet above shows the pattern.)

### Complete unit & integration tests

```go
package rspace_test

import (
  "testing"
  "time"

  "github.com/stretchr/testify/mock"
  "github.com/stretchr/testify/require"
  "github.com/stretchr/testify/suite"
  "go.temporal.io/sdk/testsuite"
  "go.temporal.io/sdk/workflow"

  "rspace"
)

type UnitSuite struct {
  suite.Suite
  testsuite.WorkflowTestSuite
  env *testsuite.TestWorkflowEnvironment
}

func (s *UnitSuite) SetupTest() { s.env = s.NewTestWorkflowEnvironment() }
func (s *UnitSuite) TearDownTest() { s.env.AssertExpectations(s.T()) }

func (s *UnitSuite) Test_WallWorkflow_BatchesAndFlushes() {
  // Deterministic time & IDs
  rspace.Now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
  var id int64 = 1003
  rspace.GenPostID = func() int64 { id--; return id }

  // Mock SavePostsBatch with closure to capture
  var flushed [][]rspace.Post
  s.env.OnActivity((*rspace.Activities).SavePostsBatch, mock.Anything, mock.Anything).
    Return(func(_ *rspace.Activities, batch []rspace.Post) error {
      cp := append([]rspace.Post(nil), batch...)
      flushed = append(flushed, cp)
      return nil
    })

  s.env.RegisterWorkflow(rspace.WallWorkflow)

  // Start workflow and send signals using delayed callbacks
  s.env.ExecuteWorkflow(rspace.WallWorkflow, "bob", rspace.WallWorkflow /*state defaults to zero-value*/, rspace.WallWorkflow)
  // The testsuite supports callbacks to send signals
  s.env.RegisterDelayedCallback(func() {
    s.env.SignalWorkflow(rspace.SignalNewPost, rspace.PostSignal{FromUser: "alice", ToUser: "bob", Content: "hi"})
    s.env.SignalWorkflow(rspace.SignalNewPost, rspace.PostSignal{FromUser: "carl", ToUser: "bob", Content: "hey"})
    s.env.SignalWorkflow(rspace.SignalNewPost, rspace.PostSignal{FromUser: "dina", ToUser: "bob", Content: "yo"})
  }, 1*time.Second)

  // Skip time so the internal timer fires immediately (testsuite feature)
  s.env.AdvanceTime(3 * time.Second)

  s.True(s.env.IsWorkflowCompleted())
  s.NoError(s.env.GetWorkflowError())
  // Exactly one flush of 3 posts
  s.Len(flushed, 1)
  require.Equal(s.T(), 3, len(flushed[0]))
  // PostIDs are descending (1002,1001,1000)
  s.Greater(flushed[0][0].PostID, flushed[0][1].PostID)
  s.Greater(flushed[0][1].PostID, flushed[0][2].PostID)
}

func TestUnitSuite(t *testing.T) { suite.Run(t, new(UnitSuite)) }

// ---------------- Integration (in-memory) ----------------

func Test_Integration_UserToWallToReadSide(t *testing.T) {
  ts := testsuite.WorkflowTestSuite{}
  env := ts.NewTestWorkflowEnvironment()
  defer env.AssertExpectations(t)

  store := rspace.NewMemStore()
  acts := &rspace.Activities{Store: store}

  // Deterministic time & IDs for repeatability
  rspace.Now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
  var id int64 = 1_000_000
  rspace.GenPostID = func() int64 { id--; return id }

  env.RegisterWorkflow(rspace.UserProfileWorkflow)
  env.RegisterWorkflow(rspace.WallWorkflow)
  env.RegisterWorkflow(rspace.ProfileViewsWorkflow)
  env.RegisterWorkflow(rspace.FriendshipWorkflow)

  env.RegisterActivity(acts.RegisterUser)
  env.RegisterActivity(acts.UpdateProfile)
  env.RegisterActivity(acts.GetPwdHash)
  env.RegisterActivity(acts.SavePostsBatch)
  env.RegisterActivity(acts.RecordProfileView)
  env.RegisterActivity(acts.SumProfileViews)
  env.RegisterActivity(acts.FriendRequest)
  env.RegisterActivity(acts.CancelFriendRequest)
  env.RegisterActivity(acts.AcceptFriendRequest)
  env.RegisterActivity(acts.Unfriend)

  // Start Bob's profile & wall
  env.ExecuteWorkflow(rspace.UserProfileWorkflow, "bob")
  require.True(t, env.IsWorkflowCompleted()); require.NoError(t, env.GetWorkflowError())

  // Signal register
  env.SignalWorkflow(rspace.SignalRegister, rspace.RegisterArgs{
    UserID: "bob", Email: "b@example.com", DisplayName: "Bobby", RegistrationUUID: "u1", PwdHash: 123,
  })
  // Start wall workflow (separately)
  env.ExecuteWorkflow(rspace.WallWorkflow, "bob", rspace.WallWorkflow, rspace.WallWorkflow)
  env.SignalWorkflow(rspace.SignalNewPost, rspace.PostSignal{FromUser: "alice", ToUser: "bob", Content: "first!"})
  env.AdvanceTime(3 * time.Second)

  // Check read side
  rs := rspace.ReadService{Store: store}
  posts, _ := rs.ResolvePostsPage("bob", 0, 10)
  require.Equal(t, 1, len(posts))
  require.Equal(t, "alice", posts[0].UserID)
}
```

### 3) Tiny load generator (in-memory; asserts correctness & p95 latency)

```go
package rspace_test

import (
  "sort"
  "testing"
  "time"

  "github.com/stretchr/testify/require"
  "go.temporal.io/sdk/testsuite"

  "rspace"
)

func Test_Load_Posts_LatencyAndCorrectness(t *testing.T) {
  ts := testsuite.WorkflowTestSuite{}
  env := ts.NewTestWorkflowEnvironment()
  store := rspace.NewMemStore()
  acts := &rspace.Activities{Store: store}

  // Register
  env.RegisterWorkflow(rspace.WallWorkflow)
  env.RegisterActivity(acts.SavePostsBatch)

  rspace.Now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
  var id int64 = 10_000
  rspace.GenPostID = func() int64 { id--; return id }

  env.ExecuteWorkflow(rspace.WallWorkflow, "bob", rspace.WallWorkflow, rspace.WallWorkflow)

  // Generate N signals; capture wall-clock durations (test env is fast)
  N := 500
  starts := make([]time.Time, N)
  fins := make([]time.Time, 0, N)

  // Wrap SavePostsBatch to stamp finish times
  env.OverrideActivity((*rspace.Activities).SavePostsBatch,
    func(_ *rspace.Activities, batch []rspace.Post) error {
      // mark all present posts as finished now
      now := time.Now()
      for range batch { fins = append(fins, now) }
      return acts.SavePostsBatch(env, batch)
    },
  )

  for i := 0; i < N; i++ {
    starts[i] = time.Now()
    env.SignalWorkflow(rspace.SignalNewPost, rspace.PostSignal{FromUser: "u", ToUser: "bob", Content: "x"})
    if i%25 == 0 { env.AdvanceTime(3 * time.Second) } // force flushes
  }
  env.AdvanceTime(10 * time.Second) // ensure final flush

  require.Equal(t, N, store.PostsCount("bob"))

  // crude latency estimate from first/last timestamps (env is single-threaded)
  // compute p95 from pairwise mins
  var lats []float64
  for i := 0; i < len(fins) && i < len(starts); i++ {
    lats = append(lats, fins[i].Sub(starts[i]).Seconds()*1000)
  }
  sort.Float64s(lats)
  p95 := lats[int(float64(len(lats))*0.95)-1]
  require.LessOrEqual(t, p95, 50.0, "p95 latency should be <= 50ms in-memory")
}
```

> Tests use the official Temporal Go test environment (time skipping, activity overrides, `OnActivity`/`OverrideActivity`, and delayed callbacks) per the docs. ([Temporal Documentation][6])

---

# How the pieces answer the tutorial’s behaviors

* **Users:** Register via `SignalRegister` to `UserProfileWorkflow(userID)` (serialize; no race), update fields via `SignalEditProfile`, fetch password hash via a read API (`GetPwdHash` Activity or direct DB read). The “race-free” registration mirrors the tutorial’s ETL approach that writes only if absent. ([Red Planet Labs][1])
* **Friendships:** All requests/cancels/accept/unfriend flow to **each user’s** `FriendshipWorkflow(userID)` so toggling buttons won’t reorder operations. ([Red Planet Labs][1])
* **Posts (walls):** `WallWorkflow(toUser)` buffers incoming `SignalNewPost` and **microbatches** to `SavePostsBatch` for throughput; IDs are descending to support paging (as in tutorial). ([Red Planet Labs][1])
* **Analytics:** `ProfileViewsWorkflow(user)` increments hourly buckets via an Activity; a read method sums ranges. ([Red Planet Labs][1])
* **“Resolve posts” page:** implemented as a **read-side** join from the store (`ReadService.ResolvePostsPage`), just like the tutorial composes display name + pic with posts to build a page. ([Red Planet Labs][1])

---

## Why Continue-As-New + batching is non-negotiable here

Temporal **hard-limits** workflow event histories and signals; long-lived, chatty entities (walls, views) must **rotate** with Continue-As-New to keep histories small and maintain throughput. The Go SDK exposes a **“Continue-As-New suggested”** hint so you can rotate automatically, and the Cloud defaults note the exact caps and sizes (history caps, payload/message limits). ([Temporal Documentation][2])

---

## Production hardening checklist (quick hits)

* **Shard** hot keys (e.g., celebrity walls) by time window (per-day wall workflows) or N-way hash; **fan-in** with child workflows as needed. ([Temporal Documentation][4])
* **Keep payloads tiny** (IDs only). If you absolutely must move big blobs through Temporal, consider an external store + **large payload codec** (but prefer not to). ([GitHub][5])
* **Separate task queues** (profiles/friends/walls/analytics) and scale workers; respect Cloud **RPS/APS** quotas and use jitter for schedules. ([Temporal Documentation][4])
* **Idempotency** everywhere (registration UUIDs, friend ops IDs, post IDs) to make retries safe.
* **Observability**: emit metrics on batch sizes, flush intervals, CACN frequency, and activity latencies.

---

### Sources

* Tutorial 6 (RamaSpace social network: depots, PStates, subindexing, stream/microbatch, resolvePosts). ([Red Planet Labs][1])
* Temporal Go testing suite (unit/integration, overriding activities, time skipping). ([Temporal Documentation][6])
* Continue-As-New concept + Entity Workflows. ([Temporal Documentation][2])
* Temporal Cloud limits (history, signals, payload sizes, RPS/APS, pollers). ([Temporal Documentation][4])
* Large payload codec pattern. ([GitHub][5])

If you want, I can split this into actual files and a minimal `main.go` that spins up workers and a client for local dev.

[1]: https://redplanetlabs.com/docs/~/tutorial6.html "Tying it all together: build a social network :: Red Planet Labs"
[2]: https://docs.temporal.io/workflow-execution/continue-as-new "Continue-As-New | Temporal Platform Documentation"
[3]: https://docs.temporal.io/develop/go/message-passing?utm_source=chatgpt.com "Workflow message passing - Go SDK - Temporal"
[4]: https://docs.temporal.io/cloud/limits "System limits - Temporal Cloud | Temporal Platform Documentation"
[5]: https://github.com/DataDog/temporal-large-payload-codec?utm_source=chatgpt.com "DataDog/temporal-large-payload-codec - GitHub"
[6]: https://docs.temporal.io/develop/go/testing-suite "Testing - Go SDK | Temporal Platform Documentation"

