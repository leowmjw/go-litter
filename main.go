package main

import (
	"golitter/app"
	"sync/atomic"

	"github.com/golemcloud/golem-go/std"
)

type RequestBody struct {
	CurrentTotal uint64
}

type ResponseBody struct {
	Message string
}

func init() {
	// runningAccountID zero value is considered as not logged in
	runningAccountID = 1
	// Initialize map of usernames to accountIDs
	usernames := make(map[string]uint64)
	app.SetExportsGolitterAppApi(&AppImpl{
		UserNames: usernames,
	})
}

// AppImpl is the singleton of GoLitter App
type AppImpl struct {
	UserNames map[string]uint64
}

// runningAccountID is the next accountID available
var runningAccountID uint64

// Add implements app.ExportsGolitterAppApi.
func (*AppImpl) Add(value uint64) {
	panic("unimplemented")
}

// Get implements app.ExportsGolitterAppApi.
func (*AppImpl) Get() uint64 {
	panic("unimplemented")
}

// Login implements app.ExportsGolitterAppApi.
func (a *AppImpl) Login(username string) uint64 {
	std.Init(std.Packages{Os: true, NetHttp: true})
	// Check if the username is already in the database
	if accountID, exists := a.UserNames[username]; exists {
		// If yes, return the accountID
		return accountID
	}

	// If not, create a new account
	// Atomically update the runningAccountID
	newAccountID := atomic.AddUint64(&runningAccountID, 1)

	// Append the username to the map
	a.UserNames[username] = newAccountID

	// Return the new accountID
	return newAccountID
}

// func init() {
// 	app.SetExportsComponentsAccountsApi(&AccountsImpl{})
// }

// // total State can be stored in global variables
// var total uint64

// type AccountsImpl struct {
// }

// // Implementation of the exported interface

// func (e *AccountsImpl) Add(value uint64) {
// 	std.Init(std.Packages{Os: true, NetHttp: true})

// 	total += value
// }

// func (e *AccountsImpl) Get() uint64 {
// 	std.Init(std.Packages{Os: true, NetHttp: true})

// 	return total
// }

func main() {
}
