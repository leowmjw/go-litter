package temporal

import (
    "crypto/sha1"
    "time"

    "go.temporal.io/sdk/workflow"
    "ramaspace/internal/store"
)

type ReqUser struct{ RequestID, U string; P store.Profile }
type ReqFriend struct{ RequestID, A, B string }
type ReqFriendReq struct{ RequestID, From, To string }
type ReqViews struct{ RequestID, U string; Hour, Delta int64 }
type ReqPost struct{ RequestID string; To string; P store.Post }

type wfState struct {
    OpsSinceRotate int
    Dedup map[string]struct{}
}

const (
    rotateEveryOps = 10000
    rotateEvery    = 20 * time.Minute
)

func partOf(u string, n int) int { h:=sha1.Sum([]byte(u)); return int(h[0]) % n }

func PartitionWorkflowV2(ctx workflow.Context, st wfState) error {
    if st.Dedup == nil { st.Dedup = map[string]struct{}{} }
    start := workflow.Now(ctx)
    ops := st.OpsSinceRotate
    bump := func(){ ops++ }
    rotateIfNeeded := func() error {
        if ops >= rotateEveryOps || workflow.Now(ctx).Sub(start) >= rotateEvery {
            return workflow.NewContinueAsNewError(ctx, PartitionWorkflowV2, wfState{OpsSinceRotate:0})
        }
        return nil
    }

    ao := workflow.ActivityOptions{ StartToCloseTimeout: 30 * time.Second }
    ctx = workflow.WithActivityOptions(ctx, ao)

    dedup := func(id string) bool { _,ok := st.Dedup[id]; if ok { return false }; st.Dedup[id]=struct{}{}; return true }

    workflow.SetUpdateHandler(ctx, "PutProfile", func(ctx workflow.Context, r ReqUser) (string, error) {
        if !dedup(r.RequestID) { return "dup", nil }
        bump()
        err := workflow.ExecuteActivity(ctx, (*Activities).PutProfile, r).Get(ctx, nil)
        return "ok", err
    })
    workflow.SetUpdateHandler(ctx, "Befriend", func(ctx workflow.Context, r ReqFriend) (string, error) {
        if !dedup(r.RequestID) { return "dup", nil }
        bump()
        err := workflow.ExecuteActivity(ctx, (*Activities).Befriend, r).Get(ctx, nil)
        return "ok", err
    })
    workflow.SetUpdateHandler(ctx, "Unfriend", func(ctx workflow.Context, r ReqFriend) (string, error) {
        if !dedup(r.RequestID) { return "dup", nil }
        bump()
        err := workflow.ExecuteActivity(ctx, (*Activities).Unfriend, r).Get(ctx, nil)
        return "ok", err
    })
    workflow.SetUpdateHandler(ctx, "ReqFriend", func(ctx workflow.Context, r ReqFriendReq) (string, error) {
        if !dedup(r.RequestID) { return "dup", nil }
        bump()
        if err := workflow.ExecuteActivity(ctx, (*Activities).AddOutReq, r).Get(ctx, nil); err!=nil { return "", err }
        if err := workflow.ExecuteActivity(ctx, (*Activities).AddInReq,  r).Get(ctx, nil); err!=nil { return "", err }
        return "ok", nil
    })
    workflow.SetUpdateHandler(ctx, "CancelFriend", func(ctx workflow.Context, r ReqFriendReq) (string, error) {
        if !dedup(r.RequestID) { return "dup", nil }
        bump()
        if err := workflow.ExecuteActivity(ctx, (*Activities).RemOutReq, r).Get(ctx, nil); err!=nil { return "", err }
        if err := workflow.ExecuteActivity(ctx, (*Activities).RemInReq,  r).Get(ctx, nil); err!=nil { return "", err }
        return "ok", nil
    })
    workflow.SetUpdateHandler(ctx, "IncViews", func(ctx workflow.Context, r ReqViews) (string, error) {
        if !dedup(r.RequestID) { return "dup", nil }
        bump()
        err := workflow.ExecuteActivity(ctx, (*Activities).IncViews, r).Get(ctx, nil)
        return "ok", err
    })
    workflow.SetUpdateHandler(ctx, "PutPost", func(ctx workflow.Context, r ReqPost) (string, error) {
        if !dedup(r.RequestID) { return "dup", nil }
        bump()
        err := workflow.ExecuteActivity(ctx, (*Activities).PutPost, r).Get(ctx, nil)
        return "ok", err
    })

    for {
        _ = workflow.Sleep(ctx, 5*time.Second)
        if err := rotateIfNeeded(); err!=nil { return err }
    }
}
