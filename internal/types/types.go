package types

import "time"

type UserID = string

type Profile struct {
	UserID           UserID
	Email            string
	DisplayName      string
	ProfilePic       string
	Bio              string
	Location         string
	PwdHash          int
	JoinedAtMillis   int64
	RegistrationUUID string
}

type PostID = int64

type Post struct {
	PostID    PostID
	FromUser  string
	ToUser    string
	Content   string
	CreatedAt time.Time
}

type ResolvedPost struct {
	UserID
	Content    string
	Display    string
	ProfilePic string
	PostID
	CreatedAt time.Time
}
