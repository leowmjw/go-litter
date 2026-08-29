package ramaspace_test

import (
    "testing"
    "ramaspace/internal/store"
    "ramaspace/internal/resolve"
    "github.com/stretchr/testify/require"
)

func TestMemBasics(t *testing.T){
    st := store.NewMem()
    require.NoError(t, st.PutProfile("alice", store.Profile{DisplayName:"A", PwdHash:7}))
    require.NoError(t, st.FriendsAddBidirectional("alice","bob"))
    cnt,_ := st.FriendsCount("alice"); require.Equal(t,int64(1),cnt)
    id1,_ := st.NextPostID("alice"); _ = st.PutPost("alice", id1, store.Post{UserID:"bob", ToUserID:"alice", Content:"hi"})
    res,err := resolve.ResolvePosts(st, "alice", 1<<62, 10); require.NoError(t,err); require.Equal(t,1,len(res))
}
