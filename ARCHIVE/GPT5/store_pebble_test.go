package ramaspace_test

import (
    "testing"
    "ramaspace/internal/store"
    "ramaspace/internal/resolve"
    "github.com/cockroachdb/pebble"
    "github.com/cockroachdb/pebble/vfs"
    "github.com/stretchr/testify/require"
    "encoding/binary"
)

func openDB(t *testing.T) *pebble.DB {
    t.Helper()
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
    db, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem(), Merger: merger})
    require.NoError(t, err)
    return db
}

func TestPebbleBasics(t *testing.T){
    db := openDB(t); defer db.Close()
    st := store.NewPebble(db)
    require.NoError(t, st.PutProfile("alice", store.Profile{DisplayName:"A", PwdHash:7}))
    require.NoError(t, st.FriendsAddBidirectional("alice","bob"))
    cnt,_ := st.FriendsCount("alice"); require.Equal(t,int64(1),cnt)
    id1,_ := st.NextPostID("alice"); _ = st.PutPost("alice", id1, store.Post{UserID:"bob", ToUserID:"alice", Content:"hi"})
    res,err := resolve.ResolvePosts(st, "alice", 1<<62, 10); require.NoError(t,err); require.Equal(t,1,len(res))
}
