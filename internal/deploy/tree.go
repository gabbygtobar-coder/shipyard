package deploy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var errOutsideSymlink = errors.New("clone contains a symlink that points outside the repository")

func checkTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		target, readErr := os.Readlink(path)
		if readErr != nil {
			return readErr
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		target = filepath.Clean(target)
		rel, relErr := filepath.Rel(root, target)
		if relErr != nil {
			return relErr
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errOutsideSymlink
		}
		return nil
	})
}
