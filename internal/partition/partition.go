package partition

import (
	"encoding/binary"
	"errors"
)

var ErrTaskCount = errors.New("task count must be a non-zero power of two")

func New(taskCount uint32) (func([]byte) uint32, error) {
	if taskCount == 0 || taskCount&(taskCount-1) != 0 {
		return nil, ErrTaskCount
	}
	return func(key []byte) uint32 {
		var hash uint64 = 14695981039346656037
		for _, value := range key {
			hash ^= uint64(value)
			hash *= 1099511628211
		}
		return uint32(hash & uint64(taskCount-1))
	}, nil
}

func Uint64Key(value uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, value)
}
