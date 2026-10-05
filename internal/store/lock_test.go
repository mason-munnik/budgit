package store

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSaveReleasesLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.json")
	db, _ := Load(path)
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Errorf("lock left behind after a save: %v", err)
	}
}

// Many writers who all read the same version: exactly one save may land.
// Without the lock, several can pass the on-disk check before any renames.
func TestConcurrentSavesOnlyOneWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.json")
	first, _ := Load(path)
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}

	const n = 16
	dbs := make([]*DB, n)
	for i := range dbs {
		dbs[i], _ = Load(path)
		if _, err := AddCategory(dbs[i], "Cat"+string(rune('A'+i)), KindExpense); err != nil {
			t.Fatal(err)
		}
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		won      int
		start    = make(chan struct{})
		unwanted []error
	)
	for _, db := range dbs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := db.Save()
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case !errors.Is(err, ErrChangedOnDisk):
				unwanted = append(unwanted, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if won != 1 {
		t.Errorf("%d saves landed, want exactly 1", won)
	}
	for _, err := range unwanted {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSaveWaitsThenGivesUpOnHeldLock(t *testing.T) {
	defer func(w time.Duration) { lockWait = w }(lockWait)
	lockWait = 50 * time.Millisecond

	path := filepath.Join(t.TempDir(), "b.json")
	db, _ := Load(path)
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.Save(); err == nil {
		t.Fatal("saved while another process held the lock")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("data file written despite the lock: %v", err)
	}
}

// A lock left by a crashed save must not wedge budgit forever.
func TestSaveBreaksStaleLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.json")
	db, _ := Load(path)
	lock := path + ".lock"
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * lockStale)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if err := db.Save(); err != nil {
		t.Fatalf("stale lock not broken: %v", err)
	}
}
