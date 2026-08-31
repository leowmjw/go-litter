// Package ramaspace implements the Tutorial 6 capstone: a small social-network
// application (RamaSpace) built on the shared stream/microbatch runtime.
package ramaspace

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"app/internal/microbatch"
	"app/internal/partition"
	"app/internal/storage"
	"app/internal/stream"
)

const (
	ModuleName storage.ModuleID = "ramaspace"

	UserRegistrationsDepot storage.DepotID = "user-registrations"
	ProfileEditsDepot      storage.DepotID = "profile-edits"
	ProfileViewsDepot      storage.DepotID = "profile-views"
	FriendRequestsDepot    storage.DepotID = "friend-requests"
	FriendshipChangesDepot storage.DepotID = "friendship-changes"
	PostsDepot             storage.DepotID = "posts"

	UsersTopology        storage.TopologyID = "users"
	FriendsTopology      storage.TopologyID = "friends"
	PostsTopology        storage.TopologyID = "posts"
	ProfileViewsTopology storage.TopologyID = "profile-views"

	ProfilesState         storage.StateID = "profiles"
	OutgoingRequestsState storage.StateID = "outgoing-requests"
	IncomingRequestsState storage.StateID = "incoming-requests"
	FriendsState          storage.StateID = "friends"
	FriendCountState      storage.StateID = "friend-count"
	PostsState            storage.StateID = "posts"
	PostIDState           storage.StateID = "post-id"
	PostCountState        storage.StateID = "post-count"
	ProfileViewsState     storage.StateID = "profile-views"

	FieldDisplayName    = "displayName"
	FieldBio            = "bio"
	FieldLocation       = "location"
	FieldProfilePicture = "profilePicture"
	FieldEmail          = "email"

	requestEventType = "request"
	cancelEventType  = "cancel"
	addEventType     = "add"
	removeEventType  = "remove"

	SchemaVersion1 = 1
	SchemaVersion2 = 2

	MaxPageSize = 20
)

var (
	ErrInvalidRegistration  = errors.New("user id, email, display name, password hash, and registration uuid are required")
	ErrInvalidEdit          = errors.New("user id, field, and value are required")
	ErrUnknownField         = errors.New("unknown profile field")
	ErrUserNotFound         = errors.New("user not found")
	ErrInvalidFriendRequest = errors.New("user id and destination user id are required and must differ")
	ErrInvalidFriendship    = errors.New("both user ids are required and must differ")
	ErrInvalidPost          = errors.New("author, destination, and non-empty content are required")
	ErrInvalidProfileView   = errors.New("user id and positive timestamp are required")
	ErrInvalidPageSize      = errors.New("limit must be between 1 and the maximum page size")
	ErrInvalidRange         = errors.New("range start must not be after range end")
	ErrIncompatibleConfig   = errors.New("stored ramaspace module configuration is incompatible")
)

// UserRegistration is the userRegistrations depot event.
type UserRegistration struct {
	UserID           string `json:"userId"`
	Email            string `json:"email"`
	DisplayName      string `json:"displayName"`
	PasswordHash     string `json:"passwordHash"`
	RegistrationUUID string `json:"registrationUuid"`
}

// ProfileRecord is the full internal profile record, including secrets.
type ProfileRecord struct {
	SchemaVersion    int    `json:"schemaVersion"`
	UserID           string `json:"userId"`
	Email            string `json:"email"`
	DisplayName      string `json:"displayName"`
	PasswordHash     string `json:"passwordHash"`
	Bio              string `json:"bio"`
	Location         string `json:"location"`
	ProfilePicture   string `json:"profilePicture"`
	Pronouns         string `json:"pronouns,omitempty"`
	JoinedAtMillis   int64  `json:"joinedAtMillis"`
	RegistrationUUID string `json:"registrationUuid"`
}

// Profile is the public query result for a profile (no password hash).
type Profile struct {
	UserID         string `json:"userId"`
	DisplayName    string `json:"displayName"`
	Bio            string `json:"bio"`
	Location       string `json:"location"`
	ProfilePicture string `json:"profilePicture"`
	Pronouns       string `json:"pronouns,omitempty"`
	JoinedAtMillis int64  `json:"joinedAtMillis"`
}

// ProfileEdit is the profileEdits depot event.
type ProfileEdit struct {
	UserID string `json:"userId"`
	Field  string `json:"field"`
	Value  string `json:"value"`
}

type friendRequestEvent struct {
	Type       string `json:"type"`
	UserID     string `json:"userId"`
	DestUserID string `json:"destUserId"`
}

type friendshipChangeEvent struct {
	Type  string `json:"type"`
	UserA string `json:"userA"`
	UserB string `json:"userB"`
}

// Post is the posts depot event.
type Post struct {
	Author      string `json:"author"`
	Destination string `json:"destination"`
	Content     string `json:"content"`
}

// StoredPost is a post as materialized on a wall partition.
type StoredPost struct {
	PostID          uint64 `json:"postId"`
	Author          string `json:"author"`
	Content         string `json:"content"`
	CreatedAtMillis int64  `json:"createdAtMillis"`
}

// ResolvedPost is a wall post with its author's public profile data attached.
type ResolvedPost struct {
	PostID               uint64 `json:"postId"`
	Author               string `json:"author"`
	AuthorDisplayName    string `json:"authorDisplayName"`
	AuthorProfilePicture string `json:"authorProfilePicture"`
	Content              string `json:"content"`
	CreatedAtMillis      int64  `json:"createdAtMillis"`
}

// PostPage is a page of resolved posts plus a cursor for the next page.
type PostPage struct {
	Posts      []ResolvedPost `json:"posts"`
	NextCursor uint64         `json:"nextCursor"`
	HasMore    bool           `json:"hasMore"`
}

// RequestPage is a page of pending friend-request user IDs.
type RequestPage struct {
	UserIDs    []string `json:"userIds"`
	NextCursor string   `json:"nextCursor"`
	HasMore    bool     `json:"hasMore"`
}

// FriendPage is a page of friend user IDs.
type FriendPage struct {
	UserIDs    []string `json:"userIds"`
	NextCursor string   `json:"nextCursor"`
	HasMore    bool     `json:"hasMore"`
}

// ProfileView is the profileViews depot event.
type ProfileView struct {
	UserID          string `json:"userId"`
	TimestampMillis int64  `json:"timestampMillis"`
}

type moduleMetadata struct {
	StorageVersion int    `json:"storageVersion"`
	TaskCount      uint32 `json:"taskCount"`
}

// Module is the RamaSpace capstone API surface.
type Module struct {
	RegisterUser         func(context.Context, UserRegistration) (bool, error)
	EditProfile          func(context.Context, ProfileEdit) error
	GetPasswordHash      func(context.Context, string) (string, bool, error)
	GetProfile           func(context.Context, string) (Profile, bool, error)
	FriendRequest        func(context.Context, string, string) error
	CancelFriendRequest  func(context.Context, string, string) error
	AcceptFriendRequest  func(context.Context, string, string) error
	RemoveFriendship     func(context.Context, string, string) error
	IsFriend             func(context.Context, string, string) (bool, error)
	FriendCount          func(context.Context, string) (uint64, error)
	ListFriends          func(context.Context, string, string, int) (FriendPage, error)
	ListOutgoingRequests func(context.Context, string, string, int) (RequestPage, error)
	ListIncomingRequests func(context.Context, string, string, int) (RequestPage, error)
	Post                 func(context.Context, Post) error
	PostCount            func(context.Context, string) (uint64, error)
	ResolvePosts         func(context.Context, string, uint64, int) (PostPage, error)
	RecordProfileView    func(context.Context, ProfileView) error
	ProfileViewCount     func(context.Context, string, int64, int64) (uint64, error)
	Advance              func(context.Context) (int, error)
	Replay               func(context.Context) error
	Rebuild              func(context.Context) error
	MigrateProfiles      func(context.Context) (int, error)
}

// New builds a RamaSpace module at the given schema version (SchemaVersion1 or
// SchemaVersion2). Both versions share storage and can run against the same
// store concurrently to exercise rollout coexistence; SchemaVersion2 also
// populates the Pronouns field on newly registered profiles.
func New(store *storage.Store, taskCount uint32, schemaVersion int, now func() time.Time) (*Module, error) {
	if schemaVersion != SchemaVersion1 && schemaVersion != SchemaVersion2 {
		return nil, fmt.Errorf("unsupported schema version %d", schemaVersion)
	}
	if now == nil {
		now = time.Now
	}
	choosePartition, err := partition.New(taskCount)
	if err != nil {
		return nil, err
	}

	expectedMetadata := moduleMetadata{StorageVersion: SchemaVersion2, TaskCount: taskCount}
	storedMetadata, found, err := store.GetMetadata(context.Background(), ModuleName, []byte("config"))
	if err != nil {
		return nil, err
	}
	if found {
		var metadata moduleMetadata
		if err := json.Unmarshal(storedMetadata, &metadata); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrIncompatibleConfig, err)
		}
		if metadata != expectedMetadata {
			return nil, fmt.Errorf("%w: stored=%+v requested=%+v", ErrIncompatibleConfig, metadata, expectedMetadata)
		}
	} else {
		encoded, err := json.Marshal(expectedMetadata)
		if err != nil {
			return nil, err
		}
		if err := store.SetMetadata(context.Background(), ModuleName, []byte("config"), encoded); err != nil {
			return nil, err
		}
	}

	state := func(name storage.StateID, task uint32) storage.StatePartition {
		return storage.StatePartition{Module: ModuleName, State: name, Partition: task}
	}
	statePartitionFor := func(name storage.StateID, key string) storage.StatePartition {
		return state(name, choosePartition([]byte(key)))
	}

	registrationHandler := func(ctx context.Context, event *stream.Event, record storage.Record, task uint32) ([]byte, error) {
		var reg UserRegistration
		if err := json.Unmarshal(record.Payload, &reg); err != nil {
			return nil, err
		}
		key := []byte(reg.UserID)
		existingBytes, found, err := event.Get(ctx, state(ProfilesState, task), key)
		if err != nil {
			return nil, err
		}
		if found {
			var existing ProfileRecord
			if err := json.Unmarshal(existingBytes, &existing); err != nil {
				return nil, err
			}
			if existing.RegistrationUUID == reg.RegistrationUUID {
				return []byte{1}, nil
			}
			return []byte{0}, nil
		}
		profile := ProfileRecord{
			SchemaVersion:    schemaVersion,
			UserID:           reg.UserID,
			Email:            reg.Email,
			DisplayName:      reg.DisplayName,
			PasswordHash:     reg.PasswordHash,
			JoinedAtMillis:   now().UnixMilli(),
			RegistrationUUID: reg.RegistrationUUID,
		}
		if schemaVersion >= SchemaVersion2 {
			profile.Pronouns = "unspecified"
		}
		encoded, err := json.Marshal(profile)
		if err != nil {
			return nil, err
		}
		event.Set(state(ProfilesState, task), key, encoded)
		return []byte{1}, nil
	}

	editHandler := func(ctx context.Context, event *stream.Event, record storage.Record, task uint32) ([]byte, error) {
		var edit ProfileEdit
		if err := json.Unmarshal(record.Payload, &edit); err != nil {
			return nil, err
		}
		key := []byte(edit.UserID)
		existingBytes, found, err := event.Get(ctx, state(ProfilesState, task), key)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, ErrUserNotFound
		}
		var profile ProfileRecord
		if err := json.Unmarshal(existingBytes, &profile); err != nil {
			return nil, err
		}
		switch edit.Field {
		case FieldDisplayName:
			profile.DisplayName = edit.Value
		case FieldBio:
			profile.Bio = edit.Value
		case FieldLocation:
			profile.Location = edit.Value
		case FieldProfilePicture:
			profile.ProfilePicture = edit.Value
		case FieldEmail:
			profile.Email = edit.Value
		default:
			return nil, ErrUnknownField
		}
		encoded, err := json.Marshal(profile)
		if err != nil {
			return nil, err
		}
		event.Set(state(ProfilesState, task), key, encoded)
		return nil, nil
	}

	adjustFriendCount := func(ctx context.Context, event *stream.Event, user string, delta int64) error {
		partitionState := statePartitionFor(FriendCountState, user)
		raw, _, err := event.Get(ctx, partitionState, []byte(user))
		if err != nil {
			return err
		}
		count := int64(decodeUint64(raw)) + delta
		if count < 0 {
			count = 0
		}
		event.Set(partitionState, []byte(user), partition.Uint64Key(uint64(count)))
		return nil
	}

	friendRequestHandler := func(ctx context.Context, event *stream.Event, record storage.Record, task uint32) ([]byte, error) {
		var ev friendRequestEvent
		if err := json.Unmarshal(record.Payload, &ev); err != nil {
			return nil, err
		}
		switch ev.Type {
		case requestEventType:
			event.Set(statePartitionFor(OutgoingRequestsState, ev.UserID), compositeKey(ev.UserID, ev.DestUserID), []byte{1})
			event.Set(statePartitionFor(IncomingRequestsState, ev.DestUserID), compositeKey(ev.DestUserID, ev.UserID), []byte{1})
		case cancelEventType:
			event.Delete(statePartitionFor(OutgoingRequestsState, ev.UserID), compositeKey(ev.UserID, ev.DestUserID))
			event.Delete(statePartitionFor(IncomingRequestsState, ev.DestUserID), compositeKey(ev.DestUserID, ev.UserID))
		default:
			return nil, fmt.Errorf("unknown friend request event type %q", ev.Type)
		}
		return nil, nil
	}

	friendshipChangeHandler := func(ctx context.Context, event *stream.Event, record storage.Record, task uint32) ([]byte, error) {
		var ev friendshipChangeEvent
		if err := json.Unmarshal(record.Payload, &ev); err != nil {
			return nil, err
		}
		clearPending := func() {
			event.Delete(statePartitionFor(OutgoingRequestsState, ev.UserA), compositeKey(ev.UserA, ev.UserB))
			event.Delete(statePartitionFor(IncomingRequestsState, ev.UserB), compositeKey(ev.UserB, ev.UserA))
			event.Delete(statePartitionFor(OutgoingRequestsState, ev.UserB), compositeKey(ev.UserB, ev.UserA))
			event.Delete(statePartitionFor(IncomingRequestsState, ev.UserA), compositeKey(ev.UserA, ev.UserB))
		}
		switch ev.Type {
		case addEventType:
			clearPending()
			_, alreadyFriends, err := event.Get(ctx, statePartitionFor(FriendsState, ev.UserA), compositeKey(ev.UserA, ev.UserB))
			if err != nil {
				return nil, err
			}
			event.Set(statePartitionFor(FriendsState, ev.UserA), compositeKey(ev.UserA, ev.UserB), []byte{1})
			event.Set(statePartitionFor(FriendsState, ev.UserB), compositeKey(ev.UserB, ev.UserA), []byte{1})
			if !alreadyFriends {
				if err := adjustFriendCount(ctx, event, ev.UserA, 1); err != nil {
					return nil, err
				}
				if err := adjustFriendCount(ctx, event, ev.UserB, 1); err != nil {
					return nil, err
				}
			}
		case removeEventType:
			_, wereFriends, err := event.Get(ctx, statePartitionFor(FriendsState, ev.UserA), compositeKey(ev.UserA, ev.UserB))
			if err != nil {
				return nil, err
			}
			event.Delete(statePartitionFor(FriendsState, ev.UserA), compositeKey(ev.UserA, ev.UserB))
			event.Delete(statePartitionFor(FriendsState, ev.UserB), compositeKey(ev.UserB, ev.UserA))
			if wereFriends {
				if err := adjustFriendCount(ctx, event, ev.UserA, -1); err != nil {
					return nil, err
				}
				if err := adjustFriendCount(ctx, event, ev.UserB, -1); err != nil {
					return nil, err
				}
			}
		default:
			return nil, fmt.Errorf("unknown friendship change event type %q", ev.Type)
		}
		return nil, nil
	}

	streamRuntime, err := stream.New(ModuleName, taskCount, choosePartition, store,
		stream.Source{
			Depot: UserRegistrationsDepot, Topology: UsersTopology,
			PartitionKey: func(payload []byte) ([]byte, error) {
				var reg UserRegistration
				if err := json.Unmarshal(payload, &reg); err != nil {
					return nil, err
				}
				return []byte(reg.UserID), nil
			},
			Handle: registrationHandler,
		},
		stream.Source{
			Depot: ProfileEditsDepot, Topology: UsersTopology,
			PartitionKey: func(payload []byte) ([]byte, error) {
				var edit ProfileEdit
				if err := json.Unmarshal(payload, &edit); err != nil {
					return nil, err
				}
				return []byte(edit.UserID), nil
			},
			Handle: editHandler,
		},
		stream.Source{
			Depot: FriendRequestsDepot, Topology: FriendsTopology,
			PartitionKey: func(payload []byte) ([]byte, error) {
				var ev friendRequestEvent
				if err := json.Unmarshal(payload, &ev); err != nil {
					return nil, err
				}
				return []byte(ev.UserID), nil
			},
			Handle: friendRequestHandler,
		},
		stream.Source{
			Depot: FriendshipChangesDepot, Topology: FriendsTopology,
			PartitionKey: func(payload []byte) ([]byte, error) {
				var ev friendshipChangeEvent
				if err := json.Unmarshal(payload, &ev); err != nil {
					return nil, err
				}
				return []byte(ev.UserA), nil
			},
			Handle: friendshipChangeHandler,
		},
	)
	if err != nil {
		return nil, err
	}

	postsHandler := func(ctx context.Context, event *microbatch.Event, items []microbatch.Item) error {
		nextByPartition := make(map[uint32]uint64)
		nextFor := func(task uint32) (uint64, error) {
			if next, ok := nextByPartition[task]; ok {
				return next, nil
			}
			raw, _, err := event.Get(ctx, state(PostIDState, task), []byte("next"))
			if err != nil {
				return 0, err
			}
			return decodeUint64(raw), nil
		}
		countByPartitionKey := make(map[string]uint64)
		countKeyFor := func(wall string) (storage.StatePartition, string) {
			partitionState := statePartitionFor(PostCountState, wall)
			return partitionState, wall
		}
		for _, item := range items {
			var p Post
			if err := json.Unmarshal(item.Record.Payload, &p); err != nil {
				return err
			}
			if p.Author == "" || p.Destination == "" || strings.TrimSpace(p.Content) == "" {
				return ErrInvalidPost
			}
			task := item.Partition
			next, err := nextFor(task)
			if err != nil {
				return err
			}
			next++
			nextByPartition[task] = next
			stored := StoredPost{PostID: next, Author: p.Author, Content: p.Content, CreatedAtMillis: now().UnixMilli()}
			encoded, err := json.Marshal(stored)
			if err != nil {
				return err
			}
			event.Set(state(PostsState, task), seqKey(p.Destination, next), encoded)
			countState, wall := countKeyFor(p.Destination)
			if _, ok := countByPartitionKey[wall]; !ok {
				raw, _, err := event.Get(ctx, countState, []byte(wall))
				if err != nil {
					return err
				}
				countByPartitionKey[wall] = decodeUint64(raw)
			}
			countByPartitionKey[wall]++
			event.Set(countState, []byte(wall), partition.Uint64Key(countByPartitionKey[wall]))
		}
		for task, next := range nextByPartition {
			event.Set(state(PostIDState, task), []byte("next"), partition.Uint64Key(next))
		}
		return nil
	}

	profileViewsHandler := func(ctx context.Context, event *microbatch.Event, items []microbatch.Item) error {
		type bucketKey struct {
			user   string
			bucket uint64
		}
		deltas := make(map[bucketKey]uint64)
		order := make([]bucketKey, 0)
		for _, item := range items {
			var view ProfileView
			if err := json.Unmarshal(item.Record.Payload, &view); err != nil {
				return err
			}
			if view.UserID == "" || view.TimestampMillis <= 0 {
				return ErrInvalidProfileView
			}
			bucket := uint64(view.TimestampMillis / int64(time.Hour/time.Millisecond))
			key := bucketKey{user: view.UserID, bucket: bucket}
			if _, ok := deltas[key]; !ok {
				order = append(order, key)
			}
			deltas[key]++
		}
		for _, key := range order {
			partitionState := statePartitionFor(ProfileViewsState, key.user)
			mapKey := seqKey(key.user, key.bucket)
			raw, _, err := event.Get(ctx, partitionState, mapKey)
			if err != nil {
				return err
			}
			event.Set(partitionState, mapKey, partition.Uint64Key(decodeUint64(raw)+deltas[key]))
		}
		return nil
	}

	microbatchRuntime, err := microbatch.New(ModuleName, taskCount, choosePartition, store,
		microbatch.Topology{
			Name: PostsTopology,
			Sources: []microbatch.Source{
				{
					Depot: PostsDepot,
					PartitionKey: func(payload []byte) ([]byte, error) {
						var p Post
						if err := json.Unmarshal(payload, &p); err != nil {
							return nil, err
						}
						return []byte(p.Destination), nil
					},
				},
			},
			Handle: postsHandler,
		},
		microbatch.Topology{
			Name: ProfileViewsTopology,
			Sources: []microbatch.Source{
				{
					Depot: ProfileViewsDepot,
					PartitionKey: func(payload []byte) ([]byte, error) {
						var view ProfileView
						if err := json.Unmarshal(payload, &view); err != nil {
							return nil, err
						}
						return []byte(view.UserID), nil
					},
				},
			},
			Handle: profileViewsHandler,
		},
	)
	if err != nil {
		return nil, err
	}

	module := &Module{}
	module.Replay = streamRuntime.Replay
	module.Rebuild = func(ctx context.Context) error {
		// Reset once: streamRuntime.Rebuild and microbatchRuntime.Rebuild would
		// each call store.ResetDerived independently, and the second reset would
		// wipe out what the first replay just repopulated since both runtimes
		// share the same module's derived-state namespace.
		if err := store.ResetDerived(ctx, ModuleName); err != nil {
			return err
		}
		if err := streamRuntime.Replay(ctx); err != nil {
			return err
		}
		_, err := microbatchRuntime.AdvanceAll(ctx)
		return err
	}
	module.Advance = microbatchRuntime.AdvanceAll

	module.RegisterUser = func(ctx context.Context, reg UserRegistration) (bool, error) {
		reg.UserID = strings.TrimSpace(reg.UserID)
		reg.RegistrationUUID = strings.TrimSpace(reg.RegistrationUUID)
		if reg.UserID == "" || reg.Email == "" || reg.DisplayName == "" || reg.PasswordHash == "" || reg.RegistrationUUID == "" {
			return false, ErrInvalidRegistration
		}
		payload, err := json.Marshal(reg)
		if err != nil {
			return false, err
		}
		ack, err := streamRuntime.Append(ctx, UserRegistrationsDepot, reg.RegistrationUUID, payload)
		if err != nil {
			return false, err
		}
		result := ack[UsersTopology]
		return len(result) == 1 && result[0] == 1, nil
	}
	module.EditProfile = func(ctx context.Context, edit ProfileEdit) error {
		edit.UserID = strings.TrimSpace(edit.UserID)
		if edit.UserID == "" || edit.Field == "" {
			return ErrInvalidEdit
		}
		switch edit.Field {
		case FieldDisplayName, FieldBio, FieldLocation, FieldProfilePicture, FieldEmail:
		default:
			return ErrUnknownField
		}
		payload, err := json.Marshal(edit)
		if err != nil {
			return err
		}
		_, err = streamRuntime.Append(ctx, ProfileEditsDepot, "", payload)
		return err
	}
	getProfileRecord := func(ctx context.Context, userID string) (ProfileRecord, bool, error) {
		partitionState := statePartitionFor(ProfilesState, userID)
		encoded, found, err := store.GetState(ctx, partitionState, []byte(userID))
		if err != nil || !found {
			return ProfileRecord{}, found, err
		}
		var record ProfileRecord
		if err := json.Unmarshal(encoded, &record); err != nil {
			return ProfileRecord{}, false, err
		}
		return record, true, nil
	}
	module.GetPasswordHash = func(ctx context.Context, userID string) (string, bool, error) {
		record, found, err := getProfileRecord(ctx, userID)
		if err != nil || !found {
			return "", found, err
		}
		return record.PasswordHash, true, nil
	}
	module.GetProfile = func(ctx context.Context, userID string) (Profile, bool, error) {
		record, found, err := getProfileRecord(ctx, userID)
		if err != nil || !found {
			return Profile{}, found, err
		}
		return Profile{
			UserID: record.UserID, DisplayName: record.DisplayName, Bio: record.Bio,
			Location: record.Location, ProfilePicture: record.ProfilePicture,
			Pronouns: record.Pronouns, JoinedAtMillis: record.JoinedAtMillis,
		}, true, nil
	}
	module.MigrateProfiles = func(ctx context.Context) (int, error) {
		migrated := 0
		for task := uint32(0); task < taskCount; task++ {
			partitionState := state(ProfilesState, task)
			entries, err := store.ScanState(ctx, partitionState, nil, nil, 1<<20, false)
			if err != nil {
				return migrated, err
			}
			for _, entry := range entries {
				var record ProfileRecord
				if err := json.Unmarshal(entry.Value, &record); err != nil {
					return migrated, err
				}
				if record.SchemaVersion >= SchemaVersion2 {
					continue
				}
				record.SchemaVersion = SchemaVersion2
				if record.Pronouns == "" {
					record.Pronouns = "unspecified"
				}
				encoded, err := json.Marshal(record)
				if err != nil {
					return migrated, err
				}
				if _, err := store.Commit(ctx, storage.CommitRequest{
					Module:    ModuleName,
					BatchID:   storage.BatchID(fmt.Sprintf("migrate-profile/%s", entry.Key)),
					Mutations: []storage.Mutation{{State: partitionState, Key: entry.Key, Value: encoded}},
				}); err != nil {
					return migrated, err
				}
				migrated++
			}
		}
		return migrated, nil
	}

	sendFriendRequestEvent := func(ctx context.Context, ev friendRequestEvent) error {
		payload, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		_, err = streamRuntime.Append(ctx, FriendRequestsDepot, "", payload)
		return err
	}
	sendFriendshipChangeEvent := func(ctx context.Context, ev friendshipChangeEvent) error {
		payload, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		_, err = streamRuntime.Append(ctx, FriendshipChangesDepot, "", payload)
		return err
	}
	validatePair := func(a, b string) error {
		if a == "" || b == "" || a == b {
			return ErrInvalidFriendRequest
		}
		return nil
	}
	module.FriendRequest = func(ctx context.Context, userID, destUserID string) error {
		if err := validatePair(userID, destUserID); err != nil {
			return err
		}
		return sendFriendRequestEvent(ctx, friendRequestEvent{Type: requestEventType, UserID: userID, DestUserID: destUserID})
	}
	module.CancelFriendRequest = func(ctx context.Context, userID, destUserID string) error {
		if err := validatePair(userID, destUserID); err != nil {
			return err
		}
		return sendFriendRequestEvent(ctx, friendRequestEvent{Type: cancelEventType, UserID: userID, DestUserID: destUserID})
	}
	module.AcceptFriendRequest = func(ctx context.Context, userID, destUserID string) error {
		if err := validatePair(userID, destUserID); err != nil {
			return ErrInvalidFriendship
		}
		return sendFriendshipChangeEvent(ctx, friendshipChangeEvent{Type: addEventType, UserA: userID, UserB: destUserID})
	}
	module.RemoveFriendship = func(ctx context.Context, userID, destUserID string) error {
		if err := validatePair(userID, destUserID); err != nil {
			return ErrInvalidFriendship
		}
		return sendFriendshipChangeEvent(ctx, friendshipChangeEvent{Type: removeEventType, UserA: userID, UserB: destUserID})
	}
	module.IsFriend = func(ctx context.Context, userID, otherUserID string) (bool, error) {
		_, found, err := store.GetState(ctx, statePartitionFor(FriendsState, userID), compositeKey(userID, otherUserID))
		return found, err
	}
	module.FriendCount = func(ctx context.Context, userID string) (uint64, error) {
		raw, found, err := store.GetState(ctx, statePartitionFor(FriendCountState, userID), []byte(userID))
		if err != nil || !found {
			return 0, err
		}
		return decodeUint64(raw), nil
	}

	scanMembers := func(ctx context.Context, stateID storage.StateID, userID, cursor string, limit int) ([]string, string, bool, error) {
		if limit <= 0 || limit > MaxPageSize {
			return nil, "", false, ErrInvalidPageSize
		}
		partitionState := statePartitionFor(stateID, userID)
		start := userPrefix(userID)
		if cursor != "" {
			start = append(compositeKey(userID, cursor), 0)
		}
		end := prefixUpperBound(userPrefix(userID))
		entries, err := store.ScanState(ctx, partitionState, start, end, limit+1, false)
		if err != nil {
			return nil, "", false, err
		}
		hasMore := len(entries) > limit
		if hasMore {
			entries = entries[:limit]
		}
		ids := make([]string, len(entries))
		for i, entry := range entries {
			ids[i] = string(entry.Key[len(userPrefix(userID)):])
		}
		next := ""
		if hasMore {
			next = ids[len(ids)-1]
		}
		return ids, next, hasMore, nil
	}
	module.ListFriends = func(ctx context.Context, userID, cursor string, limit int) (FriendPage, error) {
		ids, next, hasMore, err := scanMembers(ctx, FriendsState, userID, cursor, limit)
		if err != nil {
			return FriendPage{}, err
		}
		return FriendPage{UserIDs: ids, NextCursor: next, HasMore: hasMore}, nil
	}
	module.ListOutgoingRequests = func(ctx context.Context, userID, cursor string, limit int) (RequestPage, error) {
		ids, next, hasMore, err := scanMembers(ctx, OutgoingRequestsState, userID, cursor, limit)
		if err != nil {
			return RequestPage{}, err
		}
		return RequestPage{UserIDs: ids, NextCursor: next, HasMore: hasMore}, nil
	}
	module.ListIncomingRequests = func(ctx context.Context, userID, cursor string, limit int) (RequestPage, error) {
		ids, next, hasMore, err := scanMembers(ctx, IncomingRequestsState, userID, cursor, limit)
		if err != nil {
			return RequestPage{}, err
		}
		return RequestPage{UserIDs: ids, NextCursor: next, HasMore: hasMore}, nil
	}

	module.Post = func(ctx context.Context, p Post) error {
		if p.Author == "" || p.Destination == "" || strings.TrimSpace(p.Content) == "" {
			return ErrInvalidPost
		}
		payload, err := json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = microbatchRuntime.Append(ctx, PostsDepot, "", payload)
		return err
	}
	module.PostCount = func(ctx context.Context, userID string) (uint64, error) {
		raw, found, err := store.GetState(ctx, statePartitionFor(PostCountState, userID), []byte(userID))
		if err != nil || !found {
			return 0, err
		}
		return decodeUint64(raw), nil
	}
	module.ResolvePosts = func(ctx context.Context, userID string, cursor uint64, limit int) (PostPage, error) {
		if limit <= 0 || limit > MaxPageSize {
			return PostPage{}, ErrInvalidPageSize
		}
		partitionState := statePartitionFor(PostsState, userID)
		start := userPrefix(userID)
		end := prefixUpperBound(start)
		if cursor != 0 {
			end = seqKey(userID, cursor)
		}
		entries, err := store.ScanState(ctx, partitionState, start, end, limit+1, true)
		if err != nil {
			return PostPage{}, err
		}
		hasMore := len(entries) > limit
		if hasMore {
			entries = entries[:limit]
		}
		resolved := make([]ResolvedPost, len(entries))
		for i, entry := range entries {
			var stored StoredPost
			if err := json.Unmarshal(entry.Value, &stored); err != nil {
				return PostPage{}, err
			}
			author, found, err := getProfileRecord(ctx, stored.Author)
			if err != nil {
				return PostPage{}, err
			}
			resolvedPost := ResolvedPost{PostID: stored.PostID, Author: stored.Author, Content: stored.Content, CreatedAtMillis: stored.CreatedAtMillis}
			if found {
				resolvedPost.AuthorDisplayName = author.DisplayName
				resolvedPost.AuthorProfilePicture = author.ProfilePicture
			}
			resolved[i] = resolvedPost
		}
		next := uint64(0)
		if hasMore {
			next = resolved[len(resolved)-1].PostID
		}
		return PostPage{Posts: resolved, NextCursor: next, HasMore: hasMore}, nil
	}

	module.RecordProfileView = func(ctx context.Context, view ProfileView) error {
		if view.UserID == "" || view.TimestampMillis <= 0 {
			return ErrInvalidProfileView
		}
		payload, err := json.Marshal(view)
		if err != nil {
			return err
		}
		_, err = microbatchRuntime.Append(ctx, ProfileViewsDepot, "", payload)
		return err
	}
	module.ProfileViewCount = func(ctx context.Context, userID string, fromHourInclusive, toHourExclusive int64) (uint64, error) {
		if fromHourInclusive > toHourExclusive {
			return 0, ErrInvalidRange
		}
		partitionState := statePartitionFor(ProfileViewsState, userID)
		start := seqKey(userID, uint64(fromHourInclusive))
		end := seqKey(userID, uint64(toHourExclusive))
		entries, err := store.ScanState(ctx, partitionState, start, end, 1<<20, false)
		if err != nil {
			return 0, err
		}
		var total uint64
		for _, entry := range entries {
			total += decodeUint64(entry.Value)
		}
		return total, nil
	}

	if err := module.Replay(context.Background()); err != nil {
		return nil, fmt.Errorf("replay ramaspace module: %w", err)
	}
	return module, nil
}

func compositeKey(userID, member string) []byte {
	buf := make([]byte, 0, 2+len(userID)+len(member))
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(userID)))
	buf = append(buf, userID...)
	buf = append(buf, member...)
	return buf
}

func userPrefix(userID string) []byte {
	buf := make([]byte, 0, 2+len(userID))
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(userID)))
	buf = append(buf, userID...)
	return buf
}

func seqKey(userID string, seq uint64) []byte {
	buf := userPrefix(userID)
	return binary.BigEndian.AppendUint64(buf, seq)
}

func prefixUpperBound(prefix []byte) []byte {
	end := append([]byte(nil), prefix...)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}

func decodeUint64(value []byte) uint64 {
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}
