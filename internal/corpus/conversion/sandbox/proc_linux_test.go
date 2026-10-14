//go:build linux

package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestGroupUsageProcessDisappearance(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"1", "2"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []error{syscall.ENOENT, syscall.ESRCH} {
		t.Run(gone.Error(), func(t *testing.T) {
			read := func(path string) ([]byte, error) {
				if path == "/proc/1/stat" {
					return nil, &os.PathError{Op: "read", Path: path, Err: gone}
				}
				if path != "/proc/2/stat" {
					t.Fatalf("unexpected accounting path: %s", path)
				}
				// Four CPU counters total ten USER_HZ ticks; RSS is five pages.
				return []byte("2 (synthetic worker) S 0 42 0 0 0 0 0 0 0 0 3 4 1 2 0 0 1 0 123 0 5\n"), nil
			}
			cpu, memory, err := groupUsageFrom(42, entries, read)
			if err != nil || cpu != 100*time.Millisecond || memory != 5*int64(os.Getpagesize()) {
				t.Fatalf("sample after process disappearance: cpu=%v memory=%d err=%v", cpu, memory, err)
			}
		})
	}
	_, _, err = groupUsageFrom(42, entries, func(path string) ([]byte, error) {
		return nil, &os.PathError{Op: "read", Path: path, Err: syscall.EIO}
	})
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("accounting hid a live-process read failure: %v", err)
	}
}

func TestReadProcIdentityProcessDisappearance(t *testing.T) {
	for _, gone := range []error{syscall.ENOENT, syscall.ESRCH} {
		t.Run(gone.Error(), func(t *testing.T) {
			_, err := readProcIdentityFrom(1, func(path string) ([]byte, error) {
				return nil, &os.PathError{Op: "read", Path: path, Err: gone}
			})
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("disappeared identity was not reported absent: %v", err)
			}
		})
	}
	_, err := readProcIdentityFrom(1, func(path string) ([]byte, error) {
		return nil, &os.PathError{Op: "read", Path: path, Err: syscall.EIO}
	})
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("identity lookup hid a live-process read failure: %v", err)
	}
}
