package friends_test

import (
	"encoding/binary"
	"fmt"
	"io"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/stretchr/testify/require"

	"golitter/internal/friends"
	"golitter/internal/types"
)

// int64ValueMerger implements pebble.ValueMerger for int64 addition
type int64ValueMerger struct {
	sum int64
}

func (m *int64ValueMerger) MergeNewer(value []byte) error {
	if len(value) == 8 {
		m.sum += int64(binary.BigEndian.Uint64(value))
	}
	return nil
}

func (m *int64ValueMerger) MergeOlder(value []byte) error {
	if len(value) == 8 {
		m.sum += int64(binary.BigEndian.Uint64(value))
	}
	return nil
}

func (m *int64ValueMerger) Finish(includesBase bool) ([]byte, io.Closer, error) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(m.sum))
	return b[:], io.NopCloser(nil), nil
}

func openDB(t *testing.T) *pebble.DB {
	t.Helper()
	merger := &pebble.Merger{
		Name: "int64-add",
		Merge: func(key, value []byte) (pebble.ValueMerger, error) {
			m := &int64ValueMerger{}
			if len(value) == 8 {
				m.sum = int64(binary.BigEndian.Uint64(value))
			}
			return m, nil
		},
	}
	db, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem(), Merger: merger})
	require.NoError(t, err)
	return db
}

func TestMemBasics(t *testing.T) {
	// Create a shared friendship instance
	friendship := friends.NewMem()
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")

	// Initially no friends
	count, err := alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)
	count, err = bob.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)

	isFriend, err := alice.IsFriends("bob")
	require.NoError(t, err)
	require.False(t, isFriend)

	friendsList, err := alice.GetFriends()
	require.NoError(t, err)
	require.Empty(t, friendsList)

	// Alice sends friend request to Bob
	require.NoError(t, alice.AddFriendRequest("bob"))

	// Bob accepts the request
	require.NoError(t, bob.AcceptFriendRequest("alice"))

	// Now they should be friends
	count, err = alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = bob.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)

	isFriend, err = alice.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = bob.IsFriends("alice")
	require.NoError(t, err)
	require.True(t, isFriend)

	friendsList, err = alice.GetFriends()
	require.NoError(t, err)
	require.Equal(t, []types.UserID{"bob"}, friendsList)
	friendsList, err = bob.GetFriends()
	require.NoError(t, err)
	require.Equal(t, []types.UserID{"alice"}, friendsList)
}

func TestMemFriendRequestFlow(t *testing.T) {
	friendship := friends.NewMem()
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")
	charlie := friendship.ForUser("charlie")

	// Alice sends friend request to Bob
	require.NoError(t, alice.AddFriendRequest("bob"))
	count, err := alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)
	isFriend, err := alice.IsFriends("bob")
	require.NoError(t, err)
	require.False(t, isFriend)

	// Alice cancels the request
	require.NoError(t, alice.CancelFriendRequest("bob"))
	count, err = alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)

	// Alice sends request again
	require.NoError(t, alice.AddFriendRequest("bob"))

	// Bob accepts
	require.NoError(t, bob.AcceptFriendRequest("alice"))
	isFriend, err = alice.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = bob.IsFriends("alice")
	require.NoError(t, err)
	require.True(t, isFriend)

	// Charlie sends request to Alice
	require.NoError(t, charlie.AddFriendRequest("alice"))

	// Alice accepts
	require.NoError(t, alice.AcceptFriendRequest("charlie"))
	isFriend, err = alice.IsFriends("charlie")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = charlie.IsFriends("alice")
	require.NoError(t, err)
	require.True(t, isFriend)

	// Alice now has 2 friends
	count, err = alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 2, count)
	friendsList, err := alice.GetFriends()
	require.NoError(t, err)
	require.Len(t, friendsList, 2)
	require.Contains(t, friendsList, "bob")
	require.Contains(t, friendsList, "charlie")
}

func TestMemUnfriend(t *testing.T) {
	friendship := friends.NewMem()
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")

	// Become friends
	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, bob.AcceptFriendRequest("alice"))
	isFriend, err := alice.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)
	count, err := alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Alice unfriends Bob
	require.NoError(t, alice.Unfriend("bob"))

	// No longer friends
	isFriend, err = alice.IsFriends("bob")
	require.NoError(t, err)
	require.False(t, isFriend)
	isFriend, err = bob.IsFriends("alice")
	require.NoError(t, err)
	require.False(t, isFriend)
	count, err = alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)
	count, err = bob.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestMemMultipleUsers(t *testing.T) {
	friendship := friends.NewMem()
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")
	charlie := friendship.ForUser("charlie")
	dave := friendship.ForUser("dave")

	// Create a friend network: alice-bob-charlie, and dave is isolated
	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, bob.AcceptFriendRequest("alice"))

	require.NoError(t, bob.AddFriendRequest("charlie"))
	require.NoError(t, charlie.AcceptFriendRequest("bob"))

	// Verify connections
	isFriend, err := alice.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = bob.IsFriends("alice")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = bob.IsFriends("charlie")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = charlie.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = alice.IsFriends("charlie")
	require.NoError(t, err)
	require.False(t, isFriend)
	isFriend, err = alice.IsFriends("dave")
	require.NoError(t, err)
	require.False(t, isFriend)
	isFriend, err = dave.IsFriends("alice")
	require.NoError(t, err)
	require.False(t, isFriend)

	// Counts
	count, err := alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = bob.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 2, count)
	count, err = charlie.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = dave.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestMemEdgeCases(t *testing.T) {
	friendship := friends.NewMem()
	alice := friendship.ForUser("alice")

	// Operations on non-existent users
	require.NoError(t, alice.AddFriendRequest("nonexistent"))
	require.NoError(t, alice.CancelFriendRequest("nonexistent"))

	// Unfriend someone who isn't a friend (should return error)
	err := alice.Unfriend("stranger")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrNotFriends)

	// Accept request from someone who didn't send one (should return error)
	err = alice.AcceptFriendRequest("stranger")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrNoIncomingRequest)

	// Check friends of user with no friends
	count, err := alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)
	friendsList, err := alice.GetFriends()
	require.NoError(t, err)
	require.Empty(t, friendsList)
}

func TestMemGeneralOperations(t *testing.T) {
	// Test general operations (explicit userID)
	friendship := friends.NewMem()

	// General operations with explicit userID
	require.NoError(t, friendship.AddFriendRequest("alice", "bob"))
	// AcceptFriendRequest(from, to) means "to accepts a request from from"
	// So "bob accepts a request from alice" is AcceptFriendRequest("alice", "bob")
	require.NoError(t, friendship.AcceptFriendRequest("alice", "bob"))

	isFriend, err := friendship.IsFriends("alice", "bob")
	require.NoError(t, err)
	require.True(t, isFriend)

	friendsList, err := friendship.GetFriends("alice")
	require.NoError(t, err)
	require.Contains(t, friendsList, "bob")

	count, err := friendship.FriendsCount("alice")
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestPebbleBasics(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	friendship := friends.NewPebble(db)
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")

	// Initially no friends
	count, err := alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)
	count, err = bob.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)

	isFriend, err := alice.IsFriends("bob")
	require.NoError(t, err)
	require.False(t, isFriend)

	friendsList, err := alice.GetFriends()
	require.NoError(t, err)
	require.Empty(t, friendsList)

	// Alice sends friend request to Bob
	require.NoError(t, alice.AddFriendRequest("bob"))

	// Bob accepts the request
	require.NoError(t, bob.AcceptFriendRequest("alice"))

	// Now they should be friends
	count, err = alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = bob.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)

	isFriend, err = alice.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = bob.IsFriends("alice")
	require.NoError(t, err)
	require.True(t, isFriend)

	friendsList, err = bob.GetFriends()
	require.NoError(t, err)
	require.Equal(t, []types.UserID{"alice"}, friendsList)
	friendsList, err = alice.GetFriends()
	require.NoError(t, err)
	require.Len(t, friendsList, 1)
	require.Contains(t, friendsList, "bob")
}

func TestPebbleFriendRequestFlow(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	friendship := friends.NewPebble(db)
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")
	charlie := friendship.ForUser("charlie")

	// Alice sends friend request to Bob
	require.NoError(t, alice.AddFriendRequest("bob"))
	count, err := alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)
	isFriend, err := alice.IsFriends("bob")
	require.NoError(t, err)
	require.False(t, isFriend)

	// Alice cancels the request
	require.NoError(t, alice.CancelFriendRequest("bob"))
	count, err = alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)

	// Alice sends request again
	require.NoError(t, alice.AddFriendRequest("bob"))

	// Bob accepts
	require.NoError(t, bob.AcceptFriendRequest("alice"))
	isFriend, err = alice.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = bob.IsFriends("alice")
	require.NoError(t, err)
	require.True(t, isFriend)

	// Charlie sends request to Alice
	require.NoError(t, charlie.AddFriendRequest("alice"))

	// Alice accepts
	require.NoError(t, alice.AcceptFriendRequest("charlie"))
	isFriend, err = alice.IsFriends("charlie")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = charlie.IsFriends("alice")
	require.NoError(t, err)
	require.True(t, isFriend)

	// Alice now has 2 friends
	count, err = alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 2, count)
	friendsList, err := alice.GetFriends()
	require.NoError(t, err)
	require.Len(t, friendsList, 2)
	require.Contains(t, friendsList, "bob")
	require.Contains(t, friendsList, "charlie")
}

func TestPebbleUnfriend(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	friendship := friends.NewPebble(db)
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")

	// Become friends
	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, bob.AcceptFriendRequest("alice"))
	isFriend, err := alice.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)
	count, err := alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Alice unfriends Bob
	require.NoError(t, alice.Unfriend("bob"))

	// No longer friends
	isFriend, err = alice.IsFriends("bob")
	require.NoError(t, err)
	require.False(t, isFriend)
	isFriend, err = bob.IsFriends("alice")
	require.NoError(t, err)
	require.False(t, isFriend)
	count, err = alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)
	count, err = bob.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestPebbleMultipleUsers(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	friendship := friends.NewPebble(db)
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")
	charlie := friendship.ForUser("charlie")
	dave := friendship.ForUser("dave")

	// Create a friend network: alice-bob-charlie, and dave is isolated
	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, bob.AcceptFriendRequest("alice"))

	require.NoError(t, bob.AddFriendRequest("charlie"))
	require.NoError(t, charlie.AcceptFriendRequest("bob"))

	// Verify connections
	isFriend, err := alice.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = bob.IsFriends("alice")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = bob.IsFriends("charlie")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = charlie.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = alice.IsFriends("charlie")
	require.NoError(t, err)
	require.False(t, isFriend)
	isFriend, err = alice.IsFriends("dave")
	require.NoError(t, err)
	require.False(t, isFriend)
	isFriend, err = dave.IsFriends("alice")
	require.NoError(t, err)
	require.False(t, isFriend)

	// Counts
	count, err := alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = bob.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 2, count)
	count, err = charlie.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = dave.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestPebbleEdgeCases(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	friendship := friends.NewPebble(db)
	alice := friendship.ForUser("alice")

	// Operations on non-existent users
	require.NoError(t, alice.AddFriendRequest("nonexistent"))
	require.NoError(t, alice.CancelFriendRequest("nonexistent"))

	// Unfriend someone who isn't a friend (should return error)
	err := alice.Unfriend("stranger")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrNotFriends)

	// Accept request from someone who didn't send one (should return error)
	err = alice.AcceptFriendRequest("stranger")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrNoIncomingRequest)

	// Check friends of user with no friends
	count, err := alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)
	friendsList, err := alice.GetFriends()
	require.NoError(t, err)
	require.Empty(t, friendsList)
}

func TestPebblePersistence(t *testing.T) {
	// Test that data persists across store instances
	db := openDB(t)
	defer db.Close()

	friendship1 := friends.NewPebble(db)
	alice1 := friendship1.ForUser("alice")
	bob1 := friendship1.ForUser("bob")

	// Create friendship
	require.NoError(t, alice1.AddFriendRequest("bob"))
	require.NoError(t, bob1.AcceptFriendRequest("alice"))
	isFriend, err := alice1.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)

	// Create new store instances pointing to same DB
	friendship2 := friends.NewPebble(db)
	alice2 := friendship2.ForUser("alice")
	bob2 := friendship2.ForUser("bob")

	// Should still be friends
	isFriend, err = alice2.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)
	isFriend, err = bob2.IsFriends("alice")
	require.NoError(t, err)
	require.True(t, isFriend)
	count, err := alice2.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = bob2.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestPebbleGeneralOperations(t *testing.T) {
	// Test general operations (explicit userID)
	db := openDB(t)
	defer db.Close()

	friendship := friends.NewPebble(db)

	// General operations with explicit userID
	require.NoError(t, friendship.AddFriendRequest("alice", "bob"))
	// AcceptFriendRequest(from, to) means "to accepts a request from from"
	// So "bob accepts a request from alice" is AcceptFriendRequest("alice", "bob")
	require.NoError(t, friendship.AcceptFriendRequest("alice", "bob"))

	isFriend, err := friendship.IsFriends("alice", "bob")
	require.NoError(t, err)
	require.True(t, isFriend)

	friendsList, err := friendship.GetFriends("alice")
	require.NoError(t, err)
	require.Contains(t, friendsList, "bob")

	count, err := friendship.FriendsCount("alice")
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestRealisticScenario(t *testing.T) {
	// Simulate a realistic social network scenario
	friendship := friends.NewMem()
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")
	dave := friendship.ForUser("dave")
	eve := friendship.ForUser("eve")

	// Scenario: Alice wants to build her network
	// 1. Alice sends requests to Bob, Charlie, and Dave
	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, alice.AddFriendRequest("charlie"))
	require.NoError(t, alice.AddFriendRequest("dave"))

	// 2. Bob accepts immediately
	require.NoError(t, bob.AcceptFriendRequest("alice"))
	isFriend, err := alice.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)
	count, err := alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// 3. Charlie declines by not accepting (we cancel the request)
	require.NoError(t, alice.CancelFriendRequest("charlie"))
	isFriend, err = alice.IsFriends("charlie")
	require.NoError(t, err)
	require.False(t, isFriend)

	// 4. Dave accepts later
	require.NoError(t, dave.AcceptFriendRequest("alice"))
	isFriend, err = alice.IsFriends("dave")
	require.NoError(t, err)
	require.True(t, isFriend)
	count, err = alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 2, count)

	// 5. Eve sends a request to Alice
	require.NoError(t, eve.AddFriendRequest("alice"))
	require.NoError(t, alice.AcceptFriendRequest("eve"))
	isFriend, err = alice.IsFriends("eve")
	require.NoError(t, err)
	require.True(t, isFriend)
	count, err = alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 3, count)

	// 6. Bob and Dave become friends through Alice's network
	require.NoError(t, bob.AddFriendRequest("dave"))
	require.NoError(t, dave.AcceptFriendRequest("bob"))
	isFriend, err = bob.IsFriends("dave")
	require.NoError(t, err)
	require.True(t, isFriend)

	// 7. Alice has a falling out with Eve and unfriends her
	require.NoError(t, alice.Unfriend("eve"))
	isFriend, err = alice.IsFriends("eve")
	require.NoError(t, err)
	require.False(t, isFriend)
	count, err = alice.FriendsCount()
	require.NoError(t, err)
	require.Equal(t, 2, count)

	// Final state verification
	friendsList, err := alice.GetFriends()
	require.NoError(t, err)
	require.Len(t, friendsList, 2)
	require.Contains(t, friendsList, "bob")
	require.Contains(t, friendsList, "dave")
	require.NotContains(t, friendsList, "charlie")
	require.NotContains(t, friendsList, "eve")
}

// New tests for error handling and validation

func TestMemSelfFriendship(t *testing.T) {
	friendship := friends.NewMem()
	alice := friendship.ForUser("alice")

	// Cannot send friend request to self
	err := alice.AddFriendRequest("alice")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrSelfFriendship)

	// Cannot cancel request to self
	err = alice.CancelFriendRequest("alice")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrSelfFriendship)

	// Cannot accept request from self
	err = alice.AcceptFriendRequest("alice")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrSelfFriendship)

	// Cannot unfriend self
	err = alice.Unfriend("alice")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrSelfFriendship)
}

func TestMemDuplicateRequests(t *testing.T) {
	friendship := friends.NewMem()
	alice := friendship.ForUser("alice")

	// Send request
	require.NoError(t, alice.AddFriendRequest("bob"))

	// Try to send again
	err := alice.AddFriendRequest("bob")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrRequestAlreadyExists)
}

func TestMemRequestAlreadyFriends(t *testing.T) {
	friendship := friends.NewMem()
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")

	// Become friends
	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, bob.AcceptFriendRequest("alice"))

	// Try to send request when already friends
	err := alice.AddFriendRequest("bob")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrAlreadyFriends)
}

func TestMemRequestListing(t *testing.T) {
	friendship := friends.NewMem()
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")
	_ = friendship.ForUser("charlie")

	// Alice sends requests to Bob and Charlie
	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, alice.AddFriendRequest("charlie"))

	// Check Alice's outgoing requests
	outgoing, err := alice.GetOutgoingRequests()
	require.NoError(t, err)
	require.Len(t, outgoing, 2)
	require.Contains(t, outgoing, types.UserID("bob"))
	require.Contains(t, outgoing, types.UserID("charlie"))

	// Check Bob's incoming requests
	incoming, err := bob.GetIncomingRequests()
	require.NoError(t, err)
	require.Len(t, incoming, 1)
	require.Contains(t, incoming, types.UserID("alice"))

	// Bob accepts
	require.NoError(t, bob.AcceptFriendRequest("alice"))

	// Now Alice should only have one outgoing request
	outgoing, err = alice.GetOutgoingRequests()
	require.NoError(t, err)
	require.Len(t, outgoing, 1)
	require.Contains(t, outgoing, types.UserID("charlie"))

	// Bob should have no incoming requests
	incoming, err = bob.GetIncomingRequests()
	require.NoError(t, err)
	require.Empty(t, incoming)
}

func TestMemMemoryCleanup(t *testing.T) {
	friendship := friends.NewMem()
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")

	// Become friends
	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, bob.AcceptFriendRequest("alice"))

	// Verify friendship
	isFriend, err := alice.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)

	// Unfriend
	require.NoError(t, alice.Unfriend("bob"))

	// Verify the internal maps are cleaned up by becoming friends again
	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, bob.AcceptFriendRequest("alice"))
	require.NoError(t, alice.Unfriend("bob"))

	// Send and cancel request to verify cleanup
	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, alice.CancelFriendRequest("bob"))
}

func TestMemConcurrency(t *testing.T) {
	t.Parallel()
	friendship := friends.NewMem()

	// Create 100 users concurrently sending requests
	const numUsers = 100
	done := make(chan bool, numUsers)

	for i := 0; i < numUsers; i++ {
		go func(id int) {
			defer func() { done <- true }()
			userID := types.UserID(fmt.Sprintf("user%d", id))
			user := friendship.ForUser(userID)

			// Send requests to next 5 users
			for j := 1; j <= 5; j++ {
				targetID := types.UserID(fmt.Sprintf("user%d", (id+j)%numUsers))
				_ = user.AddFriendRequest(targetID)
			}

			// Accept some incoming requests
			incoming, _ := user.GetIncomingRequests()
			for _, from := range incoming {
				_ = user.AcceptFriendRequest(from)
			}
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < numUsers; i++ {
		<-done
	}

	// Verify no crashes and basic consistency
	user0 := friendship.ForUser("user0")
	count, err := user0.FriendsCount()
	require.NoError(t, err)
	t.Logf("User0 has %d friends", count)
}

func TestPebbleSelfFriendship(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	friendship := friends.NewPebble(db)
	alice := friendship.ForUser("alice")

	// Cannot send friend request to self
	err := alice.AddFriendRequest("alice")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrSelfFriendship)

	// Cannot cancel request to self
	err = alice.CancelFriendRequest("alice")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrSelfFriendship)

	// Cannot accept request from self
	err = alice.AcceptFriendRequest("alice")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrSelfFriendship)

	// Cannot unfriend self
	err = alice.Unfriend("alice")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrSelfFriendship)
}

func TestPebbleDuplicateRequests(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	friendship := friends.NewPebble(db)
	alice := friendship.ForUser("alice")

	// Send request
	require.NoError(t, alice.AddFriendRequest("bob"))

	// Try to send again
	err := alice.AddFriendRequest("bob")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrRequestAlreadyExists)
}

func TestPebbleRequestAlreadyFriends(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	friendship := friends.NewPebble(db)
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")

	// Become friends
	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, bob.AcceptFriendRequest("alice"))

	// Try to send request when already friends
	err := alice.AddFriendRequest("bob")
	require.Error(t, err)
	require.ErrorIs(t, err, friends.ErrAlreadyFriends)
}

func TestPebbleRequestListing(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	friendship := friends.NewPebble(db)
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")
	_ = friendship.ForUser("charlie")

	// Alice sends requests to Bob and Charlie
	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, alice.AddFriendRequest("charlie"))

	// Check Alice's outgoing requests
	outgoing, err := alice.GetOutgoingRequests()
	require.NoError(t, err)
	require.Len(t, outgoing, 2)
	require.Contains(t, outgoing, types.UserID("bob"))
	require.Contains(t, outgoing, types.UserID("charlie"))

	// Check Bob's incoming requests
	incoming, err := bob.GetIncomingRequests()
	require.NoError(t, err)
	require.Len(t, incoming, 1)
	require.Contains(t, incoming, types.UserID("alice"))

	// Bob accepts
	require.NoError(t, bob.AcceptFriendRequest("alice"))

	// Now Alice should only have one outgoing request
	outgoing, err = alice.GetOutgoingRequests()
	require.NoError(t, err)
	require.Len(t, outgoing, 1)
	require.Contains(t, outgoing, types.UserID("charlie"))

	// Bob should have no incoming requests
	incoming, err = bob.GetIncomingRequests()
	require.NoError(t, err)
	require.Empty(t, incoming)
}

func TestPebbleWriteOptions(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	// Test with NoSync for performance
	friendship := friends.NewPebbleWithOptions(db, pebble.NoSync, nil)
	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")

	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, bob.AcceptFriendRequest("alice"))

	isFriend, err := alice.IsFriends("bob")
	require.NoError(t, err)
	require.True(t, isFriend)
}

func TestPebbleConcurrency(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	friendship := friends.NewPebbleWithOptions(db, pebble.NoSync, nil)

	// Create 50 users concurrently sending requests
	const numUsers = 50
	done := make(chan bool, numUsers)

	for i := 0; i < numUsers; i++ {
		go func(id int) {
			defer func() { done <- true }()
			userID := types.UserID(fmt.Sprintf("user%d", id))
			user := friendship.ForUser(userID)

			// Send requests to next 3 users
			for j := 1; j <= 3; j++ {
				targetID := types.UserID(fmt.Sprintf("user%d", (id+j)%numUsers))
				_ = user.AddFriendRequest(targetID)
			}

			// Accept some incoming requests
			incoming, _ := user.GetIncomingRequests()
			for _, from := range incoming {
				_ = user.AcceptFriendRequest(from)
			}
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < numUsers; i++ {
		<-done
	}

	// Verify no crashes and basic consistency
	user0 := friendship.ForUser("user0")
	count, err := user0.FriendsCount()
	require.NoError(t, err)
	t.Logf("User0 has %d friends", count)
}

func TestAnonymousMethodReplacement(t *testing.T) {
	// Test that we can replace methods with custom implementations
	friendship := friends.NewMem()

	// Replace GetFriends with a custom implementation
	originalGetFriends := friendship.GetFriends
	callCount := 0
	friendship.GetFriends = func(user types.UserID) ([]types.UserID, error) {
		callCount++
		return originalGetFriends(user)
	}

	alice := friendship.ForUser("alice")
	bob := friendship.ForUser("bob")

	require.NoError(t, alice.AddFriendRequest("bob"))
	require.NoError(t, bob.AcceptFriendRequest("alice"))

	_, err := alice.GetFriends()
	require.NoError(t, err)
	require.Equal(t, 1, callCount)

	_, err = bob.GetFriends()
	require.NoError(t, err)
	require.Equal(t, 2, callCount)
}
