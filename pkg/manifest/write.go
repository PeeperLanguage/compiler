package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// WriteFileAtomic stages, syncs, and atomically replaces one file.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", filepath.Base(path), err)
	}
	tempPath, err := stageFile(path, data, mode)
	if err != nil {
		return err
	}
	defer os.Remove(tempPath)
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	return syncDirectory(dir)
}

func stageFile(path string, data []byte, mode os.FileMode) (string, error) {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create temporary file: %w", err)
	}
	tempPath := temp.Name()
	failed := true
	defer func() {
		if failed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("set temporary file mode: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("write temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close temporary file: %w", err)
	}
	failed = false
	return tempPath, nil
}

func syncDirectory(dir string) error {
	// Unix directory sync makes the preceding atomic rename durable across a
	// crash. Windows does not support POSIX directory fsync and may return
	// ERROR_ACCESS_DENIED here, so file contents remain synced but directory
	// entry durability cannot be explicitly requested on that platform.
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open parent directory: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return fmt.Errorf("sync parent directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close parent directory: %w", err)
	}
	return nil
}

type dependencyFileState struct {
	path      string
	data      []byte
	mode      os.FileMode
	exists    bool
	directory bool
}

// SaveDependencyState publishes manifest and lockfile as one returned-error
// transaction. It does not claim crash recovery across both renames.
func SaveDependencyState(projectRoot string, file *File, lock *Lockfile) error {
	manifestData, err := marshalManifest(file)
	if err != nil {
		return err
	}
	lockData, err := marshalLockfile(lock)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(projectRoot, FileName)
	lockPath := filepath.Join(projectRoot, LockfileName)
	manifestState, err := captureDependencyFile(manifestPath, 0o644)
	if err != nil {
		return err
	}
	lockState, err := captureDependencyFile(lockPath, 0o644)
	if err != nil {
		return err
	}
	manifestStage, err := stageFile(manifestPath, manifestData, manifestState.mode)
	if err != nil {
		return err
	}
	defer os.Remove(manifestStage)
	lockStage, err := stageFile(lockPath, lockData, lockState.mode)
	if err != nil {
		return err
	}
	defer os.Remove(lockStage)

	if err := os.Rename(manifestStage, manifestPath); err != nil {
		return fmt.Errorf("publish %s: %w", FileName, err)
	}
	if err := os.Rename(lockStage, lockPath); err != nil {
		return rollbackDependencyState(fmt.Errorf("publish %s: %w", LockfileName, err), manifestState, lockState)
	}
	if err := syncDirectory(projectRoot); err != nil {
		return rollbackDependencyState(err, manifestState, lockState)
	}
	return nil
}

func captureDependencyFile(path string, defaultMode os.FileMode) (dependencyFileState, error) {
	state := dependencyFileState{path: path, mode: defaultMode}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("inspect %s: %w", filepath.Base(path), err)
	}
	state.exists = true
	state.mode = info.Mode().Perm()
	state.directory = info.IsDir()
	if state.directory {
		return state, nil
	}
	state.data, err = os.ReadFile(path)
	if err != nil {
		return state, fmt.Errorf("read %s for rollback: %w", filepath.Base(path), err)
	}
	return state, nil
}

func rollbackDependencyState(publishErr error, states ...dependencyFileState) error {
	errs := []error{publishErr}
	for _, state := range states {
		if state.directory {
			continue
		}
		if !state.exists {
			if err := os.Remove(state.path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("remove new %s during rollback: %w", filepath.Base(state.path), err))
			}
			continue
		}
		stage, err := stageFile(state.path, state.data, state.mode)
		if err != nil {
			errs = append(errs, fmt.Errorf("stage %s rollback: %w", filepath.Base(state.path), err))
			continue
		}
		if err := os.Rename(stage, state.path); err != nil {
			_ = os.Remove(stage)
			errs = append(errs, fmt.Errorf("restore %s: %w", filepath.Base(state.path), err))
		}
	}
	if len(states) > 0 {
		if err := syncDirectory(filepath.Dir(states[0].path)); err != nil {
			errs = append(errs, fmt.Errorf("sync rollback: %w", err))
		}
	}
	return errors.Join(errs...)
}
