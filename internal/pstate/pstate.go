package pstate

import "golitter/internal/types"

//
//
//Multiple PStates are needed to support all these tasks, although some tasks are supported by the same PState.
// As you gain experience using Rama, mapping the set of queries you need to support your application to a collection
// of PStates becomes second nature.
// To support the tasks for this application, we’ll build the following PStates:

//$$profiles
//
//{userId<String>:
//{"displayName":      <String>,
//"email":            <String>,
//"profilePic":       <String>,
//"bio":              <String>,
//"location":         <String>,
//"pwdHash":          <Integer>,
//"joinedAtMillis":   <Long>,
//"registrationUUID": <String>
//}}

// Profiles PState
type Profiles struct {
	Profiles map[types.UserID]Profile // Index by user ID
}

type Profile struct {
	DisplayName      string
	Email            string
	ProfilePic       string
	Bio              string
	Location         string
	PwdHash          int
	JoinedAtMillis   int64
	RegistrationUUID string
}

//
//$$outgoingFriendRequests
//
//{userId<String>: Set<userId<String>>}
//

type OutgoingFriendRequests struct {
	OutgoingFriendRequests map[types.UserID]map[types.UserID]bool
}

//$$incomingFriendRequests
//
//{userId<String>: Set<userId<String>>}

type IncomingFriendRequests struct {
	IncomingFriendRequests map[types.UserID]map[types.UserID]bool
}

//
//$$friends
//
//{userId<String>: Set<userId<String>>}

type Friends struct {
	Friends map[types.UserID]map[types.UserID]bool
}

//
//$$posts
//
//{userId<String>: {postId<Long>: <Post>}}

type Posts struct {
	Posts map[types.UserID]map[types.PostID]types.Post
}

// $$postId
//
// <Long>

//$$profileViews
//
//{userId<String>: {hourBucket<Long>: count<Long>}}
//

type HourBucket int
type ProfileViews struct {
	ProfileViews map[types.UserID]map[HourBucket]int64
}
