// Package soul implements the sovereign soul archive: the whole agent state
// directory packed into one passphrase-encrypted, portable file. The package
// is deliberately ignorant of the archive contents — it moves bytes — so world
// schema changes never couple to it.
package soul

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// manifestName is the fixed first entry of every soul archive.
const manifestName = "MANIFEST.json"

// manifestFormatVersion is the manifest schema version embedded in archives.
const manifestFormatVersion = 1

// Manifest describes an exported soul archive. FileCount counts regular files;
// directories and symlinks are restored on import but not counted.
type Manifest struct {
	FormatVersion int    `json:"format_version"`
	CreatedAt     string `json:"created_at"`
	SourceDir     string `json:"source_dir"`
	FileCount     int    `json:"file_count"`
}

// Export packs srcDir into an encrypted soul archive at outPath. It refuses to
// overwrite an existing outPath and writes via a temp file + rename so a
// failed export never leaves a half-written archive behind.
func Export(srcDir, outPath, passphrase string) (Manifest, error) {
	if passphrase == "" {
		return Manifest{}, fmt.Errorf("passphrase must not be empty")
	}
	info, err := os.Stat(srcDir)
	if err != nil {
		return Manifest{}, fmt.Errorf("stat source dir: %w", err)
	}
	if !info.IsDir() {
		return Manifest{}, fmt.Errorf("source %s is not a directory", srcDir)
	}
	if _, err := os.Stat(outPath); err == nil {
		return Manifest{}, fmt.Errorf("output %s already exists: refusing to overwrite", outPath)
	} else if !os.IsNotExist(err) {
		return Manifest{}, fmt.Errorf("stat output path: %w", err)
	}
	manifest, payload, err := buildPayload(srcDir)
	if err != nil {
		return Manifest{}, err
	}
	if err := writeArchiveFile(outPath, passphrase, payload); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Import restores an encrypted soul archive into dstDir. A non-empty dstDir is
// refused unless force is set; force overwrites into the directory without
// clearing it first — clearing is the more dangerous behavior, so deleting
// stale files is left as an explicit user decision.
func Import(archivePath, dstDir, passphrase string, force bool) (Manifest, error) {
	if passphrase == "" {
		return Manifest{}, fmt.Errorf("passphrase must not be empty")
	}
	if err := ensureImportTarget(dstDir, force); err != nil {
		return Manifest{}, err
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = f.Close() }()
	payload, err := openArchive(f, passphrase)
	if err != nil {
		return Manifest{}, err
	}
	gz, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return Manifest{}, fmt.Errorf("decompress archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	manifest, err := readManifest(tr)
	if err != nil {
		return Manifest{}, err
	}
	if err := extractTar(tr, dstDir); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// buildPayload assembles the gzip(tar) plaintext: manifest first, then the
// directory entries. The file count is taken in a pre-pass so the manifest can
// lead the stream; if the directory changes between passes the export fails
// rather than shipping a manifest that lies.
func buildPayload(srcDir string) (Manifest, []byte, error) {
	absSrc, err := filepath.Abs(srcDir)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("resolve source dir: %w", err)
	}
	preCount, err := countRegularFiles(srcDir)
	if err != nil {
		return Manifest{}, nil, err
	}
	manifest := Manifest{
		FormatVersion: manifestFormatVersion,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		SourceDir:     absSrc,
		FileCount:     preCount,
	}
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	tw := tar.NewWriter(gz)
	if err := writeManifestEntry(tw, manifest); err != nil {
		return Manifest{}, nil, err
	}
	count, err := writeTar(tw, srcDir)
	if err != nil {
		return Manifest{}, nil, err
	}
	if count != preCount {
		return Manifest{}, nil, fmt.Errorf("source directory changed during export (%d files became %d): retry", preCount, count)
	}
	if err := tw.Close(); err != nil {
		return Manifest{}, nil, fmt.Errorf("finalize tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return Manifest{}, nil, fmt.Errorf("finalize gzip: %w", err)
	}
	return manifest, gzBuf.Bytes(), nil
}

func countRegularFiles(srcDir string) (int, error) {
	count := 0
	err := filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", path, walkErr)
		}
		if entry.Type().IsRegular() {
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

func writeManifestEntry(tw *tar.Writer, manifest Manifest) error {
	body, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	hdr := &tar.Header{Typeflag: tar.TypeReg, Name: manifestName, Mode: 0o600, Size: int64(len(body))}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write manifest header: %w", err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("write manifest body: %w", err)
	}
	return nil
}

func writeArchiveFile(outPath, passphrase string, payload []byte) error {
	tmpPath := outPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temp archive: %w", err)
	}
	if err := sealArchive(f, passphrase, payload); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp archive: %w", err)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("finalize archive: %w", err)
	}
	return nil
}

func ensureImportTarget(dstDir string, force bool) error {
	entries, err := os.ReadDir(dstDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read target dir: %w", err)
	}
	if len(entries) > 0 && !force {
		return fmt.Errorf("target %s is not empty: pass --force to import over it (existing files are overwritten, not cleared)", dstDir)
	}
	return nil
}

func readManifest(tr *tar.Reader) (Manifest, error) {
	hdr, err := tr.Next()
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest entry: %w", err)
	}
	if hdr.Name != manifestName {
		return Manifest{}, fmt.Errorf("archive missing %s as first entry", manifestName)
	}
	var manifest Manifest
	if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	if manifest.FormatVersion != manifestFormatVersion {
		return Manifest{}, fmt.Errorf("unsupported manifest format version %d (supported: %d)", manifest.FormatVersion, manifestFormatVersion)
	}
	return manifest, nil
}
