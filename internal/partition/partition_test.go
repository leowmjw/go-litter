package partition

import (
	"errors"
	"reflect"
	"testing"
)

func TestNew(t *testing.T) {
	for _, count := range []uint32{0, 3, 6} {
		if _, err := New(count); !errors.Is(err, ErrTaskCount) {
			t.Fatalf("count %d error = %v", count, err)
		}
	}
	choose, err := New(4)
	if err != nil {
		t.Fatal(err)
	}
	first := choose([]byte("alice"))
	if first >= 4 || choose([]byte("alice")) != first {
		t.Fatalf("unstable partition %d", first)
	}
}

func TestUint64Key(t *testing.T) {
	if got := Uint64Key(0x0102030405060708); !reflect.DeepEqual(got, []byte{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("key = %v", got)
	}
}
