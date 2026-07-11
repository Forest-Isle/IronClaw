package soul

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPassphrase = "correct horse battery staple"

// seedSourceDir builds a representative state directory: nested dirs, multiple
// files, an executable, and a relative symlink.
func seedSourceDir(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "identity.md"), "I am daimon.", 0o644)
	mustWrite(t, filepath.Join(src, "attention", "rules.yaml"), "rules: []\n", 0o600)
	mustWrite(t, filepath.Join(src, "skills", "active", "greet", "SKILL.md"), "# greet", 0o644)
	mustWrite(t, filepath.Join(src, "bin", "hook.sh"), "#!/bin/sh\necho hi\n", 0o755)
	if err := os.Symlink("../identity.md", filepath.Join(src, "attention", "identity-link")); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}
	return src
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func exportToTemp(t *testing.T, src string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "soul.dsoul")
	if _, err := Export(src, out, testPassphrase); err != nil {
		t.Fatalf("Export: %v", err)
	}
	return out
}

func TestRoundTrip(t *testing.T) {
	src := seedSourceDir(t)
	out := filepath.Join(t.TempDir(), "soul.dsoul")

	manifest, err := Export(src, out, testPassphrase)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if manifest.FileCount != 4 {
		t.Fatalf("FileCount = %d, want 4 regular files", manifest.FileCount)
	}
	if manifest.FormatVersion != manifestFormatVersion {
		t.Fatalf("FormatVersion = %d, want %d", manifest.FormatVersion, manifestFormatVersion)
	}
	if manifest.CreatedAt == "" {
		t.Fatal("CreatedAt empty")
	}

	dst := filepath.Join(t.TempDir(), "restored")
	imported, err := Import(out, dst, testPassphrase, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported.FileCount != manifest.FileCount {
		t.Fatalf("imported FileCount = %d, want %d", imported.FileCount, manifest.FileCount)
	}

	for _, tc := range []struct {
		rel     string
		content string
		mode    os.FileMode
	}{
		{"identity.md", "I am daimon.", 0o644},
		{"attention/rules.yaml", "rules: []\n", 0o600},
		{"skills/active/greet/SKILL.md", "# greet", 0o644},
		{"bin/hook.sh", "#!/bin/sh\necho hi\n", 0o755},
	} {
		path := filepath.Join(dst, filepath.FromSlash(tc.rel))
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read restored %s: %v", tc.rel, err)
		}
		if string(got) != tc.content {
			t.Errorf("%s content = %q, want %q", tc.rel, got, tc.content)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat restored %s: %v", tc.rel, err)
		}
		if info.Mode().Perm() != tc.mode {
			t.Errorf("%s mode = %v, want %v", tc.rel, info.Mode().Perm(), tc.mode)
		}
	}

	link, err := os.Readlink(filepath.Join(dst, "attention", "identity-link"))
	if err != nil {
		t.Fatalf("readlink restored symlink: %v", err)
	}
	if link != "../identity.md" {
		t.Errorf("symlink target = %q, want ../identity.md", link)
	}
}

func TestWrongPassphrase(t *testing.T) {
	out := exportToTemp(t, seedSourceDir(t))
	_, err := Import(out, filepath.Join(t.TempDir(), "dst"), "wrong", false)
	if err == nil || !strings.Contains(err.Error(), "wrong passphrase") {
		t.Fatalf("Import with wrong passphrase: err = %v, want wrong-passphrase error", err)
	}
}

func TestTamperDetection(t *testing.T) {
	out := exportToTemp(t, seedSourceDir(t))
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}

	// Flip last ciphertext byte: GCM auth must fail.
	flipped := append([]byte(nil), raw...)
	flipped[len(flipped)-1] ^= 0xFF
	tampered := filepath.Join(t.TempDir(), "tampered.dsoul")
	if err := os.WriteFile(tampered, flipped, 0o600); err != nil {
		t.Fatalf("write tampered: %v", err)
	}
	if _, err := Import(tampered, filepath.Join(t.TempDir(), "dst1"), testPassphrase, false); err == nil {
		t.Fatal("Import of tampered ciphertext succeeded, want auth failure")
	}

	// Corrupt magic: framing check must fail with a distinct error.
	badMagic := append([]byte(nil), raw...)
	badMagic[0] ^= 0xFF
	notArchive := filepath.Join(t.TempDir(), "notarchive.dsoul")
	if err := os.WriteFile(notArchive, badMagic, 0o600); err != nil {
		t.Fatalf("write bad magic: %v", err)
	}
	_, err = Import(notArchive, filepath.Join(t.TempDir(), "dst2"), testPassphrase, false)
	if err == nil || !strings.Contains(err.Error(), "not a daimon soul archive") {
		t.Fatalf("Import with bad magic: err = %v, want not-an-archive error", err)
	}
}

func TestImportNonEmptyTarget(t *testing.T) {
	out := exportToTemp(t, seedSourceDir(t))
	dst := t.TempDir()
	mustWrite(t, filepath.Join(dst, "existing.txt"), "keep me", 0o644)

	_, err := Import(out, dst, testPassphrase, false)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("Import into non-empty target: err = %v, want --force hint", err)
	}
	// Refusal happens before decryption touches anything: target untouched.
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "existing.txt" {
		t.Fatalf("target modified after refused import: %v", entries)
	}

	if _, err := Import(out, dst, testPassphrase, true); err != nil {
		t.Fatalf("Import with force: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "existing.txt")); err != nil {
		t.Errorf("force import cleared unrelated file (overwrite-not-clear violated): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "identity.md")); err != nil {
		t.Errorf("force import missing restored file: %v", err)
	}
}

// sealMalicious builds an encrypted archive whose tar contains attacker-chosen
// entries, using the package-internal primitives an attacker outside the
// package would replicate.
func sealMalicious(t *testing.T, entries []*tar.Header, bodies map[string]string) string {
	t.Helper()
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	tw := tar.NewWriter(gz)
	if err := writeManifestEntry(tw, Manifest{FormatVersion: manifestFormatVersion}); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for _, hdr := range entries {
		body := ""
		if hdr.Typeflag == tar.TypeReg {
			body = bodies[hdr.Name]
			hdr.Size = int64(len(body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write malicious header: %v", err)
		}
		if body != "" {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatalf("write malicious body: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	path := filepath.Join(t.TempDir(), "malicious.dsoul")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create malicious archive: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := sealArchive(f, testPassphrase, gzBuf.Bytes()); err != nil {
		t.Fatalf("seal malicious archive: %v", err)
	}
	return path
}

func TestMaliciousArchiveRefused(t *testing.T) {
	parent := t.TempDir()
	evilOutside := filepath.Join(parent, "evil.txt")

	cases := []struct {
		name    string
		entries []*tar.Header
		bodies  map[string]string
	}{
		{
			name:    "path traversal",
			entries: []*tar.Header{{Typeflag: tar.TypeReg, Name: "../evil.txt", Mode: 0o644}},
			bodies:  map[string]string{"../evil.txt": "pwned"},
		},
		{
			name:    "absolute path",
			entries: []*tar.Header{{Typeflag: tar.TypeReg, Name: evilOutside, Mode: 0o644}},
			bodies:  map[string]string{evilOutside: "pwned"},
		},
		{
			name:    "escaping symlink",
			entries: []*tar.Header{{Typeflag: tar.TypeSymlink, Name: "link", Linkname: "../../etc", Mode: 0o777}},
		},
		{
			name:    "absolute symlink",
			entries: []*tar.Header{{Typeflag: tar.TypeSymlink, Name: "link", Linkname: "/etc/passwd", Mode: 0o777}},
		},
		{
			name:    "duplicate manifest",
			entries: []*tar.Header{{Typeflag: tar.TypeReg, Name: manifestName, Mode: 0o644}},
			bodies:  map[string]string{manifestName: `{"format_version":1}`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := sealMalicious(t, tc.entries, tc.bodies)
			dst := filepath.Join(parent, "dst-"+strings.ReplaceAll(tc.name, " ", "-"))
			if _, err := Import(archive, dst, testPassphrase, false); err == nil {
				t.Fatal("Import of malicious archive succeeded, want refusal")
			}
			if _, err := os.Stat(evilOutside); !os.IsNotExist(err) {
				t.Fatalf("malicious entry landed outside target: stat err = %v", err)
			}
		})
	}
}

func TestArchiveRejectsInvalidMode(t *testing.T) {
	cases := []struct {
		name     string
		typeflag byte
		entry    string
		mode     int64
	}{
		{name: "regular file negative", typeflag: tar.TypeReg, entry: "negative.txt", mode: -1},
		{name: "regular file oversized", typeflag: tar.TypeReg, entry: "oversized.txt", mode: 1 << 40},
		{name: "directory negative", typeflag: tar.TypeDir, entry: "negative-dir/", mode: -1},
		{name: "directory oversized", typeflag: tar.TypeDir, entry: "oversized-dir/", mode: 1 << 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := sealMalicious(t, []*tar.Header{{
				Typeflag: tc.typeflag,
				Name:     tc.entry,
				Mode:     tc.mode,
			}}, map[string]string{tc.entry: "payload"})
			dst := filepath.Join(t.TempDir(), "dst")

			_, err := Import(archive, dst, testPassphrase, false)
			if err == nil || !strings.Contains(err.Error(), "invalid permission mode") {
				t.Fatalf("Import mode %d: err = %v, want invalid permission mode", tc.mode, err)
			}
			target := filepath.Join(dst, filepath.FromSlash(tc.entry))
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatalf("invalid-mode entry was written: stat err = %v", err)
			}
		})
	}
}

// TestNoWriteThroughSymlink: an archive that plants a symlink and then ships
// a same-named regular file must not have the write follow the link — the
// link is replaced by the file.
func TestNoWriteThroughSymlink(t *testing.T) {
	archive := sealMalicious(t, []*tar.Header{
		{Typeflag: tar.TypeSymlink, Name: "evil", Linkname: "victim.txt", Mode: 0o777},
		{Typeflag: tar.TypeReg, Name: "evil", Mode: 0o644},
	}, map[string]string{"evil": "payload"})
	dst := filepath.Join(t.TempDir(), "dst")
	if _, err := Import(archive, dst, testPassphrase, false); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "victim.txt")); !os.IsNotExist(err) {
		t.Fatalf("write followed symlink to victim.txt: stat err = %v", err)
	}
	info, err := os.Lstat(filepath.Join(dst, "evil"))
	if err != nil {
		t.Fatalf("lstat evil: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("evil is still a symlink, want regular file")
	}
}

// TestForceImportRefusesSymlinkedParentEscape: a --force target containing a
// directory symlink pointing outside the root must not let an entry beneath
// it land outside.
func TestForceImportRefusesSymlinkedParentEscape(t *testing.T) {
	outside := t.TempDir()
	archive := sealMalicious(t, []*tar.Header{
		{Typeflag: tar.TypeReg, Name: "sub/pwn.txt", Mode: 0o644},
	}, map[string]string{"sub/pwn.txt": "pwned"})
	dst := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dst, "sub")); err != nil {
		t.Fatalf("plant symlinked parent: %v", err)
	}
	_, err := Import(archive, dst, testPassphrase, true)
	if err == nil || !strings.Contains(err.Error(), "escapes archive root") {
		t.Fatalf("Import through symlinked parent: err = %v, want escape refusal", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwn.txt")); !os.IsNotExist(err) {
		t.Fatalf("entry landed outside root: stat err = %v", err)
	}
}

// TestSymlinkedParentWithinRootAllowed: symlinked parents that resolve inside
// the root are legitimate archive content and must keep working.
func TestSymlinkedParentWithinRootAllowed(t *testing.T) {
	archive := sealMalicious(t, []*tar.Header{
		{Typeflag: tar.TypeDir, Name: "real/", Mode: 0o755},
		{Typeflag: tar.TypeSymlink, Name: "alias", Linkname: "real", Mode: 0o777},
		{Typeflag: tar.TypeReg, Name: "alias/f.txt", Mode: 0o644},
	}, map[string]string{"alias/f.txt": "ok"})
	dst := filepath.Join(t.TempDir(), "dst")
	if _, err := Import(archive, dst, testPassphrase, false); err != nil {
		t.Fatalf("Import with within-root symlinked parent: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "real", "f.txt"))
	if err != nil {
		t.Fatalf("read through resolved parent: %v", err)
	}
	if string(got) != "ok" {
		t.Fatalf("content = %q, want ok", got)
	}
}

func TestExportRefusesExistingOutput(t *testing.T) {
	src := seedSourceDir(t)
	out := filepath.Join(t.TempDir(), "soul.dsoul")
	mustWrite(t, out, "occupied", 0o644)
	_, err := Export(src, out, testPassphrase)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Export over existing file: err = %v, want already-exists refusal", err)
	}
}

func TestEmptyPassphraseRefused(t *testing.T) {
	src := seedSourceDir(t)
	if _, err := Export(src, filepath.Join(t.TempDir(), "x.dsoul"), ""); err == nil {
		t.Fatal("Export with empty passphrase succeeded")
	}
	out := exportToTemp(t, src)
	if _, err := Import(out, filepath.Join(t.TempDir(), "dst"), "", false); err == nil {
		t.Fatal("Import with empty passphrase succeeded")
	}
}

func TestExportRefusesReservedManifestName(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, manifestName), "{}", 0o644)
	_, err := Export(src, filepath.Join(t.TempDir(), "x.dsoul"), testPassphrase)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("Export with reserved name in source: err = %v, want reserved-name refusal", err)
	}
}
