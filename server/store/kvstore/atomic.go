package kvstore

import (
	"time"

	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"
)

// Retry tuning for atomic compare-and-set. These match the error-handling
// convention pinned in PLAN.md section 9: 5 retries with a ~10 ms backoff.
const (
	// MaxRetries is the maximum number of compare-and-set attempts before
	// SetAtomicWithRetries gives up and returns an error.
	MaxRetries = 5
	// RetryBackoff is the delay slept between conflicting CAS attempts. It is
	// intentionally small (cooperative scheduling) to avoid hammering the DB
	// while letting competing writers make progress.
	RetryBackoff = 10 * time.Millisecond
)

// conflictLogger is the shape of a logger call used to report each CAS
// conflict. It mirrors pluginapi.LogService.Warn (a pre-formatted message plus
// alternating key/value fields) so the production client can wire Log.Warn
// directly, and tests can capture into a buffer. Keeping it as a function type
// (rather than depending on a concrete logger interface) decouples the package
// from pluginapi's LogService and makes the retry loop unit-testable in
// isolation.
type conflictLogger func(message string, keyValuePairs ...any)

// atomicBackend is the minimal subset of pluginapi.KVService (and
// pluginapi.MemoryStore) that the retry loop needs. Declaring it locally lets
// the retry helper operate against any KV implementation — the production
// pluginapi client in Client, or pluginapi.MemoryStore in tests — without
// pulling in extra dependencies.
type atomicBackend interface {
	Get(key string, o any) error
	Set(key string, value any, options ...pluginapi.KVSetOption) (bool, error)
}

// SetAtomicWithRetries performs a read-modify-write on key using atomic
// compare-and-set semantics, retrying on conflict.
//
// For each attempt it:
//  1. Reads the current value of key (old).
//  2. Invokes update(old) to compute the new value.
//  3. Writes the new value back atomically, requiring old to still be the
//     current value (pluginapi.SetAtomic).
//
// If another writer changed the value between the read and the write (Set
// returns saved=false), the conflict is reported via log and the loop sleeps
// RetryBackoff before retrying. After MaxRetries attempts the function returns
// an error naming the key and the number of retries.
//
// An error from update propagates immediately (no retry), since it signals an
// application-level decision to abort the write rather than a conflict.
func SetAtomicWithRetries(backend atomicBackend, log conflictLogger, key string, update func(old []byte) (any, error)) error {
	if update == nil {
		return errors.New("update function must not be nil")
	}

	for attempt := 1; attempt <= MaxRetries; attempt++ {
		var old []byte
		if err := backend.Get(key, &old); err != nil {
			return errors.Wrapf(err, "failed to get value for key %s", key)
		}

		newVal, err := update(old)
		if err != nil {
			return errors.Wrap(err, "update function failed")
		}

		saved, err := backend.Set(key, newVal, pluginapi.SetAtomic(old))
		if err != nil {
			return errors.Wrapf(err, "failed to atomically set key %s", key)
		}
		if saved {
			return nil
		}

		// Conflict: another writer beat us between read and write. Log clearly
		// (key + attempt count) so contention is debuggable, then back off and
		// retry. We only sleep between attempts, not after the final one.
		if log != nil {
			log("KV atomic conflict",
				"key", key,
				"retry", attempt,
				"max_retries", MaxRetries,
			)
		}
		if attempt < MaxRetries {
			time.Sleep(RetryBackoff)
		}
	}

	return errors.Errorf("failed to atomically set key=%s after %d retries", key, MaxRetries)
}
