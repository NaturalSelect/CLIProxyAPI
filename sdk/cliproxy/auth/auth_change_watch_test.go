package auth

import (
	"context"
	"sync"
	"testing"
)

type memoryAuthTestStore struct {
	mu    sync.Mutex
	auths map[string]*Auth
}

func newMemoryAuthTestStore() *memoryAuthTestStore {
	return &memoryAuthTestStore{auths: make(map[string]*Auth)}
}

func (s *memoryAuthTestStore) List(context.Context) ([]*Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make([]*Auth, 0, len(s.auths))
	for _, a := range s.auths {
		res = append(res, a.Clone())
	}
	return res, nil
}

func (s *memoryAuthTestStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auths[auth.ID] = auth.Clone()
	return auth.ID, nil
}

func (s *memoryAuthTestStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.auths, id)
	return nil
}

func TestAuthChangeWatchCoversNativeMutationsAndReload(t *testing.T) {
	store := newMemoryAuthTestStore()
	manager := NewManager(store, nil, nil)
	changes, stop := manager.WatchAuthChanges("watched")
	defer stop()
	other, stopOther := manager.WatchAuthChanges("other")
	defer stopOther()
	assertChanged := func() {
		t.Helper()
		select {
		case <-changes:
		default:
			t.Fatal("native mutation did not notify watcher")
		}
		select {
		case <-other:
			t.Fatal("unrelated watcher notified")
		default:
		}
	}
	auth, err := manager.Register(t.Context(), &Auth{ID: "watched", Provider: "antigravity", Metadata: map[string]any{"access_token": "initial"}})
	if err != nil {
		t.Fatal(err)
	}
	assertChanged()
	next := auth.Clone()
	next.Metadata["access_token"] = "replace"
	auth, err = manager.Update(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	assertChanged()
	// Slow subscribers never block manager mutations and receive a coalesced hint.
	for range 100 {
		auth, err = manager.Update(t.Context(), auth)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(changes) != 1 {
		t.Fatalf("queued %d events, want coalesced notification", len(changes))
	}
	assertChanged()
	manager.Remove(t.Context(), auth.ID)
	assertChanged()
	// Reload invalidates all subscribed snapshots, including currently absent IDs.
	if err = manager.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	default:
		t.Fatal("reload did not invalidate watcher")
	}
	stop()
	stop()
	if _, open := <-changes; open {
		t.Fatal("unsubscribe did not close watcher")
	}
	manager.mu.RLock()
	_, retained := manager.authChangeWatchers["watched"]
	manager.mu.RUnlock()
	if retained {
		t.Fatal("watcher retained after unsubscribe")
	}
}
