package soul

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// writeTar streams srcDir into tw. Regular files, directories and symlinks are
// included (symlinks stored as links, never dereferenced); sockets, devices
// and FIFOs are skipped so a live runtime artifact never blocks an export.
// Returns the number of regular files written.
func writeTar(tw *tar.Writer, srcDir string) (int, error) {
	count := 0
	err := filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", path, walkErr)
		}
		if path == srcDir {
			return nil
		}
		wroteFile, err := writeTarEntry(tw, srcDir, path, entry)
		if err != nil {
			return err
		}
		if wroteFile {
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

func writeTarEntry(tw *tar.Writer, srcDir, path string, entry fs.DirEntry) (bool, error) {
	rel, err := filepath.Rel(srcDir, path)
	if err != nil {
		return false, fmt.Errorf("resolve entry path %s: %w", path, err)
	}
	name := filepath.ToSlash(rel)
	if name == manifestName {
		return false, fmt.Errorf("source contains reserved entry name %s: rename it before exporting", manifestName)
	}
	info, err := entry.Info()
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	switch {
	case info.IsDir():
		return false, writeTarHeader(tw, &tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: int64(info.Mode().Perm())})
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return false, fmt.Errorf("read symlink %s: %w", path, err)
		}
		return false, writeTarHeader(tw, &tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777})
	case info.Mode().IsRegular():
		return true, writeTarFile(tw, path, name, info)
	default:
		// Socket/device/FIFO: skipped, not an error.
		return false, nil
	}
}

func writeTarHeader(tw *tar.Writer, hdr *tar.Header) error {
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %s: %w", hdr.Name, err)
	}
	return nil
}

func writeTarFile(tw *tar.Writer, path, name string, info fs.FileInfo) error {
	hdr := &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size()}
	if err := writeTarHeader(tw, hdr); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("copy %s into archive: %w", path, err)
	}
	return nil
}

// extractTar restores the remaining entries of tr into dstDir. Every entry is
// validated before touching the filesystem: absolute or ..-containing names,
// escaping symlink targets, duplicate manifests and unknown entry types all
// abort the import (fail-closed) — a malicious archive must not place a single
// byte outside dstDir.
func extractTar(tr *tar.Reader, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive entry: %w", err)
		}
		if err := extractEntry(tr, dstDir, hdr); err != nil {
			return err
		}
	}
}

func extractEntry(tr *tar.Reader, dstDir string, hdr *tar.Header) error {
	if hdr.Name == manifestName {
		return fmt.Errorf("archive entry %q: duplicate manifest refused", hdr.Name)
	}
	target, err := safeEntryPath(dstDir, hdr.Name)
	if err != nil {
		return err
	}
	if err := ensureRealParentWithin(dstDir, target); err != nil {
		return fmt.Errorf("archive entry %q: %w", hdr.Name, err)
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(target, fs.FileMode(hdr.Mode).Perm()); err != nil {
			return fmt.Errorf("create dir %s: %w", target, err)
		}
		return nil
	case tar.TypeReg:
		return extractFile(tr, target, hdr)
	case tar.TypeSymlink:
		return extractSymlink(dstDir, target, hdr)
	default:
		return fmt.Errorf("archive entry %q: unsupported type %d", hdr.Name, hdr.Typeflag)
	}
}

// ensureRealParentWithin creates target's parent directory and re-verifies its
// resolved (symlink-free) path still sits inside dstDir. A lexically-clean
// entry name can otherwise be redirected outside the root through a symlinked
// parent — planted by an earlier entry of the same archive or pre-existing in
// a --force target. Symlinked parents that resolve inside the root remain
// allowed.
func ensureRealParentWithin(dstDir, target string) error {
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("resolve parent dir: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(dstDir)
	if err != nil {
		return fmt.Errorf("resolve archive root: %w", err)
	}
	if realParent != realRoot && !pathWithin(realRoot, realParent) {
		return fmt.Errorf("parent directory escapes archive root")
	}
	return nil
}

func extractFile(tr *tar.Reader, target string, hdr *tar.Header) error {
	// Never write through a symlink: an entry landing on a link (planted by
	// an earlier entry or pre-existing in a --force target) would redirect
	// the write elsewhere. Replace the link with the file instead.
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(target); err != nil {
			return fmt.Errorf("replace symlink %s: %w", target, err)
		}
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fs.FileMode(hdr.Mode).Perm())
	if err != nil {
		return fmt.Errorf("create file %s: %w", target, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(f, tr); err != nil {
		return fmt.Errorf("write file %s: %w", target, err)
	}
	return nil
}

func extractSymlink(dstDir, target string, hdr *tar.Header) error {
	if filepath.IsAbs(hdr.Linkname) {
		return fmt.Errorf("archive entry %q: absolute symlink target refused", hdr.Name)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(target), hdr.Linkname))
	if !pathWithin(dstDir, resolved) {
		return fmt.Errorf("archive entry %q: symlink target escapes archive root", hdr.Name)
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace existing %s: %w", target, err)
	}
	if err := os.Symlink(hdr.Linkname, target); err != nil {
		return fmt.Errorf("create symlink %s: %w", target, err)
	}
	return nil
}

func safeEntryPath(dstDir, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("archive entry %q: absolute path refused", name)
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == ".." {
			return "", fmt.Errorf("archive entry %q: path traversal refused", name)
		}
	}
	return filepath.Join(dstDir, clean), nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
