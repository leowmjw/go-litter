package ramaspace_test

import (
    "sync"
    "testing"
    "ramaspace/internal/store"
    "github.com/stretchr/testify/require"
)

func TestTinyLoadMemViews(t *testing.T){
    st := store.NewMem()
    const N=200
    wg:=sync.WaitGroup{}; wg.Add(N)
    for i:=0;i<N;i++{ go func(){ defer wg.Done(); _ = st.IncProfileViews("alice", 20251109, 1) }() }
    wg.Wait()
    sum,_ := st.SumProfileViews("alice",20251109,20251109); require.Equal(t,int64(N),sum)
}
