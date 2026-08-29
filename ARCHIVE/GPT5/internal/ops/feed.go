package ops

import (
    "encoding/json"
    "ramaspace/internal/store"
    "github.com/cockroachdb/pebble"
)

type FeedRecord struct { Seq uint64 `json:"seq"`; Kind string `json:"kind"`; Key string `json:"key"`; Body json.RawMessage `json:"body,omitempty"` }

func ApplyAndAppendPebble(db *pebble.DB, part int, recs []FeedRecord, mutate func(b *pebble.Batch) error) error {
    b := db.NewBatch(); defer b.Close()
    if mutate!=nil { if err:=mutate(b); err!=nil { return err } }
    for _,r := range recs { payload,_ := json.Marshal(r); if err:=b.Set(store.FeedKey(part,r.Seq), payload, nil); err!=nil { return err } }
    return b.Commit(pebble.Sync)
}

func TailPebble(db *pebble.DB, part int, lastSeq uint64, handle func(FeedRecord)) (uint64,error) {
    pfx := store.FeedPrefix(part); lo := store.FeedKey(part,lastSeq+1); ub := store.NextPrefix(pfx)
    snap := db.NewSnapshot(); defer snap.Close()
    it,err := snap.NewIter(&pebble.IterOptions{LowerBound: lo, UpperBound: ub}); if err!=nil { return lastSeq, err }
    defer it.Close()
    for ok:=it.First(); ok; ok=it.Next(){ var rec FeedRecord; if err:=json.Unmarshal(it.Value(),&rec); err==nil { handle(rec); if rec.Seq>lastSeq { lastSeq = rec.Seq } } }
    return lastSeq, nil
}
