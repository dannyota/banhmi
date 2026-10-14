//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// CloseInheritedDescriptors is the protocol entrypoint's first action. Only
// stdin, stdout and stderr carry protocol bytes. No ExtraFiles are admitted.
// Raw getdents avoids creating Go's poll descriptors during the inventory.
func CloseInheritedDescriptors() error {
	for pass := 0; pass < 2; pass++ {
		fd, err := unix.Open("/proc/self/fd", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("descriptor inventory: UNAVAILABLE")
		}
		var buf [8192]byte
		for {
			n, err := unix.ReadDirent(fd, buf[:])
			if err != nil {
				_ = unix.Close(fd)
				return fmt.Errorf("descriptor inventory: FAILED")
			}
			if n == 0 {
				break
			}
			_, _, names := unix.ParseDirent(buf[:n], -1, nil)
			for _, name := range names {
				inherited, err := strconv.Atoi(name)
				if err != nil || inherited <= 2 || inherited == fd {
					continue
				}
				if pass == 1 {
					_ = unix.Close(fd)
					return fmt.Errorf("descriptor verification: FAILED")
				}
				if err := unix.Close(inherited); err != nil && err != unix.EBADF {
					_ = unix.Close(fd)
					return fmt.Errorf("descriptor closure: FAILED")
				}
			}
		}
		_ = unix.Close(fd)
	}
	return nil
}

// openBeneath fails closed on all errors except ENOSYS, which means the kernel
// lacks openat2. The fallback pins every directory and refuses all symlinks.
func openBeneath(root *os.File, name string) (*os.File, error) {
	if !safePath(name) {
		return nil, &Failure{Cause: UnsafeOutput}
	}
	fd, err := unix.Openat2(int(root.Fd()), name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err == unix.ENOSYS {
		fd, err = openComponents(int(root.Fd()), name)
	}
	if err != nil {
		return nil, &Failure{Cause: UnsafeOutput}
	}
	return os.NewFile(uintptr(fd), "screened-output"), nil
}

func mountID(fd int) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID, &stat); err == nil && stat.Mask&unix.STATX_MNT_ID != 0 {
		return stat.Mnt_id, nil
	}
	data, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(fd))
	if err != nil {
		return 0, fmt.Errorf("mount identity: UNAVAILABLE")
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "mnt_id:" {
			return strconv.ParseUint(fields[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("mount identity: UNAVAILABLE")
}

func openComponents(root int, name string) (int, error) {
	return openComponentsWithMountID(root, name, mountID)
}

func openComponentsWithMountID(root int, name string, readID func(int) (uint64, error)) (int, error) {
	if !safePath(name) {
		return -1, &Failure{Cause: UnsafeOutput}
	}
	rootMount, err := readID(root)
	if err != nil {
		return -1, err
	}
	parent, err := unix.Dup(root)
	if err != nil {
		return -1, err
	}
	unix.CloseOnExec(parent)
	parts := strings.Split(name, "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(parent, part, flags, 0)
		_ = unix.Close(parent)
		if err != nil {
			return -1, err
		}
		id, err := readID(next)
		if err != nil || id != rootMount {
			_ = unix.Close(next)
			return -1, &Failure{Cause: UnsafeOutput}
		}
		parent = next
	}
	return parent, nil
}

func singleRegular(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}

func openChecked(root *os.File, name string, checked os.FileInfo) (*os.File, error) {
	if !singleRegular(checked) {
		return nil, &Failure{Cause: UnsafeOutput}
	}
	f, err := openBeneath(root, name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !singleRegular(info) || !os.SameFile(checked, info) || info.Size() != checked.Size() {
		_ = f.Close()
		return nil, &Failure{Cause: UnsafeOutput}
	}
	return f, nil
}
