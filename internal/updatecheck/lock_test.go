// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package updatecheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func deadPIDForLockTest(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	return pid
}

func writeLockForTest(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "check.lock")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckLockAbandoned(t *testing.T) {
	t.Run("young lock with dead pid is abandoned", func(t *testing.T) {
		path := writeLockForTest(t, t.TempDir(), strconv.Itoa(deadPIDForLockTest(t))+"\n")
		if !checkLockAbandoned(path) {
			t.Fatalf("expected dead-PID lock to be reclaimable")
		}
	})

	t.Run("young lock with live pid is held", func(t *testing.T) {
		path := writeLockForTest(t, t.TempDir(), strconv.Itoa(os.Getpid())+"\n")
		if checkLockAbandoned(path) {
			t.Fatalf("expected live-PID lock to be treated as held")
		}
	})

	t.Run("empty lock is treated as held", func(t *testing.T) {
		path := writeLockForTest(t, t.TempDir(), "")
		if checkLockAbandoned(path) {
			t.Fatalf("expected empty lock to be treated as held")
		}
	})

	t.Run("garbage lock is treated as held", func(t *testing.T) {
		path := writeLockForTest(t, t.TempDir(), "not-a-pid")
		if checkLockAbandoned(path) {
			t.Fatalf("expected garbage lock to be treated as held")
		}
	})

	t.Run("old lock is abandoned regardless of pid", func(t *testing.T) {
		path := writeLockForTest(t, t.TempDir(), strconv.Itoa(os.Getpid())+"\n")
		old := time.Now().Add(-11 * time.Minute)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		if !checkLockAbandoned(path) {
			t.Fatalf("expected old lock to be reclaimable")
		}
	})

	t.Run("missing lock is abandoned", func(t *testing.T) {
		if !checkLockAbandoned(filepath.Join(t.TempDir(), "absent.lock")) {
			t.Fatalf("expected missing lock to be reclaimable")
		}
	})
}

func TestAcquireLockReclaimsDeadOwner(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	lockPath := StatePath() + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(deadPIDForLockTest(t))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unlock, ok := acquireLock()
	if !ok {
		t.Fatalf("expected acquireLock to reclaim a dead-owner lock")
	}
	unlock()
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected lock to be removed after release")
	}
}

func TestAcquireLockExcludesLiveOwner(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	lockPath := StatePath() + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := acquireLock(); ok {
		t.Fatalf("expected acquireLock to refuse while a live owner holds the lock")
	}
}
