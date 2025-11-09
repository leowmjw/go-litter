package resolve

import "ramaspace/internal/store"

func ResolvePosts(st store.Store, wallOwner store.UserID, start int64, limit int) ([]store.ResolvedPost, error) {
    ids, posts, err := st.PostsRangeFrom(wallOwner, start, limit)
    if err != nil { return nil, err }
    out := make([]store.ResolvedPost, 0, len(ids))
    for i, p := range posts {
        prof, ok, err := st.GetProfileSubset(p.UserID)
        if err != nil { return nil, err }
        if !ok { prof = store.Profile{} }
        out = append(out, store.ResolvedPost{UserID:p.UserID, Content:p.Content, DisplayName:prof.DisplayName, ProfilePic:prof.ProfilePic, PostID: ids[i]})
    }
    return out, nil
}
