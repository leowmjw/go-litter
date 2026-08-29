package temporal

import (
    "context"
    "encoding/json"

    "github.com/cockroachdb/pebble"
    "ramaspace/internal/ops"
    "ramaspace/internal/store"
)

type Activities struct{ Store store.Store; Seq func() uint64; Partitions int }

func (a *Activities) partOf(u string) int { return partOf(u, a.Partitions) }

func (a *Activities) PutProfile(ctx context.Context, r ReqUser) error   { return a.Store.PutProfile(r.U, r.P) }
func (a *Activities) Befriend(ctx context.Context, r ReqFriend) error   { return a.Store.FriendsAddBidirectional(r.A, r.B) }
func (a *Activities) Unfriend(ctx context.Context, r ReqFriend) error   { return a.Store.FriendsRemoveBidirectional(r.A, r.B) }
func (a *Activities) AddOutReq(ctx context.Context, r ReqFriendReq) error { return a.Store.OutgoingAdd(r.From, r.To) }
func (a *Activities) AddInReq(ctx context.Context, r ReqFriendReq) error  { return a.Store.IncomingAdd(r.From, r.To) }
func (a *Activities) RemOutReq(ctx context.Context, r ReqFriendReq) error { return a.Store.OutgoingRemove(r.From, r.To) }
func (a *Activities) RemInReq(ctx context.Context, r ReqFriendReq) error  { return a.Store.IncomingRemove(r.From, r.To) }

func (a *Activities) IncViews(ctx context.Context, r ReqViews) error {
    agg := ops.NewAggregator(a.Store)
    if s,ok := a.Store.(*store.PebbleStore); ok {
        // Atomic: bucket + aggregate + feed in one batch
        return ops.ApplyAndAppendPebble(s.DB, a.partOf(r.U),
            []ops.FeedRecord{{Seq:a.Seq(),Kind:"views:add",Key:r.U,Body: mustJSON(struct{Hour,Delta int64}{r.Hour,r.Delta})}},
            func(b *pebble.Batch) error {
                if err:= b.Merge(store.KViews(r.U,r.Hour), store.BeI64(r.Delta), nil); err!=nil { return err }
                if err:= b.Merge(store.KViewsTotal(r.U),   store.BeI64(r.Delta), nil); err!=nil { return err }
                return nil
            })
    }
    return agg.AddProfileView(r.U, r.Hour, r.Delta)
}

func (a *Activities) PutPost(ctx context.Context, r ReqPost) error {
    id, err := a.Store.NextPostID(r.To); if err!=nil { return err }
    if s,ok := a.Store.(*store.PebbleStore); ok {
        return ops.ApplyAndAppendPebble(s.DB, a.partOf(r.To),
            []ops.FeedRecord{{Seq:a.Seq(),Kind:"post:add",Key:r.To,Body: mustJSON(r)}},
            func(b *pebble.Batch) error {
                if err := b.Set(store.KPost(r.To,id), []byte(r.P.UserID+"\x00"+r.P.ToUserID+"\x00"+r.P.Content), nil); err!=nil { return err }
                if err := b.Merge(store.KPostsTotal(r.To), store.BeI64(+1), nil); err!=nil { return err }
                return nil
            })
    }
    return a.Store.PutPost(r.To, id, r.P)
}

func mustJSON(v any) []byte { b,_ := json.Marshal(v); return b }
