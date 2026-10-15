package mediation

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Walk directory descriptors so renaming or substituting an inbox entry cannot redirect a read.
func trustedDirectory(path string, inbox bool, gid uint32) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, errors.New("trusted directory must be an absolute clean path")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return -1, fmt.Errorf("unsafe trusted directory: %w", err)
		}
		fd = next
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			unix.Close(fd)
			return -1, err
		}
		ownerOK := st.Uid == 0 || st.Uid == uint32(os.Geteuid())
		writable := st.Mode&0022 != 0
		// Root-owned sticky ancestors protect other users' directory entries (e.g. /tmp).
		stickyRoot := st.Uid == 0 && st.Mode&unix.S_ISVTX != 0 && i < len(parts)-1
		if inbox && i == len(parts)-1 {
			writable = st.Mode&0002 != 0 || (st.Mode&0020 != 0 && st.Gid != gid)
		}
		if !ownerOK || (writable && !stickyRoot) {
			unix.Close(fd)
			return -1, errors.New("trusted directory is controlled by another account")
		}
	}
	return fd, nil
}

func readFileAt(fd int, name string, uid uint32, limit int) ([]byte, error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return nil, errors.New("submission filename must be a single path component")
	}
	filefd, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open regular input: %w", err)
	}
	file := os.NewFile(uintptr(filefd), name)
	defer file.Close()
	var before, after, named unix.Stat_t
	if err := unix.Fstat(filefd, &before); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != uid || before.Nlink != 1 || before.Mode&0022 != 0 || before.Size < 1 || before.Size > int64(limit) {
		return nil, errors.New("input must be a bounded, singly linked regular file owned by the registered account and not writable by others")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if err := unix.Fstat(filefd, &after); err != nil {
		return nil, err
	}
	if err := unix.Fstatat(fd, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, errors.New("input was substituted during capture")
	}
	before.Atim = unix.Timespec{}
	after.Atim = unix.Timespec{}
	named.Atim = unix.Timespec{}
	if before != after || after != named || len(data) > limit || int64(len(data)) != before.Size {
		return nil, errors.New("input changed during capture")
	}
	return data, nil
}

func trustedRead(path string, limit int) ([]byte, error) {
	fd, err := trustedDirectory(filepath.Dir(path), false, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	return readFileAt(fd, filepath.Base(path), uint32(os.Geteuid()), limit)
}

func CaptureSubmission(inbox, name string, uid, gid uint32) ([]byte, error) {
	fd, err := trustedDirectory(inbox, true, gid)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	return readFileAt(fd, name, uid, 64<<10)
}

func writeJSON(path string, data []byte, mode os.FileMode) error {
	fd, err := trustedDirectory(filepath.Dir(path), false, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	// The parent is trusted and cannot be changed by the requester. Rename publishes a complete receipt.
	file, err := os.CreateTemp(filepath.Dir(path), ".publish-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Keep SQLite and its sidecars under the same private directory as trusted registration.
func CheckPrivateState(registry, statePath string) error {
	if !filepath.IsAbs(registry) || !filepath.IsAbs(statePath) || filepath.Dir(registry) != filepath.Dir(statePath) || registry == statePath {
		return errors.New("registry and State Backend require distinct absolute paths in one private directory")
	}
	fd, err := trustedDirectory(filepath.Dir(registry), false, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 {
		return errors.New("trusted registration and State Backend directory must be owner-only")
	}
	var stateInfo unix.Stat_t
	if err := unix.Lstat(statePath, &stateInfo); err == nil {
		if stateInfo.Uid != uint32(os.Geteuid()) || stateInfo.Mode&unix.S_IFMT != unix.S_IFREG || stateInfo.Mode&0777 != 0600 || stateInfo.Nlink != 1 {
			return errors.New("existing private State Backend is unsafe")
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}

	return nil
}
