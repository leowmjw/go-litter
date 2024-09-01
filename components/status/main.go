package main

import (
	status "golitter/components/status/binding"
	"strings"
	"sync/atomic"
	"time"

	"github.com/golemcloud/golem-go/std"
)

// runningStatusID is the next statusID available
var runningStatusID uint64

func init() {

	// Initialize Statuses map
	Statuses := make(map[uint64]StatusDetails)
	status.SetExportsComponentsStatusApi(&StatusImpl{Statuses: Statuses})

}

type StatusImpl struct {
	// Statuses map[uint64]string
	Statuses map[uint64]StatusDetails
}

type StatusDetails struct {
	ID        uint64
	AccountID uint64
	StatusMsg string
	PostedAt  time.Time
}

// DebugCurrentState implements status.ExportsComponentsStatusApi.
func (s *StatusImpl) DebugCurrentState() {
	std.Init(std.Packages{Os: true, NetHttp: true})

	status.WasiLoggingLoggingLog(status.WasiLoggingLoggingLevelCritical(), "DEBUG", s.Statuses[0].StatusMsg)
}

// GetStatus implements status.ExportsComponentsStatusApi.
func (s *StatusImpl) GetStatus(status_id uint64) string {
	// Check if exist, implement map exist
	if status, exists := s.Statuses[status_id]; exists {
		return status.StatusMsg
	}

	// Should it instead indicate error?
	return ""
}

// GetSummary implements status.ExportsComponentsStatusApi.
func (s *StatusImpl) GetSummary(status_id uint64) string {
	panic("unimplemented")
}

// PostStatus implements status.ExportsComponentsStatusApi.
func (s *StatusImpl) PostStatus(account_id uint64, statusMsg string) uint64 {
	std.Init(std.Packages{Os: true, NetHttp: true})

	// Make sure the status is not empty and also account_id is valid
	if account_id == 0 || strings.TrimSpace(statusMsg) == "" {
		status.WasiLoggingLoggingLog(status.WasiLoggingLoggingLevelCritical(), "xxx", "Please specify statusMsg and accountID MUSt be passed!!")
	}

	// Atomically increment the StatusID, assign it and return the value
	newStatusID := atomic.AddUint64(&runningStatusID, 1)
	s.Statuses[newStatusID] = StatusDetails{
		ID:        newStatusID,
		AccountID: account_id,
		StatusMsg: statusMsg,
		PostedAt:  time.Now(),
	}

	// TODO: Kick off summarize and kick off the rpc call to the account owner; who propogate to followers of it ..
	// EventID: StatusMsgPosted --> AccountID
	return newStatusID
}

// func (s *StatusImpl) GetSummary(statusID uint64) string {
// 	return "Summary for status ID " + strconv.FormatUint(statusID, 10)
// }

func main() {}
