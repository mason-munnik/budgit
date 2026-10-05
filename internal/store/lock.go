package store

import (
	"fmt"
	"os"
	"time"
)

// Save compares the file on disk with what Load read, then renames over it.
// Two processes saving at once could both pass that check before either
// renames, and the second would silently erase the first. A lock file held
// across the check and the rename closes that gap. It is a plain O_EXCL file
// rather than flock, which works the same on every OS budgit builds for.

var (
	// lockWait is how long Save waits for another process's save to finish.
	// A save holds the lock for milliseconds, so this is generous.
	lockWait = 2 * time.Second
	// lockStale is when a lock is taken to be left by a save that crashed.
	lockStale = 10 * time.Second
)

// lockFile takes path's lock and returns the function that releases it.
func lockFile(path string) (release func(), err error) {
	lock := path + ".lock"
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- beside the user's own data file
		if err == nil {
			_ = f.Close() // the lock is the file existing; nothing is written to it
			return func() { _ = os.Remove(lock) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if fi, err := os.Stat(lock); err == nil && time.Since(fi.ModTime()) > lockStale {
			_ = os.Remove(lock) // try again next pass; another process may have beaten us to it
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another budgit is saving %s (if none is running, delete %s)", path, lock)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
