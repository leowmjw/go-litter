package profile

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"app/internal/partition"
	"app/internal/storage"
	"app/internal/stream"
)

const (
	ModuleName        storage.ModuleID   = "profiles"
	RegistrationDepot storage.DepotID    = "registration"
	ProfileEditsDepot storage.DepotID    = "profile-edits"
	ProfilesTopology  storage.TopologyID = "profiles"
	UsernameState     storage.StateID    = "username-to-registration"
	ProfilesState     storage.StateID    = "profiles"
	IDState           storage.StateID    = "id"
	FieldDisplayName                     = "displayName"
	FieldPasswordHash                    = "pwdHash"
	FieldHeightInches                    = "heightInches"
	StorageVersion                       = 1
)

var (
	ErrInvalidRegistration = errors.New("uuid, username, and password hash are required")
	ErrInvalidEdit         = errors.New("request id, user id, and at least one valid edit are required")
	ErrProfileNotFound     = errors.New("profile not found")
	ErrIDExhausted         = errors.New("user id space exhausted")
	ErrIncompatibleConfig  = errors.New("stored profile module configuration is incompatible")
)

type Registration struct {
	UUID         string `json:"uuid"`
	Username     string `json:"username"`
	PasswordHash string `json:"pwdHash"`
}

type RegistrationInfo struct {
	UserID uint64 `json:"userId"`
	UUID   string `json:"uuid"`
}

type Profile struct {
	UserID       uint64  `json:"userId"`
	Username     string  `json:"username"`
	PasswordHash string  `json:"pwdHash"`
	DisplayName  *string `json:"displayName,omitempty"`
	HeightInches *int    `json:"heightInches,omitempty"`
}

type Edit struct {
	Field   string  `json:"field"`
	String  *string `json:"string,omitempty"`
	Integer *int    `json:"integer,omitempty"`
}

type ProfileEdits struct {
	RequestID string `json:"requestId"`
	UserID    uint64 `json:"userId"`
	Edits     []Edit `json:"edits"`
}

type moduleMetadata struct {
	StorageVersion int    `json:"storageVersion"`
	TaskCount      uint32 `json:"taskCount"`
}

type Module struct {
	Register        func(context.Context, Registration) (uint64, bool, error)
	Edit            func(context.Context, ProfileEdits) error
	GetProfile      func(context.Context, uint64) (Profile, bool, error)
	GetRegistration func(context.Context, string) (RegistrationInfo, bool, error)
	Replay          func(context.Context) error
	Rebuild         func(context.Context) error
}

func DisplayName(value string) Edit {
	return Edit{Field: FieldDisplayName, String: &value}
}

func PasswordHash(value string) Edit {
	return Edit{Field: FieldPasswordHash, String: &value}
}

func HeightInches(value int) Edit {
	return Edit{Field: FieldHeightInches, Integer: &value}
}

func New(store *storage.Store, taskCount uint32) (*Module, error) {
	choosePartition, err := partition.New(taskCount)
	if err != nil {
		return nil, err
	}
	expectedMetadata := moduleMetadata{StorageVersion: StorageVersion, TaskCount: taskCount}
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
	registrationHandler := func(ctx context.Context, event *stream.Event, record storage.Record, task uint32) ([]byte, error) {
		var registration Registration
		if err := json.Unmarshal(record.Payload, &registration); err != nil {
			return nil, err
		}
		infoBytes, found, err := event.Get(ctx, state(UsernameState, task), []byte(registration.Username))
		if err != nil {
			return nil, err
		}
		if found {
			var info RegistrationInfo
			if err := json.Unmarshal(infoBytes, &info); err != nil {
				return nil, err
			}
			if info.UUID == registration.UUID {
				return partition.Uint64Key(info.UserID), nil
			}
			return nil, nil
		}
		counterKey := []byte("next")
		counterBytes, _, err := event.Get(ctx, state(IDState, task), counterKey)
		if err != nil {
			return nil, err
		}
		counter := decodeUint64(counterBytes)
		if counter == math.MaxUint64 || counter+1 > (math.MaxUint64-uint64(task))/uint64(taskCount) {
			return nil, ErrIDExhausted
		}
		counter++
		userID := counter*uint64(taskCount) + uint64(task)
		info := RegistrationInfo{UserID: userID, UUID: registration.UUID}
		profile := Profile{UserID: userID, Username: registration.Username, PasswordHash: registration.PasswordHash}
		encodedInfo, err := json.Marshal(info)
		if err != nil {
			return nil, err
		}
		encodedProfile, err := json.Marshal(profile)
		if err != nil {
			return nil, err
		}
		event.Set(state(IDState, task), counterKey, partition.Uint64Key(counter))
		event.Set(state(UsernameState, task), []byte(registration.Username), encodedInfo)
		profileTask := choosePartition(partition.Uint64Key(userID))
		event.Set(state(ProfilesState, profileTask), partition.Uint64Key(userID), encodedProfile)
		return partition.Uint64Key(userID), nil
	}
	editHandler := func(ctx context.Context, event *stream.Event, record storage.Record, task uint32) ([]byte, error) {
		var edits ProfileEdits
		if err := json.Unmarshal(record.Payload, &edits); err != nil {
			return nil, err
		}
		key := partition.Uint64Key(edits.UserID)
		encoded, found, err := event.Get(ctx, state(ProfilesState, task), key)
		if err != nil || !found {
			return nil, err
		}
		var current Profile
		if err := json.Unmarshal(encoded, &current); err != nil {
			return nil, err
		}
		for _, edit := range edits.Edits {
			switch edit.Field {
			case FieldDisplayName:
				current.DisplayName = cloneString(edit.String)
			case FieldPasswordHash:
				if edit.String != nil {
					current.PasswordHash = *edit.String
				}
			case FieldHeightInches:
				current.HeightInches = cloneInt(edit.Integer)
			}
		}
		encoded, err = json.Marshal(current)
		if err != nil {
			return nil, err
		}
		event.Set(state(ProfilesState, task), key, encoded)
		return nil, nil
	}
	runtime, err := stream.New(ModuleName, taskCount, choosePartition, store,
		stream.Source{
			Depot: RegistrationDepot, Topology: ProfilesTopology,
			PartitionKey: func(payload []byte) ([]byte, error) {
				var registration Registration
				if err := json.Unmarshal(payload, &registration); err != nil {
					return nil, err
				}
				return []byte(registration.Username), nil
			},
			Handle: registrationHandler,
		},
		stream.Source{
			Depot: ProfileEditsDepot, Topology: ProfilesTopology,
			PartitionKey: func(payload []byte) ([]byte, error) {
				var edits ProfileEdits
				if err := json.Unmarshal(payload, &edits); err != nil {
					return nil, err
				}
				return partition.Uint64Key(edits.UserID), nil
			},
			Handle: editHandler,
		},
	)
	if err != nil {
		return nil, err
	}
	module := &Module{Replay: runtime.Replay, Rebuild: runtime.Rebuild}
	module.GetRegistration = func(ctx context.Context, username string) (RegistrationInfo, bool, error) {
		task := choosePartition([]byte(username))
		encoded, found, err := store.GetState(ctx, state(UsernameState, task), []byte(username))
		if err != nil || !found {
			return RegistrationInfo{}, found, err
		}
		var info RegistrationInfo
		if err := json.Unmarshal(encoded, &info); err != nil {
			return RegistrationInfo{}, false, err
		}
		return info, true, nil
	}
	module.GetProfile = func(ctx context.Context, userID uint64) (Profile, bool, error) {
		key := partition.Uint64Key(userID)
		task := choosePartition(key)
		encoded, found, err := store.GetState(ctx, state(ProfilesState, task), key)
		if err != nil || !found {
			return Profile{}, found, err
		}
		var profile Profile
		if err := json.Unmarshal(encoded, &profile); err != nil {
			return Profile{}, false, err
		}
		return profile, true, nil
	}
	module.Register = func(ctx context.Context, registration Registration) (uint64, bool, error) {
		registration.UUID = strings.TrimSpace(registration.UUID)
		registration.Username = strings.TrimSpace(registration.Username)
		if registration.UUID == "" || registration.Username == "" || registration.PasswordHash == "" {
			return 0, false, ErrInvalidRegistration
		}
		payload, err := json.Marshal(registration)
		if err != nil {
			return 0, false, err
		}
		ack, err := runtime.Append(ctx, RegistrationDepot, registration.UUID, payload)
		if err != nil {
			return 0, false, err
		}
		userIDBytes, ok := ack[ProfilesTopology]
		if !ok || len(userIDBytes) != 8 {
			return 0, false, nil
		}
		return binary.BigEndian.Uint64(userIDBytes), true, nil
	}
	module.Edit = func(ctx context.Context, edits ProfileEdits) error {
		if err := validateEdits(edits); err != nil {
			return err
		}
		if _, found, err := module.GetProfile(ctx, edits.UserID); err != nil {
			return err
		} else if !found {
			return ErrProfileNotFound
		}
		payload, err := json.Marshal(edits)
		if err != nil {
			return err
		}
		_, err = runtime.Append(ctx, ProfileEditsDepot, edits.RequestID, payload)
		return err
	}
	if err := module.Replay(context.Background()); err != nil {
		return nil, fmt.Errorf("replay profile module: %w", err)
	}
	return module, nil
}

func validateEdits(edits ProfileEdits) error {
	if strings.TrimSpace(edits.RequestID) == "" || edits.UserID == 0 || len(edits.Edits) == 0 {
		return ErrInvalidEdit
	}
	for _, edit := range edits.Edits {
		switch edit.Field {
		case FieldDisplayName, FieldPasswordHash:
			if edit.String == nil || edit.Integer != nil {
				return ErrInvalidEdit
			}
		case FieldHeightInches:
			if edit.Integer == nil || edit.String != nil || *edit.Integer < 1 {
				return ErrInvalidEdit
			}
		default:
			return ErrInvalidEdit
		}
	}
	return nil
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func decodeUint64(value []byte) uint64 {
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}
