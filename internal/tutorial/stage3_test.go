package tutorial

import (
	"context"
	"testing"

	"app/internal/storage"
)

func TestStage3PerKeyOrderPreservedAcrossPartitions(t *testing.T) {
	ctx := context.Background()
	module, err := NewStage3(storage.NewMemory(), 4)
	if err != nil {
		t.Fatalf("NewStage3: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := module.Send(ctx, Mail{Sender: "alice", Recipient: "carol", Message: itoa(i)}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	count, err := module.SentCount(ctx, "alice")
	if err != nil || count != 5 {
		t.Fatalf("SentCount = %d, %v", count, err)
	}
	inbox, err := module.Inbox(ctx, "carol")
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(inbox) != 5 {
		t.Fatalf("inbox length = %d, want 5", len(inbox))
	}
	// A single sender's records preserve append order regardless of how the
	// module is partitioned, because they always share one depot partition.
	for i, msg := range inbox {
		want := "alice: " + itoa(i)
		if msg != want {
			t.Fatalf("inbox[%d] = %q, want %q (full inbox=%v)", i, msg, want, inbox)
		}
	}
}

func TestStage3RepartitioningRoutesToRecipientTask(t *testing.T) {
	ctx := context.Background()
	module, err := NewStage3(storage.NewMemory(), 8)
	if err != nil {
		t.Fatalf("NewStage3: %v", err)
	}
	sender, recipient := "sender-a", "recipient-b"
	if module.TaskOf(sender) == module.TaskOf(recipient) {
		t.Skip("chosen keys hashed to the same task; not exercising cross-partition routing")
	}
	if err := module.Send(ctx, Mail{Sender: sender, Recipient: recipient, Message: "hello"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	// The effect is visible on the recipient's task even though the handler
	// executed under the sender's task lock.
	inbox, err := module.Inbox(ctx, recipient)
	if err != nil || len(inbox) != 1 || inbox[0] != sender+": hello" {
		t.Fatalf("inbox = %v, %v", inbox, err)
	}
	count, err := module.SentCount(ctx, sender)
	if err != nil || count != 1 {
		t.Fatalf("SentCount = %d, %v", count, err)
	}
}

func TestStage3IndependentSendersProcessConcurrently(t *testing.T) {
	ctx := context.Background()
	module, err := NewStage3(storage.NewMemory(), 4)
	if err != nil {
		t.Fatalf("NewStage3: %v", err)
	}
	senders := []string{"alice", "bob", "carol", "dave"}
	done := make(chan error, len(senders))
	for _, sender := range senders {
		go func(sender string) {
			done <- module.Send(ctx, Mail{Sender: sender, Recipient: "shared", Message: "hi from " + sender})
		}(sender)
	}
	for range senders {
		if err := <-done; err != nil {
			t.Fatalf("send error: %v", err)
		}
	}
	for _, sender := range senders {
		count, err := module.SentCount(ctx, sender)
		if err != nil || count != 1 {
			t.Fatalf("SentCount(%s) = %d, %v", sender, count, err)
		}
	}
}

func itoa(i int) string {
	digits := "0123456789"
	if i == 0 {
		return "0"
	}
	buf := make([]byte, 0, 4)
	for i > 0 {
		buf = append([]byte{digits[i%10]}, buf...)
		i /= 10
	}
	return string(buf)
}
