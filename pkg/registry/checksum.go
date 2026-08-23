package registry

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

func ModuleChecksum(modulePath string) (string, error) {
	rootInfo, err := os.Lstat(modulePath)
	if err != nil {
		return "", fmt.Errorf("inspect module root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("module root is not a directory")
	}

	paths := make([]string, 0)
	err = filepath.WalkDir(modulePath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == modulePath || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("module contains unsupported file %q", path)
		}
		relative, err := filepath.Rel(modulePath, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk module: %w", err)
	}
	sort.Strings(paths)

	hasher := sha256.New()
	var frame [8]byte
	for _, relative := range paths {
		binary.BigEndian.PutUint64(frame[:], uint64(len(relative)))
		_, _ = hasher.Write(frame[:])
		_, _ = io.WriteString(hasher, relative)

		path := filepath.Join(modulePath, filepath.FromSlash(relative))
		file, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("open module file %q: %w", relative, err)
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return "", fmt.Errorf("inspect module file %q: %w", relative, err)
		}
		if !info.Mode().IsRegular() {
			_ = file.Close()
			return "", fmt.Errorf("module contains unsupported file %q", relative)
		}
		binary.BigEndian.PutUint64(frame[:], uint64(info.Size()))
		_, _ = hasher.Write(frame[:])
		written, copyErr := io.Copy(hasher, io.LimitReader(file, info.Size()+1))
		closeErr := file.Close()
		if copyErr != nil {
			return "", fmt.Errorf("hash module file %q: %w", relative, copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close module file %q: %w", relative, closeErr)
		}
		if written != info.Size() {
			return "", fmt.Errorf("module file %q changed while hashing", relative)
		}
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}
