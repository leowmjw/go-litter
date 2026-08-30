package storage

import "encoding/binary"

const (
	keyVersion     byte = 1
	kindRecord     byte = 1
	kindHighWater  byte = 2
	kindAppendID   byte = 3
	kindState      byte = 4
	kindCheckpoint byte = 5
	kindBatch      byte = 6
	kindMetadata   byte = 7
)

func appendString(dst []byte, value string) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(value)))
	return append(dst, value...)
}

func depotPrefix(kind byte, depot DepotPartition) []byte {
	key := []byte{keyVersion, kind}
	key = appendString(key, string(depot.Module))
	key = appendString(key, string(depot.Depot))
	return binary.BigEndian.AppendUint32(key, depot.Partition)
}

func recordKey(depot DepotPartition, position uint64) []byte {
	return binary.BigEndian.AppendUint64(depotPrefix(kindRecord, depot), position)
}

func recordPrefix(depot DepotPartition) []byte {
	return depotPrefix(kindRecord, depot)
}

func highWaterKey(depot DepotPartition) []byte {
	return depotPrefix(kindHighWater, depot)
}

func appendIDKey(depot DepotPartition, id string) []byte {
	return appendString(depotPrefix(kindAppendID, depot), id)
}

func stateModulePrefix(module ModuleID) []byte {
	return appendString([]byte{keyVersion, kindState}, string(module))
}

func statePrefix(state StatePartition) []byte {
	key := stateModulePrefix(state.Module)
	key = appendString(key, string(state.State))
	return binary.BigEndian.AppendUint32(key, state.Partition)
}

func stateKey(state StatePartition, logical []byte) []byte {
	return append(statePrefix(state), logical...)
}

func checkpointModulePrefix(module ModuleID) []byte {
	return appendString([]byte{keyVersion, kindCheckpoint}, string(module))
}

func checkpointKey(cursor Cursor) []byte {
	key := checkpointModulePrefix(cursor.Module)
	key = appendString(key, string(cursor.Topology))
	key = appendString(key, string(cursor.Depot))
	return binary.BigEndian.AppendUint32(key, cursor.Partition)
}

func batchModulePrefix(module ModuleID) []byte {
	return appendString([]byte{keyVersion, kindBatch}, string(module))
}

func batchKey(module ModuleID, id BatchID) []byte {
	return appendString(batchModulePrefix(module), string(id))
}

func metadataPrefix(module ModuleID) []byte {
	return appendString([]byte{keyVersion, kindMetadata}, string(module))
}

func metadataKey(module ModuleID, key []byte) []byte {
	return append(metadataPrefix(module), key...)
}

func prefixEnd(prefix []byte) []byte {
	end := cloneBytes(prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] != 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}

func uint64Bytes(value uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, value)
}

func decodeUint64(value []byte) uint64 {
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}
