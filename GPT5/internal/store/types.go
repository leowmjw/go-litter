package store

type UserID = string

type Profile struct {
    Email       string
    DisplayName string
    Bio         string
    Location    string
    ProfilePic  string
    JoinedAtMs  int64
    PwdHash     int
}

type Post struct {
    UserID   string
    ToUserID string
    Content  string
}

type ResolvedPost struct {
    UserID      string
    Content     string
    DisplayName string
    ProfilePic  string
    PostID      int64
}

type Store interface {
    PutProfile(u UserID, p Profile) error
    GetPwdHash(u UserID) (int, bool, error)
    GetProfileSubset(u UserID) (Profile, bool, error)

    FriendsAddBidirectional(a, b UserID) error
    FriendsRemoveBidirectional(a, b UserID) error
    FriendsCount(u UserID) (int64, error)
    FriendsPage(u UserID, start string, limit int) ([]string, error)
    IsFriends(a, b UserID) (bool, error)

    OutgoingAdd(from, to UserID) error
    IncomingAdd(from, to UserID) error
    OutgoingRemove(from, to UserID) error
    IncomingRemove(from, to UserID) error
    GetOutgoing(u UserID, start string, limit int) ([]string, error)
    GetIncoming(u UserID, start string, limit int) ([]string, error)

    IncProfileViews(u UserID, hour int64, delta int64) error
    SumProfileViews(u UserID, startHour, endHour int64) (int64, error)

    NextPostID(u UserID) (int64, error)
    PutPost(u UserID, id int64, p Post) error
    PostsCount(u UserID) (int64, error)
    PostsRangeFrom(u UserID, start int64, limit int) ([]int64, []Post, error)

    TotalUsers() (int64, error)
    TotalFriendEdges() (int64, error)
    TopKByViews(k int) ([]UserID, []int64, error)
    TopKByFriendCount(k int) ([]UserID, []int64, error)
    PendingRequestCounts(u UserID) (outgoing, incoming int64, err error)
}
