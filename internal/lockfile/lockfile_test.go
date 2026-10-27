package lockfile

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestExclusiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "maio.lock")
	l1, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path); err == nil {
		t.Fatal("second lock should fail")
	} else {
		var le *LockedError
		if !errors.As(err, &le) {
			t.Fatalf("expected LockedError, got %T %v", err, err)
		}
	}
	l1.Release()
	l2, err := Acquire(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	l2.Release()
}
