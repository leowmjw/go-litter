package banktransfer

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"app/internal/storage"
)

func TestBankTransferModule(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	m, err := New(store, 4)
	if err != nil {
		t.Fatalf("new module: %v", err)
	}

	if err := m.AppendDeposit(ctx, Deposit{UserID: 0, Amt: 200}); err != nil {
		t.Fatalf("deposit alice: %v", err)
	}
	if err := m.AppendDeposit(ctx, Deposit{UserID: 1, Amt: 100}); err != nil {
		t.Fatalf("deposit bob: %v", err)
	}
	if err := m.AppendDeposit(ctx, Deposit{UserID: 2, Amt: 100}); err != nil {
		t.Fatalf("deposit charlie: %v", err)
	}
	if _, err := m.AdvanceAll(ctx); err != nil {
		t.Fatalf("advance deposits: %v", err)
	}

	if err := m.AppendTransfer(ctx, Transfer{TransferID: "alice->bob1", FromUserID: 0, ToUserID: 1, Amt: 50}); err != nil {
		t.Fatalf("transfer alice->bob: %v", err)
	}
	if err := m.AppendTransfer(ctx, Transfer{TransferID: "alice->charlie1", FromUserID: 0, ToUserID: 2, Amt: 160}); err != nil {
		t.Fatalf("transfer alice->charlie1: %v", err)
	}
	if err := m.AppendTransfer(ctx, Transfer{TransferID: "alice->charlie2", FromUserID: 0, ToUserID: 2, Amt: 25}); err != nil {
		t.Fatalf("transfer alice->charlie2: %v", err)
	}
	if err := m.AppendTransfer(ctx, Transfer{TransferID: "charlie->bob1", FromUserID: 2, ToUserID: 1, Amt: 10}); err != nil {
		t.Fatalf("transfer charlie->bob: %v", err)
	}
	if _, err := m.AdvanceAll(ctx); err != nil {
		t.Fatalf("advance transfers: %v", err)
	}

	funds0, err := m.GetFunds(ctx, 0)
	if err != nil || funds0 != 125 {
		t.Fatalf("alice funds = %d, %v", funds0, err)
	}
	funds1, err := m.GetFunds(ctx, 1)
	if err != nil || funds1 != 160 {
		t.Fatalf("bob funds = %d, %v", funds1, err)
	}
	funds2, err := m.GetFunds(ctx, 2)
	if err != nil || funds2 != 115 {
		t.Fatalf("charlie funds = %d, %v", funds2, err)
	}

	outgoing0, err := m.GetOutgoingTransfers(ctx, 0)
	if err != nil {
		t.Fatalf("alice outgoing: %v", err)
	}
	expected0 := map[string]OutgoingRecord{
		"alice->bob1":     {ToUserID: 1, Amt: 50, IsSuccess: true},
		"alice->charlie1": {ToUserID: 2, Amt: 160, IsSuccess: false},
		"alice->charlie2": {ToUserID: 2, Amt: 25, IsSuccess: true},
	}
	if !reflect.DeepEqual(outgoing0, expected0) {
		t.Fatalf("alice outgoing = %+v, want %+v", outgoing0, expected0)
	}

	outgoing2, err := m.GetOutgoingTransfers(ctx, 2)
	if err != nil {
		t.Fatalf("charlie outgoing: %v", err)
	}
	expected2 := map[string]OutgoingRecord{
		"charlie->bob1": {ToUserID: 1, Amt: 10, IsSuccess: true},
	}
	if !reflect.DeepEqual(outgoing2, expected2) {
		t.Fatalf("charlie outgoing = %+v, want %+v", outgoing2, expected2)
	}

	incoming1, err := m.GetIncomingTransfers(ctx, 1)
	if err != nil {
		t.Fatalf("bob incoming: %v", err)
	}
	expectedBob := map[string]IncomingRecord{
		"alice->bob1":   {FromUserID: 0, Amt: 50, IsSuccess: true},
		"charlie->bob1": {FromUserID: 2, Amt: 10, IsSuccess: true},
	}
	if !reflect.DeepEqual(incoming1, expectedBob) {
		t.Fatalf("bob incoming = %+v, want %+v", incoming1, expectedBob)
	}

	incoming2, err := m.GetIncomingTransfers(ctx, 2)
	if err != nil {
		t.Fatalf("charlie incoming: %v", err)
	}
	expectedCharlie := map[string]IncomingRecord{
		"alice->charlie1": {FromUserID: 0, Amt: 160, IsSuccess: false},
		"alice->charlie2": {FromUserID: 0, Amt: 25, IsSuccess: true},
	}
	if !reflect.DeepEqual(incoming2, expectedCharlie) {
		t.Fatalf("charlie incoming = %+v, want %+v", incoming2, expectedCharlie)
	}
}

func TestBankTransferPropagatesStateReadErrors(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	module, err := New(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := module.AppendDeposit(ctx, Deposit{UserID: 1, Amt: 1}); err != nil {
		t.Fatal(err)
	}
	readErr := errors.New("read failed")
	store.GetState = func(context.Context, storage.StatePartition, []byte) ([]byte, bool, error) {
		return nil, false, readErr
	}
	if _, err := module.AdvanceAll(ctx); !errors.Is(err, readErr) {
		t.Fatalf("advance error = %v", err)
	}
}

func TestBankTransferSameBatchOrderingAndValidation(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	m, err := New(store, 1)
	if err != nil {
		t.Fatalf("new module: %v", err)
	}
	if err := m.AppendTransfer(ctx, Transfer{TransferID: "self", FromUserID: 1, ToUserID: 1, Amt: 1}); !errors.Is(err, ErrInvalidTransfer) {
		t.Fatalf("self transfer error = %v", err)
	}
	if err := m.AppendDeposit(ctx, Deposit{UserID: 1, Amt: 100}); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendTransfer(ctx, Transfer{TransferID: "first", FromUserID: 1, ToUserID: 2, Amt: 80}); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendTransfer(ctx, Transfer{TransferID: "second", FromUserID: 1, ToUserID: 3, Amt: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AdvanceAll(ctx); err != nil {
		t.Fatal(err)
	}
	outgoing, err := m.GetOutgoingTransfers(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !outgoing["first"].IsSuccess || outgoing["second"].IsSuccess {
		t.Fatalf("outgoing = %+v", outgoing)
	}
}
