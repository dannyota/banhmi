//go:build linux

package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func cgroupPrerequisite() error {
	return fmt.Errorf("container accounting: requires an empty delegated cgroup v2 subtree with writable cgroup.subtree_control, enabled cpu/memory/pids controllers and memory.peak")
}

func createContainerParent(subtree string) (string, error) {
	if !strings.HasPrefix(subtree, "/sys/fs/cgroup/") || filepath.Clean(subtree) != subtree {
		return "", cgroupPrerequisite()
	}
	// Refuse a regular filesystem or cgroup v1, including an operator typo.
	var stat unix.Statfs_t
	if err := unix.Statfs(subtree, &stat); err != nil || stat.Type != unix.CGROUP2_SUPER_MAGIC {
		return "", cgroupPrerequisite()
	}
	control, err := os.OpenFile(filepath.Join(subtree, "cgroup.subtree_control"), os.O_WRONLY, 0)
	if err != nil {
		return "", cgroupPrerequisite()
	}
	if err := control.Close(); err != nil {
		return "", cgroupPrerequisite()
	}
	data, err := os.ReadFile(filepath.Join(subtree, "cgroup.subtree_control"))
	if err != nil {
		return "", cgroupPrerequisite()
	}
	controllers := " " + strings.Join(strings.Fields(string(data)), " ") + " "
	for _, controller := range []string{"cpu", "memory", "pids"} {
		if !strings.Contains(controllers, " "+controller+" ") {
			return "", cgroupPrerequisite()
		}
	}
	data, err = os.ReadFile(filepath.Join(subtree, "cgroup.procs"))
	if err != nil || len(strings.TrimSpace(string(data))) != 0 {
		return "", cgroupPrerequisite()
	}
	reference, err := cleanupReference()
	if err != nil {
		return "", cgroupPrerequisite()
	}
	parent := filepath.Join(subtree, "corpus-attempt-"+reference)
	err = os.Mkdir(parent, 0700)
	if err != nil {
		return "", cgroupPrerequisite()
	}
	if err := os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"), []byte("+cpu +memory +pids"), 0600); err != nil {
		_ = os.Remove(parent)
		return "", cgroupPrerequisite()
	}
	if _, _, err := cgroupUsage(parent); err != nil {
		_ = os.Remove(parent)
		return "", cgroupPrerequisite()
	}
	return parent, nil
}

func cgroupEmpty(parent string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(parent, "cgroup.events"))
	if err != nil {
		return false, fmt.Errorf("parent cgroup confirmation: UNCONFIRMED")
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "populated" {
			switch fields[1] {
			case "0":
				return true, nil
			case "1":
				return false, nil
			}
		}
	}
	return false, fmt.Errorf("parent cgroup confirmation: UNCONFIRMED")
}

func cleanupReference() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func validCleanupReference(reference string) bool {
	if len(reference) != 32 {
		return false
	}
	for _, c := range reference {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Read bounded kernel metadata only. Errors never expose operator paths.
func cgroupTaskFileEmpty(directory, name string) (bool, error) {
	f, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		return false, fmt.Errorf("cgroup tasks: UNCONFIRMED")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return false, fmt.Errorf("cgroup tasks: UNCONFIRMED")
	}
	return len(strings.TrimSpace(string(data))) == 0, nil
}

func cgroupTasksEmpty(directory string) (bool, error) {
	for _, name := range []string{"cgroup.procs", "cgroup.threads"} {
		empty, err := cgroupTaskFileEmpty(directory, name)
		if err != nil || !empty {
			return false, err
		}
	}
	return true, nil
}

// The attempt owns all descendants in its delegated parent. Cgroupfs has no
// symlinks; refuse them on a substituted filesystem too. Bound enumeration to
// 10,000 directories, 100,000 entries and the shared teardown deadline.
func cgroupDirectories(parent string, deadline time.Time, clock launcherClock) ([]string, error) {
	directories := []string{parent}
	entriesSeen := 0
	for i := 0; i < len(directories); i++ {
		if !clock.Now().Before(deadline) || len(directories) > 10000 {
			return nil, fmt.Errorf("cgroup enumeration: UNCONFIRMED")
		}
		f, err := os.Open(directories[i])
		if err != nil {
			return nil, fmt.Errorf("cgroup enumeration: UNCONFIRMED")
		}
		for {
			entries, readErr := f.ReadDir(64)
			entriesSeen += len(entries)
			if entriesSeen > 100000 || !clock.Now().Before(deadline) || (readErr != nil && readErr != io.EOF) {
				f.Close()
				return nil, fmt.Errorf("cgroup enumeration: UNCONFIRMED")
			}
			for _, entry := range entries {
				if entry.Type()&os.ModeSymlink != 0 {
					f.Close()
					return nil, fmt.Errorf("cgroup enumeration: UNCONFIRMED")
				}
				if entry.IsDir() {
					directories = append(directories, filepath.Join(directories[i], entry.Name()))
				}
			}
			if readErr == io.EOF {
				break
			}
		}
		f.Close()
	}
	// ReadDir on an open directory need not sort. Sort to compare snapshots;
	// descending path order also places children before their own parents.
	slices.Sort(directories)
	return directories, nil
}

func sameDirectories(a, b []string) bool {
	return len(a) > 0 && slices.Equal(a, b)
}
