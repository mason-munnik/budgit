package app

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/mason-munnik/budgit/internal/store"
)

// importUndo is a snapshot of budgit.json from just before an import saved.
type importUndo struct {
	before []byte // the file's exact bytes before the import
	after  []byte // the file's SHA-256 right after the import saved
	info   UndoInfo
}

// UndoInfo is what the dashboard's undo bar says about the import.
type UndoInfo struct {
	FileName string `json:"file_name"`
	Imported int    `json:"imported"`
	Matched  int    `json:"matched"`
}

// undoInfo reports the pending undo while budgit.json is still exactly as the
// import left it. Once anything else has written the file the snapshot can
// never be safely restored, so it is dropped. Called with a.mu held.
func (a *App) undoInfo(db *store.DB) *UndoInfo {
	if a.undo == nil {
		return nil
	}
	if !bytes.Equal(db.Fingerprint(), a.undo.after) {
		a.undo = nil
		return nil
	}
	info := a.undo.info
	return &info
}

// UndoImport puts budgit.json back as it was before the latest import, but
// only if nothing has written it since.
func (a *App) UndoImport(req Request) (Dashboard, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	u := a.undo
	if u == nil {
		return Dashboard{}, fmt.Errorf("there is no import to undo")
	}
	a.undo = nil // one way or the other, this snapshot is spent
	if err := store.Restore(a.path, u.before, u.after); err != nil {
		if errors.Is(err, store.ErrChangedOnDisk) {
			return Dashboard{}, fmt.Errorf("budgit.json changed after this import, so undoing it " +
				"would erase newer changes. Nothing was undone")
		}
		return Dashboard{}, err
	}
	db, err := store.Load(a.path)
	if err != nil {
		return Dashboard{}, err
	}
	return a.view(db, req.Month)
}
