package graph

import (
	"os"
	"path/filepath"
)

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir, base := filepath.Dir(path), filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_, _ = tmp.Close(), os.Remove(tmpName) // cleanup on the error path
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName) // cleanup on the error path
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		_ = os.Remove(tmpName) // cleanup on the error path
		return err
	}
	return os.Rename(tmpName, path)
}
