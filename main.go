package main

import (
	"fmt"
	"golitter/app"
	"golitter/lib/cfg"
	"strings"
	"sync/atomic"

	"github.com/golemcloud/golem-go/golemhost"
	"github.com/golemcloud/golem-go/std"
)

func init() {
	// runningAccountID zero value is considered as not logged in
	//runningAccountID = 1
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

// Sessions is to keep track of active sessions; key is the sessionID, value is the accountID, has an expiration time
// var Sessions map[uint64]uint64

func (a *AppImpl) DebugCurrentState() {
	std.Init(std.Packages{Os: true, NetHttp: true})
	//log.Println("Current State:", a)
	app.WasiLoggingLoggingLog(app.WasiLoggingLoggingLevelError(), "bob", "dood")
}

// runningAccountID is the next accountID available
var runningAccountID uint64

// Add implements app.ExportsGolitterAppApi.
func (*AppImpl) Add(value uint64) {
	std.Init(std.Packages{Os: true, NetHttp: true})
	selfWorkerName := golemhost.GetSelfMetadata().WorkerId.WorkerName

	componentThreeWorkerURI, err := cfg.ComponentThreeWorkerURI(selfWorkerName)
	if err != nil {
		fmt.Printf("%+v\n", err)
		return
	}

	p := app.NewComponentThreeApi(app.GolemRpc0_1_0_TypesUri(componentThreeWorkerURI))
	defer p.Drop()

	p.BlockingAdd(value)
}

// Get implements app.ExportsGolitterAppApi.
func (*AppImpl) Get() uint64 {
	std.Init(std.Packages{Os: true, NetHttp: true})
	selfWorkerName := golemhost.GetSelfMetadata().WorkerId.WorkerName

	componentThreeWorkerURI, err := cfg.ComponentThreeWorkerURI(selfWorkerName)
	if err != nil {
		fmt.Printf("%+v\n", err)
		return 0
	}

	//s := app.NewApi(app.GolemRpc0_1_0_TypesUri(componentThreeWorkerURI))
	p := app.NewComponentThreeApi(app.GolemRpc0_1_0_TypesUri(componentThreeWorkerURI))
	defer p.Drop()

	// Use BlockingGet for sync; otherwise will panic!! Otherwise is Optional!
	return p.BlockingGet()
}

// Login implements app.ExportsGolitterAppApi.
func (a *AppImpl) Login(username string) uint64 {
	std.Init(std.Packages{Os: true, NetHttp: true})
	// If username is empty; is invalid; to not go further ..
	if strings.TrimSpace(username) == "" {
		app.WasiLoggingLoggingLog(app.WasiLoggingLoggingLevelCritical(), "xxx", "Please specify username!!")
		return 0
	}
	// Check if the username is already in the database
	if accountID, exists := a.UserNames[username]; exists {
		// If yes, return the accountID
		return accountID
	}

	// If not, create a new account
	// Atomically update the runningAccountID
	newAccountID := atomic.AddUint64(&runningAccountID, 1)

	// Append the username to the map with the current runningAccountID
	a.UserNames[username] = newAccountID

	app.WasiLoggingLoggingLog(app.WasiLoggingLoggingLevelCritical(), "xxx", fmt.Sprint(a.UserNames))
	// Return the new accountID
	return newAccountID
}

func main() {
}
