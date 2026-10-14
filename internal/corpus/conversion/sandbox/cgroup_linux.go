//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	parent, err := os.MkdirTemp(subtree, "corpus-attempt-")
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
