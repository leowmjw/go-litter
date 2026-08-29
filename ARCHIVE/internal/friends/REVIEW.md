# Code Review: internal/friends

**Reviewed:** 2025-11-11  
**Reviewer:** Golang Expert Analysis  
**Status:** Detailed review of code quality, architecture, and test coverage

## Executive Summary

The `friends` package demonstrates clean architecture with good separation between storage layer (Relations) and business logic (Friendship). Test coverage for happy paths is excellent, with both in-memory and Pebble implementations tested identically. However, there are critical issues with error handling, missing concurrency tests, and code duplication that should be addressed before production use.

---

## Critical Issues (High Severity)

| Issue | Location | Justification |
|-------|----------|---------------|
| **Ignoring errors in friend request operations** | Lines 69-70, 75-76, 144-145, 149-150 | `AddFriendRequest` and `CancelFriendRequest` ignore return errors from underlying operations. Silent failures could lead to inconsistent state (e.g., outgoing request added but incoming fails). This could corrupt the friendship graph. |
| **Missing concurrency tests** | `friends_test.go` | `memRelations` uses mutex but no tests verify race-free behavior. Should add `t.Parallel()` tests or `-race` flag verification for critical operations like simultaneous accepts/unfriends. |

---

## Medium Severity Issues

| Issue | Location | Justification |
|-------|----------|---------------|
| **Code duplication in constructors** | Lines 68-118 vs 142-192 | `NewMem()` and `NewPebble()` contain identical business logic wiring. Violates DRY principle and creates maintenance burden. Should extract to shared `wireBusinessLogic(f *Friendship)` function. |
| **Silent no-op on invalid operations** | Lines 86-88, 160-162 | Accepting non-existent request silently succeeds. Should return descriptive error like `ErrNoIncomingRequest` so callers know the operation failed semantically. |
| **Excessive sync in Pebble writes** | Lines 75, 85, 132, 136, 140, 144 | Every write uses `pebble.Sync` (forces fsync). For batch operations, this is 10-100x slower. Should accept `WriteOptions` as parameter or provide batch-aware API. |
| **No validation for self-friendship** | Throughout | No check prevents `alice.AddFriendRequest(alice.userID)`. Self-loops in friendship graph are semantically invalid and could break assumptions in graph algorithms. |
| **Potential memory leak in memRelations** | `mem.go` | Maps never shrink. User with 10,000 friends who unfriends all still has map entry. Consider removing entries when sets become empty to prevent unbounded growth. |
| **Missing error injection tests** | `friends_test.go` | No tests verify behavior when Pebble returns errors (disk full, corruption). Should use error-injecting mock DB to test error paths. |
| **Missing outgoing/incoming request tests** | `friends_test.go` | Code adds/removes requests but never lists them. If `ListOutgoingRequests()` or `ListIncomingRequests()` functionality is needed, it's not implemented or tested. |
| **No observability** | Throughout | Production code should emit metrics (friend counts, request rates) and log errors. Critical for debugging distributed system issues. |

---

## Low Severity Issues

| Issue | Location | Justification |
|-------|----------|---------------|
| **Missing interface for Relations** | Lines 11-22 | `Relations` uses function fields instead of interface. Makes testing harder and prevents proper dependency injection patterns common in Go. |
| **UserFriendship.userID unexported** | Line 45 | Field unexported but might be useful for logging/debugging. Consider adding getter method `UserID()` for observability. |
| **No duplicate request validation** | Throughout | Sending multiple requests to same user works but wastes storage. Should check and return `ErrRequestAlreadyExists`. |
| **Missing benchmark tests** | `friends_test.go` | No benchmarks for key operations (especially Pebble iteration). Important for validating performance at scale (e.g., 1000s of friends). |
| **Inconsistent sorting guarantees** | `mem.go` vs `pebble.go` | Pebble sorts explicitly (line 125-127), mem relies on TreeSet ordering. Document this contract or make both explicit. |
| **Missing package documentation** | `friends.go` | No package comment explaining two-tier API design (Relations vs Friendship vs UserFriendship). Reduces discoverability for new developers. |
| **Weak type safety for UserID** | Throughout | `types.UserID("")` is valid but semantically invalid. Consider validation or non-empty type constraint. |

---

## Positive Observations

- ✅ Clean separation between storage layer (Relations) and business logic (Friendship)
- ✅ Excellent test coverage for happy paths
- ✅ Both in-memory and persistent implementations tested identically
- ✅ Thread-safe implementation with proper mutex usage in `memRelations`
- ✅ Good use of TreeSet for sorted, unique collections
- ✅ Bidirectional friendship maintained correctly
- ✅ Edge cases handled (unfriend non-friend, accept non-existent request)
- ✅ User-scoped API (`UserFriendship`) provides ergonomic interface

---

## Recommendations for Future Work

### Immediate (Before Production)
1. Fix error handling in `AddFriendRequest` and `CancelFriendRequest` - propagate errors
2. Add concurrency tests with `-race` detector
3. Extract duplicated business logic to shared function
4. Add self-friendship validation
5. Return errors for invalid operations instead of silent no-ops

### Short Term
1. Implement proper error types (`ErrNoIncomingRequest`, `ErrAlreadyFriends`, etc.)
2. Add error injection tests for Pebble failure scenarios
3. Make Pebble write options configurable
4. Add metrics and structured logging
5. Create benchmark suite for scaling validation

### Long Term
1. Consider interface-based Relations for better testability
2. Add memory cleanup for empty sets in memRelations
3. Implement `ListOutgoingRequests()` and `ListIncomingRequests()` if needed
4. Add package-level documentation
5. Strengthen UserID type safety with validation

---

## Test Coverage Analysis

### Well Tested ✅
- Basic friendship workflow (request → accept → friends)
- Bidirectional relationship maintenance
- Multiple users and friend networks
- Unfriend operations
- Edge cases (non-existent users, safe no-ops)
- Persistence across store instances (Pebble)
- Both scoped and general API patterns

### Missing Tests ❌
- Concurrent operations (race conditions)
- Error conditions (Pebble failures)
- Request listing functionality
- Self-friendship attempts
- Duplicate request handling
- Performance benchmarks
- Memory cleanup verification

---

## Files Reviewed

- `friends.go` - Core types and constructors
- `mem.go` - In-memory implementation
- `pebble.go` - Persistent storage implementation  
- `friends_test.go` - Test suite

**Total Lines Reviewed:** ~660 lines of code + tests
