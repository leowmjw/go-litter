package ramaspace

import (
	"context"
	"fmt"
	"testing"
	"time"

	"app/internal/storage"
)

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func newTestModule(t *testing.T, store *storage.Store, taskCount uint32) *Module {
	t.Helper()
	module, err := New(store, taskCount, SchemaVersion1, fixedClock(time.UnixMilli(1_700_000_000_000)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return module
}

func TestRegisterUserFirstWinsDuplicateFails(t *testing.T) {
	ctx := context.Background()
	module := newTestModule(t, storage.NewMemory(), 4)

	ok, err := module.RegisterUser(ctx, UserRegistration{UserID: "alice", Email: "alice@example.com", DisplayName: "Alice", PasswordHash: "hash1", RegistrationUUID: "uuid-1"})
	if err != nil || !ok {
		t.Fatalf("first registration = %v, %v", ok, err)
	}

	// A retry of the same client attempt (same registration UUID) is idempotent.
	ok, err = module.RegisterUser(ctx, UserRegistration{UserID: "alice", Email: "alice@example.com", DisplayName: "Alice", PasswordHash: "hash1", RegistrationUUID: "uuid-1"})
	if err != nil || !ok {
		t.Fatalf("idempotent retry = %v, %v", ok, err)
	}

	// A different client racing for the same user ID loses.
	ok, err = module.RegisterUser(ctx, UserRegistration{UserID: "alice", Email: "other@example.com", DisplayName: "Someone Else", PasswordHash: "hash2", RegistrationUUID: "uuid-2"})
	if err != nil {
		t.Fatalf("losing registration error: %v", err)
	}
	if ok {
		t.Fatal("losing registration should return false")
	}

	profile, found, err := module.GetProfile(ctx, "alice")
	if err != nil || !found {
		t.Fatalf("GetProfile: %v, %v", found, err)
	}
	if profile.DisplayName != "Alice" {
		t.Fatalf("winner's profile was overwritten: %+v", profile)
	}
}

func TestConcurrentDuplicateRegistrationSingleWinner(t *testing.T) {
	ctx := context.Background()
	module := newTestModule(t, storage.NewMemory(), 4)

	const attempts = 8
	results := make([]bool, attempts)
	errs := make([]error, attempts)
	done := make(chan int, attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			ok, err := module.RegisterUser(ctx, UserRegistration{
				UserID: "bob", Email: "bob@example.com", DisplayName: "Bob", PasswordHash: "hash",
				RegistrationUUID: fmt.Sprintf("uuid-%d", i),
			})
			results[i] = ok
			errs[i] = err
			done <- i
		}(i)
	}
	for i := 0; i < attempts; i++ {
		<-done
	}
	winners := 0
	for i := 0; i < attempts; i++ {
		if errs[i] != nil {
			t.Fatalf("attempt %d error: %v", i, errs[i])
		}
		if results[i] {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want 1", winners)
	}
}

func TestEditProfileVisibleAfterAcknowledgedAppend(t *testing.T) {
	ctx := context.Background()
	module := newTestModule(t, storage.NewMemory(), 4)

	if _, err := module.RegisterUser(ctx, UserRegistration{UserID: "carol", Email: "carol@example.com", DisplayName: "Carol", PasswordHash: "hash", RegistrationUUID: "uuid-1"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if hash, found, err := module.GetPasswordHash(ctx, "carol"); err != nil || !found || hash != "hash" {
		t.Fatalf("GetPasswordHash = %q, %v, %v", hash, found, err)
	}

	if err := module.EditProfile(ctx, ProfileEdit{UserID: "carol", Field: FieldBio, Value: "hello world"}); err != nil {
		t.Fatalf("edit bio: %v", err)
	}
	if err := module.EditProfile(ctx, ProfileEdit{UserID: "carol", Field: FieldDisplayName, Value: "Carol Danvers"}); err != nil {
		t.Fatalf("edit display name: %v", err)
	}

	profile, found, err := module.GetProfile(ctx, "carol")
	if err != nil || !found {
		t.Fatalf("GetProfile: %v, %v", found, err)
	}
	if profile.Bio != "hello world" || profile.DisplayName != "Carol Danvers" {
		t.Fatalf("profile = %+v", profile)
	}
	if profile.Location != "" {
		t.Fatalf("unrelated field was touched: %+v", profile)
	}

	if err := module.EditProfile(ctx, ProfileEdit{UserID: "carol", Field: "bogus", Value: "x"}); err == nil {
		t.Fatal("expected unknown field error")
	}
	if err := module.EditProfile(ctx, ProfileEdit{UserID: "missing", Field: FieldBio, Value: "x"}); err == nil {
		t.Fatal("expected user not found error")
	}
}

func TestFriendshipLifecycleBidirectionalInvariants(t *testing.T) {
	ctx := context.Background()
	module := newTestModule(t, storage.NewMemory(), 4)

	for _, id := range []string{"alice", "bob"} {
		if _, err := module.RegisterUser(ctx, UserRegistration{UserID: id, Email: id + "@example.com", DisplayName: id, PasswordHash: "hash", RegistrationUUID: "uuid-" + id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}

	if err := module.FriendRequest(ctx, "alice", "bob"); err != nil {
		t.Fatalf("friend request: %v", err)
	}
	outgoing, err := module.ListOutgoingRequests(ctx, "alice", "", 20)
	if err != nil || len(outgoing.UserIDs) != 1 || outgoing.UserIDs[0] != "bob" {
		t.Fatalf("outgoing = %+v, %v", outgoing, err)
	}
	incoming, err := module.ListIncomingRequests(ctx, "bob", "", 20)
	if err != nil || len(incoming.UserIDs) != 1 || incoming.UserIDs[0] != "alice" {
		t.Fatalf("incoming = %+v, %v", incoming, err)
	}

	// Cancel and re-request to prove cancellation clears both directions.
	if err := module.CancelFriendRequest(ctx, "alice", "bob"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if outgoing, err := module.ListOutgoingRequests(ctx, "alice", "", 20); err != nil || len(outgoing.UserIDs) != 0 {
		t.Fatalf("outgoing after cancel = %+v, %v", outgoing, err)
	}
	if incoming, err := module.ListIncomingRequests(ctx, "bob", "", 20); err != nil || len(incoming.UserIDs) != 0 {
		t.Fatalf("incoming after cancel = %+v, %v", incoming, err)
	}
	if err := module.FriendRequest(ctx, "alice", "bob"); err != nil {
		t.Fatalf("re-request: %v", err)
	}

	if err := module.AcceptFriendRequest(ctx, "bob", "alice"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if outgoing, err := module.ListOutgoingRequests(ctx, "alice", "", 20); err != nil || len(outgoing.UserIDs) != 0 {
		t.Fatalf("outgoing after accept = %+v, %v", outgoing, err)
	}
	if incoming, err := module.ListIncomingRequests(ctx, "bob", "", 20); err != nil || len(incoming.UserIDs) != 0 {
		t.Fatalf("incoming after accept = %+v, %v", incoming, err)
	}
	isFriend, err := module.IsFriend(ctx, "alice", "bob")
	if err != nil || !isFriend {
		t.Fatalf("IsFriend alice/bob = %v, %v", isFriend, err)
	}
	isFriend, err = module.IsFriend(ctx, "bob", "alice")
	if err != nil || !isFriend {
		t.Fatalf("IsFriend bob/alice = %v, %v", isFriend, err)
	}
	for _, id := range []string{"alice", "bob"} {
		count, err := module.FriendCount(ctx, id)
		if err != nil || count != 1 {
			t.Fatalf("FriendCount(%s) = %d, %v", id, count, err)
		}
	}
	friends, err := module.ListFriends(ctx, "alice", "", 20)
	if err != nil || len(friends.UserIDs) != 1 || friends.UserIDs[0] != "bob" {
		t.Fatalf("friends = %+v, %v", friends, err)
	}

	// Accepting again must not double-count (idempotent add).
	if err := module.AcceptFriendRequest(ctx, "bob", "alice"); err != nil {
		t.Fatalf("re-accept: %v", err)
	}
	if count, err := module.FriendCount(ctx, "alice"); err != nil || count != 1 {
		t.Fatalf("FriendCount after re-accept = %d, %v", count, err)
	}

	if err := module.RemoveFriendship(ctx, "alice", "bob"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	isFriend, err = module.IsFriend(ctx, "alice", "bob")
	if err != nil || isFriend {
		t.Fatalf("IsFriend after remove = %v, %v", isFriend, err)
	}
	isFriend, err = module.IsFriend(ctx, "bob", "alice")
	if err != nil || isFriend {
		t.Fatalf("IsFriend reverse after remove = %v, %v", isFriend, err)
	}
	for _, id := range []string{"alice", "bob"} {
		count, err := module.FriendCount(ctx, id)
		if err != nil || count != 0 {
			t.Fatalf("FriendCount(%s) after remove = %d, %v", id, count, err)
		}
	}

	// Removing again must not underflow below zero.
	if err := module.RemoveFriendship(ctx, "alice", "bob"); err != nil {
		t.Fatalf("re-remove: %v", err)
	}
	if count, err := module.FriendCount(ctx, "alice"); err != nil || count != 0 {
		t.Fatalf("FriendCount after re-remove = %d, %v", count, err)
	}
}

func TestFriendRequestPaginationOrderedPages(t *testing.T) {
	ctx := context.Background()
	module := newTestModule(t, storage.NewMemory(), 4)
	if _, err := module.RegisterUser(ctx, UserRegistration{UserID: "hub", Email: "hub@example.com", DisplayName: "Hub", PasswordHash: "hash", RegistrationUUID: "uuid-hub"}); err != nil {
		t.Fatalf("register hub: %v", err)
	}
	for i := 0; i < 24; i++ {
		id := fmt.Sprintf("user-%02d", i)
		if _, err := module.RegisterUser(ctx, UserRegistration{UserID: id, Email: id + "@example.com", DisplayName: id, PasswordHash: "hash", RegistrationUUID: "uuid-" + id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		if err := module.FriendRequest(ctx, id, "hub"); err != nil {
			t.Fatalf("request from %s: %v", id, err)
		}
	}
	page1, err := module.ListIncomingRequests(ctx, "hub", "", 20)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.UserIDs) != 20 || !page1.HasMore {
		t.Fatalf("page1 = %+v", page1)
	}
	page2, err := module.ListIncomingRequests(ctx, "hub", page1.NextCursor, 20)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.UserIDs) != 4 || page2.HasMore {
		t.Fatalf("page2 = %+v", page2)
	}
	seen := make(map[string]bool)
	for _, id := range append(append([]string{}, page1.UserIDs...), page2.UserIDs...) {
		if seen[id] {
			t.Fatalf("duplicate id %s across pages", id)
		}
		seen[id] = true
	}
	if len(seen) != 24 {
		t.Fatalf("total distinct ids = %d, want 24", len(seen))
	}
}

func TestPostsPaginationAndResolution(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	module := newTestModule(t, store, 4)

	if _, err := module.RegisterUser(ctx, UserRegistration{UserID: "wall-owner", Email: "wo@example.com", DisplayName: "Wall Owner", PasswordHash: "hash", RegistrationUUID: "uuid-wo"}); err != nil {
		t.Fatalf("register wall owner: %v", err)
	}
	if _, err := module.RegisterUser(ctx, UserRegistration{UserID: "author", Email: "a@example.com", DisplayName: "Author Name", PasswordHash: "hash", RegistrationUUID: "uuid-author"}); err != nil {
		t.Fatalf("register author: %v", err)
	}
	if err := module.EditProfile(ctx, ProfileEdit{UserID: "author", Field: FieldProfilePicture, Value: "pic.png"}); err != nil {
		t.Fatalf("edit picture: %v", err)
	}

	for i := 0; i < 24; i++ {
		if err := module.Post(ctx, Post{Author: "author", Destination: "wall-owner", Content: fmt.Sprintf("post %d", i)}); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
	}
	if _, err := module.Advance(ctx); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if count, err := module.PostCount(ctx, "wall-owner"); err != nil || count != 24 {
		t.Fatalf("PostCount = %d, %v", count, err)
	}

	page1, err := module.ResolvePosts(ctx, "wall-owner", 0, 20)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Posts) != 20 || !page1.HasMore {
		t.Fatalf("page1 = %+v", page1)
	}
	if page1.Posts[0].Content != "post 23" {
		t.Fatalf("page1 not reverse chronological: first = %q", page1.Posts[0].Content)
	}
	for _, post := range page1.Posts {
		if post.AuthorDisplayName != "Author Name" || post.AuthorProfilePicture != "pic.png" {
			t.Fatalf("post not resolved: %+v", post)
		}
	}
	page2, err := module.ResolvePosts(ctx, "wall-owner", page1.NextCursor, 20)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Posts) != 4 || page2.HasMore {
		t.Fatalf("page2 = %+v", page2)
	}
	if page2.Posts[len(page2.Posts)-1].Content != "post 0" {
		t.Fatalf("page2 tail = %+v", page2.Posts[len(page2.Posts)-1])
	}
}

func TestProfileViewHourRangeSums(t *testing.T) {
	ctx := context.Background()
	module := newTestModule(t, storage.NewMemory(), 2)

	hour := int64(time.Hour / time.Millisecond)
	base := int64(1_700_000_000_000)
	views := []int64{base, base + 5, base + hour, base + hour + 5, base + 2*hour, base + 100*hour}
	for _, ts := range views {
		if err := module.RecordProfileView(ctx, ProfileView{UserID: "star", TimestampMillis: ts}); err != nil {
			t.Fatalf("record view: %v", err)
		}
	}
	if _, err := module.Advance(ctx); err != nil {
		t.Fatalf("advance: %v", err)
	}

	fromHour := base / hour
	total, err := module.ProfileViewCount(ctx, "star", fromHour, fromHour+3)
	if err != nil {
		t.Fatalf("ProfileViewCount: %v", err)
	}
	// Buckets fromHour (2 views), fromHour+1 (2 views), fromHour+2 (1 view) fall
	// in [fromHour, fromHour+3); the fromHour+100 view falls outside.
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	full, err := module.ProfileViewCount(ctx, "star", fromHour, fromHour+200)
	if err != nil || full != 6 {
		t.Fatalf("full total = %d, %v", full, err)
	}
}

func TestProfileViewsExactlyOnceAcrossReplayedBatch(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	module := newTestModule(t, store, 2)
	if err := module.RecordProfileView(ctx, ProfileView{UserID: "star", TimestampMillis: 1_700_000_000_000}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if n, err := module.Advance(ctx); err != nil || n != 1 {
		t.Fatalf("advance = %d, %v", n, err)
	}
	// A second advance call must be a no-op: no unprocessed items remain, so the
	// batch effect must not be applied twice.
	if n, err := module.Advance(ctx); err != nil || n != 0 {
		t.Fatalf("second advance = %d, %v", n, err)
	}
	hour := int64(time.Hour / time.Millisecond)
	fromHour := int64(1_700_000_000_000) / hour
	total, err := module.ProfileViewCount(ctx, "star", fromHour, fromHour+1)
	if err != nil || total != 1 {
		t.Fatalf("total = %d, %v", total, err)
	}
}

func TestMigrateProfilesIdempotentAndCoexistence(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()

	v1, err := New(store, 4, SchemaVersion1, fixedClock(time.UnixMilli(1_700_000_000_000)))
	if err != nil {
		t.Fatalf("new v1: %v", err)
	}
	if _, err := v1.RegisterUser(ctx, UserRegistration{UserID: "legacy", Email: "l@example.com", DisplayName: "Legacy", PasswordHash: "hash", RegistrationUUID: "uuid-legacy"}); err != nil {
		t.Fatalf("register legacy: %v", err)
	}
	profile, found, err := v1.GetProfile(ctx, "legacy")
	if err != nil || !found || profile.Pronouns != "" {
		t.Fatalf("v1 profile = %+v, %v, %v", profile, found, err)
	}

	v2, err := New(store, 4, SchemaVersion2, fixedClock(time.UnixMilli(1_700_000_100_000)))
	if err != nil {
		t.Fatalf("new v2: %v", err)
	}
	// New registrations under v2 get the new field directly.
	if _, err := v2.RegisterUser(ctx, UserRegistration{UserID: "modern", Email: "m@example.com", DisplayName: "Modern", PasswordHash: "hash", RegistrationUUID: "uuid-modern"}); err != nil {
		t.Fatalf("register modern: %v", err)
	}
	modernProfile, found, err := v2.GetProfile(ctx, "modern")
	if err != nil || !found || modernProfile.Pronouns != "unspecified" {
		t.Fatalf("modern profile = %+v, %v, %v", modernProfile, found, err)
	}

	// The old worker (v1) can still read/write records concurrently with rollout.
	if _, found, err := v1.GetProfile(ctx, "modern"); err != nil || !found {
		t.Fatalf("v1 reading modern record: %v, %v", found, err)
	}

	migrated, err := v2.MigrateProfiles(ctx)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if migrated != 1 {
		t.Fatalf("migrated = %d, want 1", migrated)
	}
	legacyProfile, found, err := v2.GetProfile(ctx, "legacy")
	if err != nil || !found || legacyProfile.Pronouns != "unspecified" {
		t.Fatalf("migrated legacy profile = %+v, %v, %v", legacyProfile, found, err)
	}
	if legacyProfile.DisplayName != "Legacy" {
		t.Fatalf("migration lost unrelated field: %+v", legacyProfile)
	}

	// Migration is idempotent.
	migratedAgain, err := v2.MigrateProfiles(ctx)
	if err != nil || migratedAgain != 0 {
		t.Fatalf("second migration = %d, %v", migratedAgain, err)
	}
}

func TestWorkerDeathReplaysOnlyUnacknowledgedMicrobatch(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	module := newTestModule(t, store, 2)

	if err := module.Post(ctx, Post{Author: "author", Destination: "wall", Content: "first"}); err != nil {
		t.Fatalf("post 1: %v", err)
	}
	if n, err := module.Advance(ctx); err != nil || n != 1 {
		t.Fatalf("advance 1 = %d, %v", n, err)
	}
	if err := module.Post(ctx, Post{Author: "author", Destination: "wall", Content: "second"}); err != nil {
		t.Fatalf("post 2: %v", err)
	}
	// Simulate a worker crash and restart by constructing a fresh module over
	// the same durable store; only the uncommitted second post should be
	// (re)processed on Advance.
	restarted, err := New(store, 2, SchemaVersion1, fixedClock(time.UnixMilli(1_700_000_200_000)))
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if n, err := restarted.Advance(ctx); err != nil || n != 1 {
		t.Fatalf("advance after restart = %d, %v", n, err)
	}
	count, err := restarted.PostCount(ctx, "wall")
	if err != nil || count != 2 {
		t.Fatalf("PostCount = %d, %v", count, err)
	}
	page, err := restarted.ResolvePosts(ctx, "wall", 0, 20)
	if err != nil || len(page.Posts) != 2 {
		t.Fatalf("resolved posts = %+v, %v", page, err)
	}
}

func TestInvalidInputsRejected(t *testing.T) {
	ctx := context.Background()
	module := newTestModule(t, storage.NewMemory(), 2)

	if _, err := module.RegisterUser(ctx, UserRegistration{}); err == nil {
		t.Fatal("expected invalid registration error")
	}
	if err := module.FriendRequest(ctx, "same", "same"); err == nil {
		t.Fatal("expected invalid friend request error")
	}
	if err := module.Post(ctx, Post{Author: "a", Destination: "b", Content: "  "}); err == nil {
		t.Fatal("expected invalid post error")
	}
	if err := module.RecordProfileView(ctx, ProfileView{UserID: "", TimestampMillis: 1}); err == nil {
		t.Fatal("expected invalid profile view error")
	}
	if _, err := module.ListFriends(ctx, "a", "", 0); err == nil {
		t.Fatal("expected invalid page size error")
	}
	if _, err := module.ListFriends(ctx, "a", "", 21); err == nil {
		t.Fatal("expected invalid page size error")
	}
	if _, err := module.ProfileViewCount(ctx, "a", 5, 1); err == nil {
		t.Fatal("expected invalid range error")
	}
}

func TestRebuildProducesSameQueryResults(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	module := newTestModule(t, store, 2)

	for _, id := range []string{"alice", "bob"} {
		if _, err := module.RegisterUser(ctx, UserRegistration{UserID: id, Email: id + "@example.com", DisplayName: id, PasswordHash: "hash", RegistrationUUID: "uuid-" + id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	if err := module.FriendRequest(ctx, "alice", "bob"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := module.AcceptFriendRequest(ctx, "bob", "alice"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := module.Post(ctx, Post{Author: "alice", Destination: "bob", Content: "hi"}); err != nil {
		t.Fatalf("post: %v", err)
	}
	if _, err := module.Advance(ctx); err != nil {
		t.Fatalf("advance: %v", err)
	}

	before, err := module.ResolvePosts(ctx, "bob", 0, 20)
	if err != nil {
		t.Fatalf("resolve before: %v", err)
	}
	beforeFriends, err := module.ListFriends(ctx, "alice", "", 20)
	if err != nil {
		t.Fatalf("friends before: %v", err)
	}

	if err := module.Rebuild(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	after, err := module.ResolvePosts(ctx, "bob", 0, 20)
	if err != nil {
		t.Fatalf("resolve after: %v", err)
	}
	afterFriends, err := module.ListFriends(ctx, "alice", "", 20)
	if err != nil {
		t.Fatalf("friends after: %v", err)
	}
	if len(before.Posts) != len(after.Posts) || len(before.Posts) == 0 {
		t.Fatalf("post counts differ: before=%d after=%d", len(before.Posts), len(after.Posts))
	}
	if before.Posts[0].Content != after.Posts[0].Content {
		t.Fatalf("post content differs after rebuild: %+v vs %+v", before.Posts[0], after.Posts[0])
	}
	if len(beforeFriends.UserIDs) != len(afterFriends.UserIDs) || len(beforeFriends.UserIDs) == 0 {
		t.Fatalf("friend counts differ: before=%d after=%d", len(beforeFriends.UserIDs), len(afterFriends.UserIDs))
	}
}

func TestIncompatibleConfigRejected(t *testing.T) {
	store := storage.NewMemory()
	if _, err := New(store, 4, SchemaVersion1, nil); err != nil {
		t.Fatalf("initial New: %v", err)
	}
	if _, err := New(store, 8, SchemaVersion1, nil); err == nil {
		t.Fatal("expected incompatible config error for different task count")
	}
	if _, err := New(store, 4, 99, nil); err == nil {
		t.Fatal("expected unsupported schema version error")
	}
}
