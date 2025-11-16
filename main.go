package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"

	"github.com/cockroachdb/pebble/v2"

	"golitter/internal/friends"
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

func newInt64Merger() *pebble.Merger {
	return &pebble.Merger{
		Name: "int64-add",
		Merge: func(key, value []byte) (pebble.ValueMerger, error) {
			m := &int64ValueMerger{}
			if len(value) == 8 {
				m.sum = int64(binary.BigEndian.Uint64(value))
			}
			return m, nil
		},
	}
}

func main() {
	fmt.Println("Welcome to golitter!!")

	// Example 1: Using in-memory friendship store
	memFriendship := friends.NewMem()
	alice := memFriendship.ForUser("alice")
	bob := memFriendship.ForUser("bob")

	// Alice sends friend request to Bob
	if err := alice.AddFriendRequest("bob"); err != nil {
		log.Fatalf("Failed to send friend request: %v", err)
	}

	// Bob accepts the request
	if err := bob.AcceptFriendRequest("alice"); err != nil {
		log.Fatalf("Failed to accept friend request: %v", err)
	}

	// Get Alice's friends
	aliceFriends, _ := alice.GetFriends()
	fmt.Printf("Alice's friends: %v\n", aliceFriends)

	// Example 2: Using Pebble-backed friendship store
	db, err := pebble.Open("golitter-db", &pebble.Options{
		Merger: newInt64Merger(),
	})
	if err != nil {
		log.Fatalf("Failed to open pebble db: %v", err)
	}
	defer db.Close()

	pebbleFriendship := friends.NewPebble(db)
	charlie := pebbleFriendship.ForUser("charlie")
	dave := pebbleFriendship.ForUser("dave")

	// Charlie sends friend request to Dave
	if err := charlie.AddFriendRequest("dave"); err != nil {
		log.Fatalf("Failed to send friend request: %v", err)
	}

	// Dave accepts
	if err := dave.AcceptFriendRequest("charlie"); err != nil {
		log.Fatalf("Failed to accept friend request: %v", err)
	}

	// Get Charlie's friends
	charlieFriends, _ := charlie.GetFriends()
	fmt.Printf("Charlie's friends: %v\n", charlieFriends)

	fmt.Println("\nFriendship system working correctly!")
}
