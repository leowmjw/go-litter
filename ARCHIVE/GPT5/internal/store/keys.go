package store

import (
    "encoding/binary"
    "fmt"
)

func BeI64(v int64) []byte { var b [8]byte; binary.BigEndian.PutUint64(b[:], uint64(v)); return b[:] }
func RdI64(b []byte) int64 { if len(b)!=8 { return 0 }; return int64(binary.BigEndian.Uint64(b)) }

// Profile
func KProf(u, f string) []byte { return []byte(fmt.Sprintf("profiles/%s/field/%s", u, f)) }

// Friends
func KFriendsSet(u, v string) []byte { return []byte(fmt.Sprintf("friends/%s/set/%s", u, v)) }
func KFriendsSize(u string) []byte   { return []byte(fmt.Sprintf("friends/%s/size", u)) }
func PfxFriends(u string) []byte     { return []byte(fmt.Sprintf("friends/%s/set/", u)) }

// Requests
func KOut(u, v string) []byte  { return []byte(fmt.Sprintf("out/%s/set/%s", u, v)) }
func KIn(u, v string) []byte   { return []byte(fmt.Sprintf("in/%s/set/%s", v, u)) }
func PfxOut(u string) []byte   { return []byte(fmt.Sprintf("out/%s/set/", u)) }
func PfxIn(u string) []byte    { return []byte(fmt.Sprintf("in/%s/set/", u)) }

// Views
func PfxViews(u string) []byte   { return []byte(fmt.Sprintf("views/%s/map/", u)) }
func KViews(u string, hour int64) []byte {
    var be [8]byte; binary.BigEndian.PutUint64(be[:], uint64(hour))
    return append(PfxViews(u), be[:]...)
}

// Aggregates
func KViewsTotal(u string) []byte { return []byte("agg/views_total/"+u) }
func KPostsTotal(u string) []byte { return []byte("agg/posts_total/"+u) }
func PfxViewsTotal() []byte       { return []byte("agg/views_total/") }

// Posts
func PfxPosts(u string) []byte   { return []byte(fmt.Sprintf("posts/%s/map/", u)) }
func KPost(u string, id int64) []byte {
    var be [8]byte; binary.BigEndian.PutUint64(be[:], uint64(id))
    return append(PfxPosts(u), be[:]...)
}
func KPostNext(u string) []byte { return []byte(fmt.Sprintf("posts/%s/next", u)) }

// Metrics
func KMetricTotalUsers() []byte       { return []byte("metrics/totalUsers") }
func KMetricTotalFriendEdges() []byte { return []byte("metrics/totalFriendEdges") }

// Feed
func FeedKey(part int, seq uint64) []byte { return []byte(fmt.Sprintf("feed/%02d/%020d", part, seq)) }
func FeedPrefix(part int) []byte          { return []byte(fmt.Sprintf("feed/%02d/", part)) }

func NextPrefix(pfx []byte) []byte {
    end := append([]byte{}, pfx...)
    for i := len(end)-1; i >= 0; i-- {
        end[i]++
        if end[i]!=0 { return end[:i+1] }
    }
    return nil
}
