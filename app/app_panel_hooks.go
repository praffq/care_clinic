package main

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

// panelHooks replaces what the Updates, Backups and Plugins screens reach
// on the host, for tests. Nil fields mean the real thing.
type panelHooks struct {
	updater func(context.Context) (clinicUpdater, error)
	backups func() (backupService, error)
	event   func(name string, data ...any)
}

// journalPoll is how often a running operation's journal is re-read for
// the live step list.
var journalPoll = 400 * time.Millisecond

// journalSource is what watchJournal reads: the coordinator's Status, which
// reads the state files without its lock.
type journalSource interface {
	Status() (updates.Status, error)
}

// watchJournal emits "update-journal" with the journal (null when none)
// each time it changes, until stop, which emits the final state.
func (a *App) watchJournal(src journalSource) (stop func()) {
	done, finished := make(chan struct{}), make(chan struct{})
	var last []byte
	poll := func() {
		st, err := src.Status()
		if err != nil {
			return
		}
		b, err := json.Marshal(st.Journal)
		if err != nil || bytes.Equal(b, last) {
			return
		}
		last = b
		a.emit("update-journal", st.Journal)
	}
	go func() {
		defer close(finished)
		t := time.NewTicker(journalPoll)
		defer t.Stop()
		for {
			poll()
			select {
			case <-done:
				return
			case <-t.C:
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		poll()
	}
}
