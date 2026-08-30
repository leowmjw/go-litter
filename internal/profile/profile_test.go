package profile

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"app/internal/partition"
	"app/internal/storage"
)

func TestProfileModuleConformance(t *testing.T) {
	factories := []struct {
		name string
		new  func(*testing.T) *storage.Store
	}{
		{name: "memory", new: func(*testing.T) *storage.Store { return storage.NewMemory() }},
		{name: "pebble", new: func(t *testing.T) *storage.Store {
			store, err := storage.NewPebble(filepath.Join(t.TempDir(), "store"))
			if err != nil {
				t.Fatal(err)
			}
			return store
		}},
	}
	for _, factory := range factories {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.new(t)
			defer store.Close()
			testProfileModule(t, store)
		})
	}
}

func testProfileModule(t *testing.T, store *storage.Store) {
	t.Helper()
	ctx := context.Background()
	module, err := New(store, 4)
	if err != nil {
		t.Fatal(err)
	}
	aliceID, registered, err := module.Register(ctx, Registration{UUID: "alice-1", Username: "alice", PasswordHash: "hash1"})
	if err != nil || !registered || aliceID == 0 {
		t.Fatalf("alice registration = %d, %v, %v", aliceID, registered, err)
	}
	retriedID, registered, err := module.Register(ctx, Registration{UUID: "alice-1", Username: "alice", PasswordHash: "hash1"})
	if err != nil || !registered || retriedID != aliceID {
		t.Fatalf("alice retry = %d, %v, %v", retriedID, registered, err)
	}
	info, found, err := module.GetRegistration(ctx, "alice")
	if err != nil || !found || info.UserID != aliceID || info.UUID != "alice-1" {
		t.Fatalf("alice registration info = %#v, %v, %v", info, found, err)
	}
	if _, _, err := module.Register(ctx, Registration{UUID: "alice-1", Username: "alice", PasswordHash: "changed"}); !errors.Is(err, storage.ErrIdempotencyConflict) {
		t.Fatalf("registration idempotency conflict = %v", err)
	}
	if _, registered, err := module.Register(ctx, Registration{UUID: "alice-2", Username: "alice", PasswordHash: "hash3"}); err != nil || registered {
		t.Fatalf("duplicate username registered=%v err=%v", registered, err)
	}
	bobID, registered, err := module.Register(ctx, Registration{UUID: "bob-1", Username: "bob", PasswordHash: "hash2"})
	if err != nil || !registered || bobID == aliceID {
		t.Fatalf("bob registration = %d, %v, %v", bobID, registered, err)
	}
	alice, found, err := module.GetProfile(ctx, aliceID)
	if err != nil || !found || alice.Username != "alice" || alice.DisplayName != nil || alice.HeightInches != nil {
		t.Fatalf("initial alice = %#v, %v, %v", alice, found, err)
	}
	firstEdit := ProfileEdits{RequestID: "alice-edit-1", UserID: aliceID, Edits: []Edit{DisplayName("Alice Smith"), HeightInches(65), PasswordHash("hash4")}}
	if err := module.Edit(ctx, firstEdit); err != nil {
		t.Fatal(err)
	}
	if err := module.Edit(ctx, firstEdit); err != nil {
		t.Fatalf("edit retry: %v", err)
	}
	if err := module.Edit(ctx, ProfileEdits{RequestID: "alice-edit-1", UserID: aliceID, Edits: []Edit{DisplayName("different")}}); !errors.Is(err, storage.ErrIdempotencyConflict) {
		t.Fatalf("edit idempotency conflict = %v", err)
	}
	alice, _, _ = module.GetProfile(ctx, aliceID)
	if alice.DisplayName == nil || *alice.DisplayName != "Alice Smith" || alice.HeightInches == nil || *alice.HeightInches != 65 || alice.PasswordHash != "hash4" {
		t.Fatalf("edited alice = %#v", alice)
	}
	if err := module.Edit(ctx, ProfileEdits{RequestID: "alice-edit-2", UserID: aliceID, Edits: []Edit{DisplayName("Alicia Smith")}}); err != nil {
		t.Fatal(err)
	}
	alice, _, _ = module.GetProfile(ctx, aliceID)
	if alice.DisplayName == nil || *alice.DisplayName != "Alicia Smith" || alice.HeightInches == nil || *alice.HeightInches != 65 || alice.PasswordHash != "hash4" {
		t.Fatalf("partially edited alice = %#v", alice)
	}
	if err := module.Edit(ctx, ProfileEdits{RequestID: "bob-edit-1", UserID: bobID, Edits: []Edit{DisplayName("Bobby")}}); err != nil {
		t.Fatal(err)
	}
	bob, found, err := module.GetProfile(ctx, bobID)
	if err != nil || !found || bob.DisplayName == nil || *bob.DisplayName != "Bobby" || bob.HeightInches != nil || bob.PasswordHash != "hash2" {
		t.Fatalf("edited bob = %#v, %v, %v", bob, found, err)
	}
	if err := module.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	alice, found, err = module.GetProfile(ctx, aliceID)
	if err != nil || !found || alice.DisplayName == nil || *alice.DisplayName != "Alicia Smith" || alice.PasswordHash != "hash4" {
		t.Fatalf("rebuilt alice = %#v, %v, %v", alice, found, err)
	}
}

func TestConcurrentRegistrationHasOneWinner(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	module, err := New(store, 4)
	if err != nil {
		t.Fatal(err)
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, registered, err := module.Register(context.Background(), Registration{UUID: string(rune('a' + i)), Username: "shared", PasswordHash: "hash"})
			if err != nil {
				t.Errorf("register: %v", err)
				return
			}
			if registered {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("winners = %d", winners.Load())
	}
}

func TestProfileValidation(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	module, err := New(store, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := module.Register(context.Background(), Registration{}); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("registration error = %v", err)
	}
	if err := module.Edit(context.Background(), ProfileEdits{}); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("edit error = %v", err)
	}
	if err := module.Edit(context.Background(), ProfileEdits{RequestID: "edit", UserID: 99, Edits: []Edit{DisplayName("missing")}}); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("missing profile error = %v", err)
	}
	invalid := Edit{Field: FieldHeightInches, String: ptr("bad")}
	if err := module.Edit(context.Background(), ProfileEdits{RequestID: "edit", UserID: 99, Edits: []Edit{invalid}}); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("invalid edit error = %v", err)
	}
}

func TestProfileConfigurationCompatibility(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	if _, err := New(store, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := New(store, 2); !errors.Is(err, ErrIncompatibleConfig) {
		t.Fatalf("task-count compatibility error = %v", err)
	}
	if err := store.SetMetadata(context.Background(), ModuleName, []byte("config"), []byte("invalid")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(store, 4); !errors.Is(err, ErrIncompatibleConfig) {
		t.Fatalf("metadata compatibility error = %v", err)
	}
}

func TestPebbleReplaysUnacknowledgedRegistration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store")
	store, err := storage.NewPebble(path)
	if err != nil {
		t.Fatal(err)
	}
	registration := Registration{UUID: "crash-1", Username: "charlie", PasswordHash: "hash"}
	payload, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	choose, _ := partition.New(4)
	depot := storage.DepotPartition{Module: ModuleName, Depot: RegistrationDepot, Partition: choose([]byte(registration.Username))}
	if _, _, err := store.Append(ctx, depot, registration.UUID, payload); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.NewPebble(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	module, err := New(store, 4)
	if err != nil {
		t.Fatal(err)
	}
	info, found, err := module.GetRegistration(ctx, "charlie")
	if err != nil || !found || info.UUID != registration.UUID {
		t.Fatalf("registration = %#v, %v, %v", info, found, err)
	}
	result, found, err := module.GetProfile(ctx, info.UserID)
	if err != nil || !found || result.Username != "charlie" {
		t.Fatalf("profile = %#v, %v, %v", result, found, err)
	}
	if err := module.Edit(ctx, ProfileEdits{RequestID: "charlie-edit", UserID: info.UserID, Edits: []Edit{DisplayName("Charles")}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.NewPebble(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	module, err = New(store, 4)
	if err != nil {
		t.Fatal(err)
	}
	result, found, err = module.GetProfile(ctx, info.UserID)
	if err != nil || !found || result.DisplayName == nil || *result.DisplayName != "Charles" {
		t.Fatalf("reopened edited profile = %#v, %v, %v", result, found, err)
	}
}

func ptr(value string) *string {
	return &value
}
