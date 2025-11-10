package main

import (
	"fmt"
	"golitter/internal/friends"
)

func main() {
	fmt.Println("Welcome to golitter!!")

	// Configure Pebble ..
	store := friends.NewPebbleStore()
	fmt.Println(store.GetFriends())

	// Run concurrent 10 concurrent scenarios in integratio tests?
	// Integration tests use MemStore
	mstore := friends.NewMemStore()
	fmt.Println(mstore.GetFriends())
}
