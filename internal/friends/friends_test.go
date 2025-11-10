package friends_test

import (
	"encoding/binary"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/stretchr/testify/require"

	"golitter/internal/friends"
	"golitter/internal/types"
)

func openDB(t *testing.T) *pebble.DB {
	t.Helper()
	merger := &pebble.Merger{
		Name: "int64-add",
		Merge: func(key, value []byte) (pebble.ValueMerger, error) {
			type vm struct{ sum int64 }
			v := &vm{}
			if len(value) == 8 {
				v.sum = int64(binary.BigEndian.Uint64(value))
			}
			return pebble.NewValueMerger(
				func(oldV []byte) error {
					if len(oldV) == 8 {
						v.sum += int64(binary.BigEndian.Uint64(oldV))
					}
					return nil
				},
				func(newV []byte) error {
					if len(newV) == 8 {
						v.sum += int64(binary.BigEndian.Uint64(newV))
					}
					return nil
				},
				func(includesBase bool) ([]byte, func() error, error) {
					var b [8]byte
					binary.BigEndian.PutUint64(b[:], uint64(v.sum))
					return b[:], func() error { return nil }, nil
				},
			), nil
		},
	}
	db, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem(), Merger: merger})
	require.NoError(t, err)
	return db
}

func TestMemBasics(t *testing.T) {
	// Create a shared backing store for all users
	backing := friends.NewMemBackingStore()
	alice := friends.NewMemStoreWithBacking("alice", backing)
	bob := friends.NewMemStoreWithBacking("bob", backing)

	// Initially no friends
	require.Equal(t, 0, alice.FriendsCount())
	require.Equal(t, 0, bob.FriendsCount())
	require.False(t, alice.IsFriends("bob"))
	require.Empty(t, alice.GetFriends())

	// Alice sends friend request to Bob
	alice.AddFriendRequest("bob")

	// Bob accepts the request
	bob.AcceptFriendRequest("alice")

	// Now they should be friends
	require.Equal(t, 1, alice.FriendsCount())
	require.Equal(t, 1, bob.FriendsCount())
	require.True(t, alice.IsFriends("bob"))
	require.True(t, bob.IsFriends("alice"))
	require.Equal(t, []types.UserID{"bob"}, alice.GetFriends())
	require.Equal(t, []types.UserID{"alice"}, bob.GetFriends())
}

func TestMemFriendRequestFlow(t *testing.T) {
	backing := friends.NewMemBackingStore()
	alice := friends.NewMemStoreWithBacking("alice", backing)
	bob := friends.NewMemStoreWithBacking("bob", backing)
	charlie := friends.NewMemStoreWithBacking("charlie", backing)

	// Alice sends friend request to Bob
	alice.AddFriendRequest("bob")
	require.Equal(t, 0, alice.FriendsCount())
	require.False(t, alice.IsFriends("bob"))

	// Alice cancels the request
	alice.CancelFriendRequest("bob")
	require.Equal(t, 0, alice.FriendsCount())

	// Alice sends request again
	alice.AddFriendRequest("bob")

	// Bob accepts
	bob.AcceptFriendRequest("alice")
	require.True(t, alice.IsFriends("bob"))
	require.True(t, bob.IsFriends("alice"))

	// Charlie sends request to Alice
	charlie.AddFriendRequest("alice")

	// Alice accepts
	alice.AcceptFriendRequest("charlie")
	require.True(t, alice.IsFriends("charlie"))
	require.True(t, charlie.IsFriends("alice"))

	// Alice now has 2 friends
	require.Equal(t, 2, alice.FriendsCount())
	friends := alice.GetFriends()
	require.Len(t, friends, 2)
	require.Contains(t, friends, "bob")
	require.Contains(t, friends, "charlie")
}

func TestMemUnfriend(t *testing.T) {
	backing := friends.NewMemBackingStore()
	alice := friends.NewMemStoreWithBacking("alice", backing)
	bob := friends.NewMemStoreWithBacking("bob", backing)

	// Become friends
	alice.AddFriendRequest("bob")
	bob.AcceptFriendRequest("alice")
	require.True(t, alice.IsFriends("bob"))
	require.Equal(t, 1, alice.FriendsCount())

	// Alice unfriends Bob
	alice.Unfriend("bob")

	// No longer friends
	require.False(t, alice.IsFriends("bob"))
	require.False(t, bob.IsFriends("alice"))
	require.Equal(t, 0, alice.FriendsCount())
	require.Equal(t, 0, bob.FriendsCount())
}

func TestMemMultipleUsers(t *testing.T) {
	backing := friends.NewMemBackingStore()
	alice := friends.NewMemStoreWithBacking("alice", backing)
	bob := friends.NewMemStoreWithBacking("bob", backing)
	charlie := friends.NewMemStoreWithBacking("charlie", backing)
	dave := friends.NewMemStoreWithBacking("dave", backing)

	// Create a friend network: alice-bob-charlie, and dave is isolated
	alice.AddFriendRequest("bob")
	bob.AcceptFriendRequest("alice")

	bob.AddFriendRequest("charlie")
	charlie.AcceptFriendRequest("bob")

	// Verify connections
	require.True(t, alice.IsFriends("bob"))
	require.True(t, bob.IsFriends("alice"))
	require.True(t, bob.IsFriends("charlie"))
	require.True(t, charlie.IsFriends("bob"))
	require.False(t, alice.IsFriends("charlie"))
	require.False(t, alice.IsFriends("dave"))
	require.False(t, dave.IsFriends("alice"))

	// Counts
	require.Equal(t, 1, alice.FriendsCount())
	require.Equal(t, 2, bob.FriendsCount())
	require.Equal(t, 1, charlie.FriendsCount())
	require.Equal(t, 0, dave.FriendsCount())
}

func TestMemEdgeCases(t *testing.T) {
	backing := friends.NewMemBackingStore()
	alice := friends.NewMemStoreWithBacking("alice", backing)

	// Operations on non-existent users
	alice.AddFriendRequest("nonexistent")
	alice.CancelFriendRequest("nonexistent")

	// Unfriend someone who isn't a friend (should be safe)
	alice.Unfriend("stranger")

	// Accept request from someone who didn't send one (should be safe)
	alice.AcceptFriendRequest("stranger")

	// Check friends of user with no friends
	require.Equal(t, 0, alice.FriendsCount())
	require.Empty(t, alice.GetFriends())
}

func TestPebbleBasics(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	alice := friends.NewPebbleStore("alice", db)
	bob := friends.NewPebbleStore("bob", db)

	// Initially no friends
	require.Equal(t, 0, alice.FriendsCount())
	require.Equal(t, 0, bob.FriendsCount())
	require.False(t, alice.IsFriends("bob"))
	require.Empty(t, alice.GetFriends())

	// Alice sends friend request to Bob
	alice.AddFriendRequest("bob")

	// Bob accepts the request
	bob.AcceptFriendRequest("alice")

	// Now they should be friends
	require.Equal(t, 1, alice.FriendsCount())
	require.Equal(t, 1, bob.FriendsCount())
	require.True(t, alice.IsFriends("bob"))
	require.True(t, bob.IsFriends("alice"))
	require.Equal(t, []types.UserID{"alice"}, bob.GetFriends())
	aliceFriends := alice.GetFriends()
	require.Len(t, aliceFriends, 1)
	require.Contains(t, aliceFriends, "bob")
}

func TestPebbleFriendRequestFlow(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	alice := friends.NewPebbleStore("alice", db)
	bob := friends.NewPebbleStore("bob", db)
	charlie := friends.NewPebbleStore("charlie", db)

	// Alice sends friend request to Bob
	alice.AddFriendRequest("bob")
	require.Equal(t, 0, alice.FriendsCount())
	require.False(t, alice.IsFriends("bob"))

	// Alice cancels the request
	alice.CancelFriendRequest("bob")
	require.Equal(t, 0, alice.FriendsCount())

	// Alice sends request again
	alice.AddFriendRequest("bob")

	// Bob accepts
	bob.AcceptFriendRequest("alice")
	require.True(t, alice.IsFriends("bob"))
	require.True(t, bob.IsFriends("alice"))

	// Charlie sends request to Alice
	charlie.AddFriendRequest("alice")

	// Alice accepts
	alice.AcceptFriendRequest("charlie")
	require.True(t, alice.IsFriends("charlie"))
	require.True(t, charlie.IsFriends("alice"))

	// Alice now has 2 friends
	require.Equal(t, 2, alice.FriendsCount())
	friends := alice.GetFriends()
	require.Len(t, friends, 2)
	require.Contains(t, friends, "bob")
	require.Contains(t, friends, "charlie")
}

func TestPebbleUnfriend(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	alice := friends.NewPebbleStore("alice", db)
	bob := friends.NewPebbleStore("bob", db)

	// Become friends
	alice.AddFriendRequest("bob")
	bob.AcceptFriendRequest("alice")
	require.True(t, alice.IsFriends("bob"))
	require.Equal(t, 1, alice.FriendsCount())

	// Alice unfriends Bob
	alice.Unfriend("bob")

	// No longer friends
	require.False(t, alice.IsFriends("bob"))
	require.False(t, bob.IsFriends("alice"))
	require.Equal(t, 0, alice.FriendsCount())
	require.Equal(t, 0, bob.FriendsCount())
}

func TestPebbleMultipleUsers(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	alice := friends.NewPebbleStore("alice", db)
	bob := friends.NewPebbleStore("bob", db)
	charlie := friends.NewPebbleStore("charlie", db)
	dave := friends.NewPebbleStore("dave", db)

	// Create a friend network: alice-bob-charlie, and dave is isolated
	alice.AddFriendRequest("bob")
	bob.AcceptFriendRequest("alice")

	bob.AddFriendRequest("charlie")
	charlie.AcceptFriendRequest("bob")

	// Verify connections
	require.True(t, alice.IsFriends("bob"))
	require.True(t, bob.IsFriends("alice"))
	require.True(t, bob.IsFriends("charlie"))
	require.True(t, charlie.IsFriends("bob"))
	require.False(t, alice.IsFriends("charlie"))
	require.False(t, alice.IsFriends("dave"))
	require.False(t, dave.IsFriends("alice"))

	// Counts
	require.Equal(t, 1, alice.FriendsCount())
	require.Equal(t, 2, bob.FriendsCount())
	require.Equal(t, 1, charlie.FriendsCount())
	require.Equal(t, 0, dave.FriendsCount())
}

func TestPebbleEdgeCases(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	alice := friends.NewPebbleStore("alice", db)

	// Operations on non-existent users
	alice.AddFriendRequest("nonexistent")
	alice.CancelFriendRequest("nonexistent")

	// Unfriend someone who isn't a friend (should be safe)
	alice.Unfriend("stranger")

	// Accept request from someone who didn't send one (should be safe)
	alice.AcceptFriendRequest("stranger")

	// Check friends of user with no friends
	require.Equal(t, 0, alice.FriendsCount())
	require.Empty(t, alice.GetFriends())
}

func TestPebblePersistence(t *testing.T) {
	// Test that data persists across store instances
	db := openDB(t)
	defer db.Close()

	alice1 := friends.NewPebbleStore("alice", db)
	bob1 := friends.NewPebbleStore("bob", db)

	// Create friendship
	alice1.AddFriendRequest("bob")
	bob1.AcceptFriendRequest("alice")
	require.True(t, alice1.IsFriends("bob"))

	// Create new store instances pointing to same DB
	alice2 := friends.NewPebbleStore("alice", db)
	bob2 := friends.NewPebbleStore("bob", db)

	// Should still be friends
	require.True(t, alice2.IsFriends("bob"))
	require.True(t, bob2.IsFriends("alice"))
	require.Equal(t, 1, alice2.FriendsCount())
	require.Equal(t, 1, bob2.FriendsCount())
}

func TestRealisticScenario(t *testing.T) {
	// Simulate a realistic social network scenario
	backing := friends.NewMemBackingStore()
	alice := friends.NewMemStoreWithBacking("alice", backing)
	bob := friends.NewMemStoreWithBacking("bob", backing)
	charlie := friends.NewMemStoreWithBacking("charlie", backing)
	dave := friends.NewMemStoreWithBacking("dave", backing)
	eve := friends.NewMemStoreWithBacking("eve", backing)

	// Scenario: Alice wants to build her network
	// 1. Alice sends requests to Bob, Charlie, and Dave
	alice.AddFriendRequest("bob")
	alice.AddFriendRequest("charlie")
	alice.AddFriendRequest("dave")

	// 2. Bob accepts immediately
	bob.AcceptFriendRequest("alice")
	require.True(t, alice.IsFriends("bob"))
	require.Equal(t, 1, alice.FriendsCount())

	// 3. Charlie declines by not accepting (we cancel the request)
	alice.CancelFriendRequest("charlie")
	require.False(t, alice.IsFriends("charlie"))

	// 4. Dave accepts later
	dave.AcceptFriendRequest("alice")
	require.True(t, alice.IsFriends("dave"))
	require.Equal(t, 2, alice.FriendsCount())

	// 5. Eve sends a request to Alice
	eve.AddFriendRequest("alice")
	alice.AcceptFriendRequest("eve")
	require.True(t, alice.IsFriends("eve"))
	require.Equal(t, 3, alice.FriendsCount())

	// 6. Bob and Dave become friends through Alice's network
	bob.AddFriendRequest("dave")
	dave.AcceptFriendRequest("bob")
	require.True(t, bob.IsFriends("dave"))

	// 7. Alice has a falling out with Eve and unfriends her
	alice.Unfriend("eve")
	require.False(t, alice.IsFriends("eve"))
	require.Equal(t, 2, alice.FriendsCount())

	// Final state verification
	aliceFriends := alice.GetFriends()
	require.Len(t, aliceFriends, 2)
	require.Contains(t, aliceFriends, "bob")
	require.Contains(t, aliceFriends, "dave")
	require.NotContains(t, aliceFriends, "charlie")
	require.NotContains(t, aliceFriends, "eve")
}

