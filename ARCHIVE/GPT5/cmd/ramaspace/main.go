package main

import (
    "context"
    "encoding/binary"
    "fmt"
    "log"
    "time"

    "github.com/cockroachdb/pebble"
    "github.com/cockroachdb/pebble/vfs"
    "go.temporal.io/sdk/client"
    "go.temporal.io/sdk/worker"

    "ramaspace/internal/store"
    "ramaspace/internal/temporal"
)

func openPebbleInMem() *pebble.DB {
    merger := &pebble.Merger{
        Name: "int64-add",
        Merge: func(key, value []byte) (pebble.ValueMerger, error) {
            type vm struct{ sum int64 }
            v := &vm{}; if len(value)==8 { v.sum = int64(binary.BigEndian.Uint64(value)) }
            return pebble.NewValueMerger(
                func(oldV []byte) error { if len(oldV)==8 { v.sum += int64(binary.BigEndian.Uint64(oldV)) }; return nil },
                func(newV []byte) error { if len(newV)==8 { v.sum += int64(binary.BigEndian.Uint64(newV)) }; return nil },
                func(includesBase bool) ([]byte, func() error, error) { var b [8]byte; binary.BigEndian.PutUint64(b[:], uint64(v.sum)); return b[:], func() error { return nil }, nil },
            ), nil
        },
    }
    db, _ := pebble.Open("", &pebble.Options{FS: vfs.NewMem(), Merger: merger})
    return db
}

func main(){
    ctx := context.Background()
    tc, err := client.Dial(client.Options{Namespace: "default"})
    if err!=nil { log.Fatalf("temporal: %v", err) }
    defer tc.Close()

    var st store.Store = store.NewPebble(openPebbleInMem())

    w := worker.New(tc, "rs-taskq", worker.Options{})
    w.RegisterWorkflow(temporal.PartitionWorkflowV2)
    acts := &temporal.Activities{ Store: st, Seq: func() uint64 { return uint64(time.Now().UnixNano()) }, Partitions: 64 }
    w.RegisterActivity(acts)
    go func(){ if err:= w.Run(worker.InterruptCh()); err!=nil { log.Fatal(err) } }()

    orch := temporal.Orchestrator{ Client: tc, TaskQ: "rs-taskq", Name: "demo", Parts: 64 }
    _ = orch.Update(ctx, "alice", "PutProfile", temporal.ReqUser{RequestID:"r1", U:"alice", P: store.Profile{DisplayName:"Alice", PwdHash: 7}})
    _ = orch.Update(ctx, "alice", "IncViews", temporal.ReqViews{RequestID:"v1", U:"alice", Hour:20251109, Delta:1})
    fmt.Println("sent a couple of updates; run tests for more")
}
