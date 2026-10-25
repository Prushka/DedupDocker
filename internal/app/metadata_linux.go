//go:build linux

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func directoryIdentity(info os.FileInfo) (directoryID, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return directoryID{}, false
	}
	return directoryID{device: uint64(stat.Dev), inode: stat.Ino}, true
}

func sameOwnership(expected, actual os.FileInfo) bool {
	a, aOK := expected.Sys().(*syscall.Stat_t)
	b, bOK := actual.Sys().(*syscall.Stat_t)
	return aOK && bOK && a.Uid == b.Uid && a.Gid == b.Gid
}

func preserveCopyMetadata(file *os.File, source os.FileInfo) error {
	stat, ok := source.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine source ownership: %q", source.Name())
	}
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	if !sameOwnership(source, actual) {
		if err := file.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
			return fmt.Errorf("preserve file ownership: %w", err)
		}
	}
	if err := clearInheritedACL(file, false); err != nil {
		return err
	}
	// chown can clear set-ID bits, so apply the mode after ownership.
	return file.Chmod(source.Mode())
}

func clearInheritedACL(file *os.File, directory bool) error {
	// Normalize newly created output permissions without inspecting or rejecting
	// any source attributes. A default ACL must not grant additional access to
	// copied data whose normal permission bits would otherwise keep it private.
	attributes := []string{"system.posix_acl_access"}
	if directory {
		attributes = append(attributes, "system.posix_acl_default")
	}
	for _, attribute := range attributes {
		if err := unix.Fremovexattr(int(file.Fd()), attribute); err != nil && !errors.Is(err, unix.ENODATA) && !errors.Is(err, unix.ENOTSUP) {
			return fmt.Errorf("cannot remove inherited output ACL: %w", err)
		}
	}
	return nil
}

func removeEmptyDirectory(file *fileRecord) error {
	if err := file.verify(); err != nil {
		return err
	}
	parent, err := unix.Open(filepath.Dir(file.path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	// AT_REMOVEDIR cannot unlink a file or symlink substituted after validation,
	// and refuses a directory that gained files. It never removes recursively.
	return unix.Unlinkat(parent, filepath.Base(file.path), unix.AT_REMOVEDIR)
}

func directoryIsMountPoint(path string) (bool, error) {
	var stat unix.Statx_t
	err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT, unix.STATX_BASIC_STATS, &stat)
	if err == nil && stat.Attributes_mask&unix.STATX_ATTR_MOUNT_ROOT != 0 {
		return stat.Attributes&unix.STATX_ATTR_MOUNT_ROOT != 0, nil
	}
	if err != nil && !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EPERM) {
		return false, fmt.Errorf("inspect mount point %q: %w", path, err)
	}
	// Older kernels and container syscall policies may not expose statx mount
	// attributes. mountinfo also identifies bind mounts on the same device.
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, fmt.Errorf("inspect Linux mounts: %w", err)
	}
	return mountInfoContains(string(data), physical), nil
}

func mountInfoContains(data, path string) bool {
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 6 && unescape.Replace(fields[4]) == path {
			return true
		}
	}
	return false
}
