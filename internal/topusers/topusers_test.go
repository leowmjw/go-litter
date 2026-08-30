package topusers

import (
	"context"
	"reflect"
	"testing"

	"app/internal/storage"
)

func mustAppendPurchase(t *testing.T, ctx context.Context, m *Module, userID uint64, cents int) {
	t.Helper()
	if err := m.Append(ctx, Purchase{UserID: userID, PurchaseCents: cents}); err != nil {
		t.Fatalf("append user=%d cents=%d: %v", userID, cents, err)
	}
}

func TestTopUsersModule(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	m, err := New(store, 4, 3)
	if err != nil {
		t.Fatalf("new module: %v", err)
	}

	mustAppendPurchase(t, ctx, m, 0, 300)
	mustAppendPurchase(t, ctx, m, 1, 200)
	mustAppendPurchase(t, ctx, m, 2, 100)
	mustAppendPurchase(t, ctx, m, 3, 100)
	mustAppendPurchase(t, ctx, m, 4, 400)

	if _, err := m.AdvanceAll(ctx); err != nil {
		t.Fatalf("advance all: %v", err)
	}

	top, err := m.GetTopSpendingUsers(ctx)
	if err != nil {
		t.Fatalf("get top: %v", err)
	}
	expected := []SpendingUser{
		{UserID: 4, Total: 400},
		{UserID: 0, Total: 300},
		{UserID: 1, Total: 200},
	}
	if !reflect.DeepEqual(top, expected) {
		t.Fatalf("top = %+v, want %+v", top, expected)
	}

	ids, err := m.GetTopUserIDs(ctx)
	if err != nil {
		t.Fatalf("get top ids: %v", err)
	}
	if !reflect.DeepEqual(ids, []uint64{4, 0, 1}) {
		t.Fatalf("top ids = %v, want [4 0 1]", ids)
	}

	mustAppendPurchase(t, ctx, m, 3, 250)
	if _, err := m.AdvanceAll(ctx); err != nil {
		t.Fatalf("advance all: %v", err)
	}

	top, err = m.GetTopSpendingUsers(ctx)
	if err != nil {
		t.Fatalf("get top after update: %v", err)
	}
	expected = []SpendingUser{
		{UserID: 4, Total: 400},
		{UserID: 3, Total: 350},
		{UserID: 0, Total: 300},
	}
	if !reflect.DeepEqual(top, expected) {
		t.Fatalf("top after update = %+v, want %+v", top, expected)
	}

	ids, err = m.GetTopUserIDs(ctx)
	if err != nil {
		t.Fatalf("get top ids after update: %v", err)
	}
	if !reflect.DeepEqual(ids, []uint64{4, 3, 0}) {
		t.Fatalf("top ids after update = %v, want [4 3 0]", ids)
	}
}
