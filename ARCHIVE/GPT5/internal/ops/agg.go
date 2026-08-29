package ops

import (
    "ramaspace/internal/store"
    "github.com/cockroachdb/pebble"
)

type Aggregator struct { Store store.Store }
func NewAggregator(s store.Store)*Aggregator{ return &Aggregator{Store:s} }

func (a *Aggregator) AddProfileView(u string, hour, delta int64) error {
    if err:= a.Store.IncProfileViews(u,hour,delta); err!=nil { return err }
    switch s := a.Store.(type) {
    case *store.PebbleStore: return s.DB.Merge(store.KViewsTotal(u), store.BeI64(delta), pebble.Sync)
    default: return nil
    }
}

func (a *Aggregator) TotalViews(u string) (int64,error) {
    switch s := a.Store.(type) {
    case *store.PebbleStore:
        v,c,err := s.DB.Get(store.KViewsTotal(u)); if err==pebble.ErrNotFound { return 0,nil }; if err!=nil { return 0,err }; defer c.Close(); return store.RdI64(v), nil
    default:
        return a.Store.SumProfileViews(u, -1<<62, 1<<62)
    }
}
