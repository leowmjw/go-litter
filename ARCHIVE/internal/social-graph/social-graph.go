package social_graph

// Social Network module ...
// With major packages: users, profiles, friendship requests, and friendships.
//
// Here are the tasks we’ll support:
//
//Users
//
//    Register a new user with a unique user ID, email, and display name
//
//    Update a profile field
//
//    Fetch password hash for a user (for login)
//
//    Post a comment on any user’s wall
//
//    View posts on a user’s wall (paginated)
//
//    View number of posts on a user’s wall
//

// Bounded Context: User, Profile
// profile implementation type .. for the tasks ..

// Friendships
//
//    Request friendship with another user
//
//    View friendship requests
//
//    Cancel friendship request
//
//    Accept friendship request
//
//    Check if two users are currently friends
//
//    View all friends for a user (paginated)
//
//    View number of friends for a user
//
//    Unfriend existing friend
//

type FriendshipImpl interface {
	AddFriendRequest(from, to string)
	CancelFriendRequest(from, to string)
	AcceptFriendRequest(from, to string) // adds both ways into friends
	Unfriend(a, b string)
	GetIncomingRequests(user string, start string, limit int) []string
	GetFriends(user string) []string
	IsFriends(a, b string) bool
	FriendsCount(user string) int
}

type Friendship struct{}

func (f Friendship) AddFriendRequest(from, to string) {
	//TODO implement me
	panic("implement me")
	// Upsert Set ..
	// Signal next state?
}

func (f Friendship) CancelFriendRequest(from, to string) {
	//TODO implement me
	panic("implement me")
}

func (f Friendship) AcceptFriendRequest(from, to string) {
	//TODO implement me
	panic("implement me")
}

func (f Friendship) Unfriend(a, b string) {
	//TODO implement me
	panic("implement me")
}

func (f Friendship) GetIncomingRequests(user string, start string, limit int) []string {
	//TODO implement me
	panic("implement me")
}

func (f Friendship) GetFriends(user string) []string {
	//TODO implement me
	panic("implement me")
}

func (f Friendship) IsFriends(a, b string) bool {
	//TODO implement me
	panic("implement me")
}

func (f Friendship) FriendsCount(user string) int {
	//TODO implement me
	panic("implement me")
}

//Analytics
//
//    Query for number of profile views for a user over a range of hours
