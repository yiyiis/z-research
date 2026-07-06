package multiagent

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"

	"z-research/backend/internal/store"
)

// TestSQLiteCheckPointStore_RoundTrip verifies the
// CheckPointStore implementation: Set a blob, Get it
// back, confirm Get returns false for missing IDs.
func TestSQLiteCheckPointStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cp.db")
	st, err := store.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cp, err := NewSQLiteCheckPointStore(ctx, st)
	if err != nil {
		t.Fatalf("NewSQLiteCheckPointStore: %v", err)
	}

	// Get on missing ID → (nil, false, nil).
	got, ok, err := cp.Get(ctx, "missing")
	if err != nil {
		t.Fatalf("Get missing: %v", err)
	}
	if ok || got != nil {
		t.Errorf("expected (nil, false), got (%v, %v)", got, ok)
	}

	// Set then Get.
	want := []byte("hello world")
	if err := cp.Set(ctx, "task-1", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err = cp.Get(ctx, "task-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("blob mismatch: got %q, want %q", got, want)
	}

	// Upsert: Set again with new value, expect latest.
	want2 := []byte("updated")
	if err := cp.Set(ctx, "task-1", want2); err != nil {
		t.Fatalf("Set upsert: %v", err)
	}
	got, _, err = cp.Get(ctx, "task-1")
	if err != nil {
		t.Fatalf("Get after upsert: %v", err)
	}
	if !bytes.Equal(got, want2) {
		t.Errorf("upsert mismatch: got %q, want %q", got, want2)
	}
}

// TestSQLiteCheckPointStore_Concurrent verifies that the
// store is safe for concurrent Set/Get on distinct IDs.
func TestSQLiteCheckPointStore_Concurrent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cp_concurrent.db")
	st, err := store.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cp, err := NewSQLiteCheckPointStore(ctx, st)
	if err != nil {
		t.Fatalf("NewSQLiteCheckPointStore: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Build a fresh per-iteration id and blob.
			// (The previous version reused a shared
			// slice across goroutines, which caused a
			// data race that this test happened to
			// surface.)
			id := "concurrent-task-" + string(rune('0'+i))
			blob := []byte{byte(i), byte(i + 1), byte(i + 2)}
			if err := cp.Set(ctx, id, blob); err != nil {
				t.Errorf("Set %d: %v", i, err)
			}
			got, ok, err := cp.Get(ctx, id)
			if err != nil {
				t.Errorf("Get %d: %v", i, err)
			}
			if !ok {
				t.Errorf("Get %d: ok=false", i)
			}
			if !bytes.Equal(got, blob) {
				t.Errorf("Get %d: blob mismatch", i)
			}
		}()
	}
	wg.Wait()
}

// TestSQLiteCheckPointStore_NilStoreFails verifies the
// error path when the store is nil.
func TestSQLiteCheckPointStore_NilStoreFails(t *testing.T) {
	_, err := NewSQLiteCheckPointStore(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
}
