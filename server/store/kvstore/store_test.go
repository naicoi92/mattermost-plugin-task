package kvstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestSetAtomicWithRetries_ConcurrentNoLostUpdates spins up many goroutines
// that each increment a shared counter via SetAtomicWithRetries against a real
// pluginapi.MemoryStore. Because every increment is a compare-and-set loop, no
// update may be lost: the final counter value must equal the number of writers.
//
// This validates acceptance criterion #1 of issue #6 ("verifies no updates are
// lost") and exercises the same code path (pluginapi.MemoryStore) that the
// production SetAtomicWithRetries uses against the Mattermost KV store.
//
// The writer count is kept small relative to MaxRetries so the cooperative
// scheduler has enough retries to resolve the burst of conflicts. The test's
// contract is "no lost updates", not "any fixed concurrency level".
func TestSetAtomicWithRetries_ConcurrentNoLostUpdates(t *testing.T) {
	const writers = 10

	store := &pluginapi.MemoryStore{}
	key := "counter:test"

	increment := func(old []byte) (any, error) {
		var current int
		if len(old) > 0 {
			if err := json.Unmarshal(old, &current); err != nil {
				return nil, fmt.Errorf("unmarshal counter: %w", err)
			}
		}
		current++
		return current, nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	start := make(chan struct{})

	for range writers {
		wg.Go(func() {
			<-start // release all writers at once to maximize contention
			if err := SetAtomicWithRetries(store, nil, key, increment); err != nil {
				errs <- err
			}
		})
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err, "writer returned error")
	}

	var got int
	require.NoError(t, store.Get(key, &got), "read back counter")
	assert.Equal(t, writers, got, "no increments may be lost under contention")
}

// TestSetAtomicWithRetries_RetriesOnConflict uses a controllable fake backend
// that rejects the first few CAS writes (simulating a competing writer) before
// accepting one. It validates acceptance criterion #2: the wrapper retries up
// to MaxRetries times with a ~RetryBackoff pause between attempts.
//
// acceptOnAttempt = N means attempts 1..N-1 conflict (and are logged) and
// attempt N succeeds, so there are N-1 conflicts and N total Set calls.
func TestSetAtomicWithRetries_RetriesOnConflict(t *testing.T) {
	const attemptsUntilSuccess = 3
	const expectedConflicts = attemptsUntilSuccess - 1

	backend := &fakeBackend{acceptOnAttempt: attemptsUntilSuccess}
	var logs []string
	logger := func(msg string, kv ...any) { logs = append(logs, formatLog(msg, kv)) }

	start := time.Now()
	err := SetAtomicWithRetries(backend, logger, "k", func(old []byte) (any, error) {
		return []byte("v"), nil
	})
	elapsed := time.Since(start)

	require.NoError(t, err, "should succeed after conflicts resolve")
	assert.Equal(t, attemptsUntilSuccess, backend.setCalls(), "one Set per attempt until accepted")
	assert.Len(t, logs, expectedConflicts, "one conflict log per rejected attempt")

	// Each conflict except the last failing attempt sleeps RetryBackoff. With
	// expectedConflicts rejected attempts the loop sleeps expectedConflicts
	// times (before each retry), bounding the minimum wall-clock duration.
	minExpected := time.Duration(expectedConflicts) * RetryBackoff
	assert.GreaterOrEqual(t, elapsed, minExpected, "should back off between retries")
}

// TestSetAtomicWithRetries_FailsAfterMaxRetries forces every CAS to conflict
// and asserts the wrapper gives up after exactly MaxRetries attempts, returning
// an error that names the key and retry budget.
func TestSetAtomicWithRetries_FailsAfterMaxRetries(t *testing.T) {
	backend := &fakeBackend{acceptOnAttempt: -1} // never accept
	var logs []string
	logger := func(msg string, kv ...any) { logs = append(logs, formatLog(msg, kv)) }

	err := SetAtomicWithRetries(backend, logger, "hotkey", func(old []byte) (any, error) {
		return []byte("v"), nil
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "hotkey", "error must name the key")
	assert.Contains(t, err.Error(), fmt.Sprintf("%d retries", MaxRetries), "error must report retry budget")
	assert.Equal(t, MaxRetries, backend.setCalls(), "must attempt exactly MaxRetries times")
	assert.Len(t, logs, MaxRetries, "every conflict is logged")
}

// TestSetAtomicWithRetries_LogsConflict validates acceptance criterion #3: each
// conflict is logged clearly with the key name and retry count. It checks both
// the message text and the structured key/value fields.
func TestSetAtomicWithRetries_LogsConflict(t *testing.T) {
	t.Run("log includes key name and retry count", func(t *testing.T) {
		backend := &fakeBackend{acceptOnAttempt: 3} // 2 conflicts, then success
		var captured []capturedLog
		logger := func(msg string, kv ...any) { captured = append(captured, capturedLog{msg: msg, fields: kv}) }

		err := SetAtomicWithRetries(backend, logger, "idx:u:u1:assigned:t9", func(old []byte) (any, error) {
			return []byte("v"), nil
		})
		require.NoError(t, err)
		require.Len(t, captured, 2)

		for i, c := range captured {
			assert.Equal(t, "KV atomic conflict", c.msg, "log %d message", i)
			assert.Contains(t, c.fields, "idx:u:u1:assigned:t9", "log %d must carry key name", i)
			assert.Contains(t, c.fields, i+1, "log %d must carry attempt number", i)
			assert.Contains(t, c.fields, MaxRetries, "log %d must carry max retries", i)
		}
	})

	t.Run("nil logger is tolerated", func(t *testing.T) {
		// A nil logger must not panic; the retry loop still runs.
		backend := &fakeBackend{acceptOnAttempt: -1}
		err := SetAtomicWithRetries(backend, nil, "k", func(old []byte) (any, error) {
			return []byte("v"), nil
		})
		require.Error(t, err)
	})
}

// TestSetAtomicWithRetries_UpdateErrorPropagates verifies that an error from
// the user-provided update function aborts immediately — it is an application
// decision, not a conflict, and must not trigger a retry.
func TestSetAtomicWithRetries_UpdateErrorPropagates(t *testing.T) {
	backend := &fakeBackend{acceptOnAttempt: 1}
	wantErr := errors.New("boom")

	err := SetAtomicWithRetries(backend, nil, "k", func(old []byte) (any, error) {
		return nil, wantErr
	})

	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, 0, backend.setCalls(), "must not call Set when update fails")
}

// TestSetAtomicWithRetries_NilUpdateRejected guards against a misuse that
// would otherwise panic inside the loop.
func TestSetAtomicWithRetries_NilUpdateRejected(t *testing.T) {
	err := SetAtomicWithRetries(&fakeBackend{acceptOnAttempt: 1}, nil, "k", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update function must not be nil")
}

// TestSetAtomicWithRetries_UpdateMutationDoesNotCorruptCAS is a regression test
// for the defensive copy of the old value. The update callback receives a copy
// of the current bytes; if it mutates them in place, the CAS precondition
// passed to pluginapi.SetAtomic must still describe the *original* value.
//
// It drives the production Client path (pluginapi.KVService.Set →
// KVSetWithOptions) against a plugintest.API mock, which records the exact
// OldValue bytes used for the CAS comparison. Without the defensive copy in the
// retry loop, mutating old's backing array would make the CAS compare against
// the mutated bytes.
func TestSetAtomicWithRetries_UpdateMutationDoesNotCorruptCAS(t *testing.T) {
	api := &plugintest.API{}
	driver := &plugintest.Driver{}
	client := pluginapi.NewClient(api, driver)
	store := NewKVStore(client)

	key := "edge:u1:t1:comments"
	// stored is what the DB holds. snapshot is an independent copy the test
	// compares against, so it is unaffected by any mutation of the Get result.
	stored := bytes.Repeat([]byte{0xAB}, 16)
	snapshot := append([]byte(nil), stored...)

	// Get returns stored; update will mutate its input in place.
	api.On("KVGet", key).Return(stored, nil).Once()
	// Capture the exact OldValue the CAS layer received.
	var captured model.PluginKVSetOptions
	api.On("KVSetWithOptions", key, []byte{0x01}, mock.AnythingOfType("model.PluginKVSetOptions")).
		Run(func(args mock.Arguments) {
			captured = args.Get(2).(model.PluginKVSetOptions)
		}).
		Return(true, nil).Once()

	mutated := false
	err := store.SetAtomicWithRetries(key, func(old []byte) (any, error) {
		for i := range old {
			if old[i] != 0 {
				old[i] = 0 // mutate the input buffer in place
				mutated = true
			}
		}
		return []byte{0x01}, nil
	})

	require.True(t, mutated, "sanity: update must have mutated its input buffer")
	require.NoError(t, err)
	// The CAS OldValue must equal the untouched snapshot of the original bytes.
	// Without the defensive copy, OldValue would be the zeroed-out buffer.
	assert.True(t, captured.Atomic, "CAS option must be atomic")
	assert.Equal(t, snapshot, captured.OldValue, "CAS OldValue must be the original, unmutated bytes")
}

// casBackend documentation removed — see the test above for why a custom
// backend cannot validate this regression (pluginapi.KVSetOptions.oldValue is
// unexported). The production Client path through plugintest.API is used
// instead, where OldValue surfaces as the exported
// model.PluginKVSetOptions.OldValue field.

// TestClient_SetAtomicWithRetries_Wiring confirms the Client method delegates
// to SetAtomicWithRetries against the pluginapi KV service, using a plugintest
// API mock for a single happy-path attempt.
func TestClient_SetAtomicWithRetries_Wiring(t *testing.T) {
	api := &plugintest.API{}
	driver := &plugintest.Driver{}
	client := pluginapi.NewClient(api, driver)
	store := NewKVStore(client)

	// First attempt: empty old value, update returns "v", CAS succeeds.
	// pluginapi passes []byte values through verbatim (no JSON re-encoding).
	api.On("KVGet", "k").Return(nil, nil).Once()
	api.On("KVSetWithOptions", "k", []byte("v"), model.PluginKVSetOptions{Atomic: true}).Return(true, nil).Once()

	err := store.SetAtomicWithRetries("k", func(old []byte) (any, error) {
		return []byte("v"), nil
	})
	require.NoError(t, err)
}

// --- helpers and fakes ---

// fakeBackend is a controllable atomicBackend for retry tests. It always reads
// an empty old value and accepts the Set only on the configured attempt.
// acceptOnAttempt <= 0 means "never accept" (always conflict).
type fakeBackend struct {
	mu              sync.Mutex
	acceptOnAttempt int
	calls           int
}

func (f *fakeBackend) Get(key string, o any) error {
	if out, ok := o.(*[]byte); ok {
		*out = nil
	}
	return nil
}

func (f *fakeBackend) Set(key string, value any, options ...pluginapi.KVSetOption) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.acceptOnAttempt > 0 && f.calls >= f.acceptOnAttempt {
		return true, nil
	}
	return false, nil
}

func (f *fakeBackend) setCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// capturedLog records a single conflict log call for assertions.
type capturedLog struct {
	msg    string
	fields []any
}

// formatLog renders a structured log call into a single human-readable line,
// matching how Mattermost would emit it, so tests can assert on substrings.
func formatLog(msg string, kv []any) string {
	var b bytes.Buffer
	b.WriteString(msg)
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, " %v=%v", kv[i], kv[i+1])
	}
	return b.String()
}
