package action

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Forest-Isle/daimon/internal/tool"
)

func okFinal(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
	return &tool.ToolResult{Output: "done"}, nil
}

// loadUndoSpec returns the single undo_journal row's spec, failing if the row
// count is not as expected.
func loadUndoSpec(t *testing.T, store *Store, wantRows int) string {
	t.Helper()
	ctx := context.Background()
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM undo_journal`).Scan(&count); err != nil {
		t.Fatalf("count undo_journal: %v", err)
	}
	if count != wantRows {
		t.Fatalf("undo_journal rows = %d, want %d", count, wantRows)
	}
	if wantRows == 0 {
		return ""
	}
	var spec string
	if err := store.db.QueryRowContext(ctx, `SELECT undo_spec FROM undo_journal LIMIT 1`).Scan(&spec); err != nil {
		t.Fatalf("read undo_spec: %v", err)
	}
	return spec
}

func TestInterceptorRecordsFileUndoNewFile(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	path := filepath.Join(t.TempDir(), "new.txt") // does not exist yet
	input, _ := json.Marshal(map[string]any{"path": path, "content": "hello"})

	call := &tool.ToolCall{ToolName: "file_write", Input: string(input), Capabilities: reversibleCaps()}
	res, err := ic.Intercept(context.Background(), call, okFinal)
	if err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if res.Metadata["receipt_id"] == "" {
		t.Fatal("expected receipt_id stamped on result")
	}
	var snap fileUndoSnapshot
	if err := json.Unmarshal([]byte(loadUndoSpec(t, store, 1)), &snap); err != nil {
		t.Fatalf("decode undo spec: %v", err)
	}
	if snap.Op != "restore" || snap.Path != path || snap.Existed {
		t.Fatalf("snapshot = %#v, want restore/new-file (existed=false)", snap)
	}
}

func TestInterceptorRecordsFileUndoOverwrite(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	path := filepath.Join(t.TempDir(), "exists.txt")
	if err := os.WriteFile(path, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"path": path, "old_string": "old", "new_string": "new"})

	call := &tool.ToolCall{ToolName: "file_edit", Input: string(input), Capabilities: reversibleCaps()}
	if _, err := ic.Intercept(context.Background(), call, okFinal); err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	var snap fileUndoSnapshot
	if err := json.Unmarshal([]byte(loadUndoSpec(t, store, 1)), &snap); err != nil {
		t.Fatalf("decode undo spec: %v", err)
	}
	if !snap.Existed || snap.Prev != "old content" || snap.Truncated {
		t.Fatalf("snapshot = %#v, want existed with prev content", snap)
	}
}

func TestInterceptorTruncatesLargeFileUndo(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	path := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", fileUndoMaxBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"path": path, "content": "small"})

	call := &tool.ToolCall{ToolName: "file_write", Input: string(input), Capabilities: reversibleCaps()}
	if _, err := ic.Intercept(context.Background(), call, okFinal); err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	var snap fileUndoSnapshot
	if err := json.Unmarshal([]byte(loadUndoSpec(t, store, 1)), &snap); err != nil {
		t.Fatalf("decode undo spec: %v", err)
	}
	if !snap.Existed || !snap.Truncated || snap.Prev != "" {
		t.Fatalf("snapshot = %#v, want truncated without prev", snap)
	}
}

func TestInterceptorSymlinkNotSnapshotted(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	dir := t.TempDir()
	target := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(target, []byte("out of tree content"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	input, _ := json.Marshal(map[string]any{"path": link, "content": "x"})

	call := &tool.ToolCall{ToolName: "file_write", Input: string(input), Capabilities: reversibleCaps()}
	if _, err := ic.Intercept(context.Background(), call, okFinal); err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	var snap fileUndoSnapshot
	if err := json.Unmarshal([]byte(loadUndoSpec(t, store, 1)), &snap); err != nil {
		t.Fatalf("decode undo spec: %v", err)
	}
	// A symlink must not have its target content captured into the journal.
	if !snap.Truncated || snap.Prev != "" {
		t.Fatalf("snapshot = %#v, want truncated symlink without prev", snap)
	}
}

func TestInterceptorNonFileToolNoUndo(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)

	// world_edit is reversible and governed, but not a file-snapshot tool: it
	// records a trust attempt but no undo row.
	call := &tool.ToolCall{ToolName: "world_edit", Capabilities: reversibleCaps()}
	res, err := ic.Intercept(context.Background(), call, okFinal)
	if err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if res.Metadata["receipt_id"] != "" {
		t.Fatalf("non-file tool should not stamp receipt_id, got %q", res.Metadata["receipt_id"])
	}
	loadUndoSpec(t, store, 0)
}

func TestInterceptorFailedFileWriteNoUndo(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	path := filepath.Join(t.TempDir(), "fail.txt")
	input, _ := json.Marshal(map[string]any{"path": path, "content": "x"})

	failFinal := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		return &tool.ToolResult{Error: "disk full"}, nil
	}
	call := &tool.ToolCall{ToolName: "file_write", Input: string(input), Capabilities: reversibleCaps()}
	if _, err := ic.Intercept(context.Background(), call, failFinal); err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	// A failed execution must not record an undo entry.
	loadUndoSpec(t, store, 0)
}

func TestExecuteUndoRestoresPrev(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	if err := os.WriteFile(path, []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := undoEntryForTest(t, fileUndoSnapshot{Op: "restore", Path: path, Existed: true, Prev: "previous"})

	if err := ExecuteUndo(context.Background(), root, entry); err != nil {
		t.Fatalf("ExecuteUndo() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "previous" {
		t.Fatalf("content = %q, want previous", got)
	}
}

func TestExecuteUndoDeletesCreatedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	if err := os.WriteFile(path, []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := undoEntryForTest(t, fileUndoSnapshot{Op: "restore", Path: path, Existed: false})

	if err := ExecuteUndo(context.Background(), root, entry); err != nil {
		t.Fatalf("ExecuteUndo() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Stat() error = %v, want not exist", err)
	}
}

func TestExecuteUndoDeleteIdempotent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing.txt")
	entry := undoEntryForTest(t, fileUndoSnapshot{Op: "restore", Path: path, Existed: false})

	if err := ExecuteUndo(context.Background(), root, entry); err != nil {
		t.Fatalf("ExecuteUndo() error = %v", err)
	}
}

func TestExecuteUndoRefusesTruncated(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	if err := os.WriteFile(path, []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := undoEntryForTest(t, fileUndoSnapshot{Op: "restore", Path: path, Existed: true, Prev: "previous", Truncated: true})

	if err := ExecuteUndo(context.Background(), root, entry); err == nil {
		t.Fatal("ExecuteUndo() error = nil, want error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "current" {
		t.Fatalf("content = %q, want current", got)
	}
}

func TestExecuteUndoRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	entry := undoEntryForTest(t, fileUndoSnapshot{Op: "restore", Path: link, Existed: true, Prev: "previous"})

	if err := ExecuteUndo(context.Background(), root, entry); err == nil {
		t.Fatal("ExecuteUndo() error = nil, want error")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "target" {
		t.Fatalf("target content = %q, want target", got)
	}
}

func TestExecuteUndoRefusesEscapeRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := undoEntryForTest(t, fileUndoSnapshot{Op: "restore", Path: outside, Existed: true, Prev: "previous"})

	if err := ExecuteUndo(context.Background(), root, entry); err == nil {
		t.Fatal("ExecuteUndo() error = nil, want error")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside" {
		t.Fatalf("outside content = %q, want outside", got)
	}
}

func TestExecuteUndoRefusesIntermediateSymlinkRestore(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "f.txt")
	if err := os.WriteFile(outsidePath, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	entry := undoEntryForTest(t, fileUndoSnapshot{Op: "restore", Path: filepath.Join(link, "f.txt"), Existed: true, Prev: "x"})

	if err := ExecuteUndo(context.Background(), root, entry); err == nil {
		t.Fatal("ExecuteUndo() error = nil, want error")
	}
	got, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "orig" {
		t.Fatalf("outside content = %q, want orig", got)
	}
}

func TestExecuteUndoRefusesIntermediateSymlinkDelete(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "f.txt")
	if err := os.WriteFile(outsidePath, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	entry := undoEntryForTest(t, fileUndoSnapshot{Op: "restore", Path: filepath.Join(link, "f.txt"), Existed: false})

	if err := ExecuteUndo(context.Background(), root, entry); err == nil {
		t.Fatal("ExecuteUndo() error = nil, want error")
	}
	if _, err := os.Stat(outsidePath); err != nil {
		t.Fatalf("outside file stat error = %v, want still exists", err)
	}
}

func TestStoreUndoMarksUndone(t *testing.T) {
	store := openActionTestStore(t)
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	if err := os.WriteFile(path, []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := undoSpecForTest(t, fileUndoSnapshot{Op: "restore", Path: path, Existed: true, Prev: "previous"})
	if err := store.RecordUndo(ctx, UndoRecord{ReceiptID: "r1", ToolName: "file_write", UndoSpec: spec}); err != nil {
		t.Fatalf("RecordUndo() error = %v", err)
	}

	if err := store.Undo(ctx, root, "r1"); err != nil {
		t.Fatalf("Undo() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "previous" {
		t.Fatalf("content = %q, want previous", got)
	}
	entry, err := store.GetUndo(ctx, "r1")
	if err != nil {
		t.Fatalf("GetUndo() error = %v", err)
	}
	if entry.UndoneAt == "" {
		t.Fatal("UndoneAt empty, want mark")
	}
	if err := store.Undo(ctx, root, "r1"); !errors.Is(err, ErrUndoAlreadyDone) {
		t.Fatalf("Undo() error = %v, want ErrUndoAlreadyDone", err)
	}
}

func TestRecordUndoPersistsEpisodeID(t *testing.T) {
	store := openActionTestStore(t)
	ctx := context.Background()
	spec := undoSpecForTest(t, fileUndoSnapshot{Op: "restore", Path: filepath.Join(t.TempDir(), "f.txt"), Existed: false})

	if err := store.RecordUndo(ctx, UndoRecord{ReceiptID: "r1", ToolName: "file_write", UndoSpec: spec, EpisodeID: "ep1"}); err != nil {
		t.Fatalf("RecordUndo() error = %v", err)
	}
	entry, err := store.GetUndo(ctx, "r1")
	if err != nil {
		t.Fatalf("GetUndo() error = %v", err)
	}
	if entry.EpisodeID != "ep1" {
		t.Fatalf("EpisodeID = %q, want ep1", entry.EpisodeID)
	}
}

func TestListUndoableByEpisodeFilters(t *testing.T) {
	store := openActionTestStore(t)
	ctx := context.Background()
	root := t.TempDir()
	spec := undoSpecForTest(t, fileUndoSnapshot{Op: "restore", Path: filepath.Join(root, "f.txt"), Existed: false})

	records := []UndoRecord{
		{ReceiptID: "ep1_old", ToolName: "file_write", UndoSpec: spec, EpisodeID: "ep1"},
		{ReceiptID: "ep1_done", ToolName: "file_write", UndoSpec: spec, EpisodeID: "ep1"},
		{ReceiptID: "ep1_new", ToolName: "file_write", UndoSpec: spec, EpisodeID: "ep1"},
		{ReceiptID: "ep2_one", ToolName: "file_write", UndoSpec: spec, EpisodeID: "ep2"},
		{ReceiptID: "no_episode", ToolName: "file_write", UndoSpec: spec},
	}
	for _, rec := range records {
		if err := store.RecordUndo(ctx, rec); err != nil {
			t.Fatalf("RecordUndo(%s) error = %v", rec.ReceiptID, err)
		}
	}
	if err := store.MarkUndone(ctx, "ep1_done"); err != nil {
		t.Fatalf("MarkUndone() error = %v", err)
	}

	entries, err := store.ListUndoableByEpisode(ctx, "ep1")
	if err != nil {
		t.Fatalf("ListUndoableByEpisode() error = %v", err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.ReceiptID)
		if entry.EpisodeID != "ep1" {
			t.Fatalf("entry EpisodeID = %q, want ep1", entry.EpisodeID)
		}
		if entry.UndoneAt != "" {
			t.Fatalf("entry %s UndoneAt = %q, want empty", entry.ReceiptID, entry.UndoneAt)
		}
	}
	want := map[string]bool{"ep1_old": true, "ep1_new": true}
	if len(got) != len(want) {
		t.Fatalf("receipts = %v, want ep1_old and ep1_new", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("receipts = %v, want only ep1_old and ep1_new", got)
		}
	}

	empty, err := store.ListUndoableByEpisode(ctx, "")
	if err != nil {
		t.Fatalf("ListUndoableByEpisode(empty) error = %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty episode entries = %d, want 0", len(empty))
	}
}

func TestUndoEpisodeReversesAllNewestFirst(t *testing.T) {
	store := openActionTestStore(t)
	ctx := context.Background()
	root := t.TempDir()
	for _, id := range []string{"r1", "r2", "r3"} {
		path := filepath.Join(root, id+".txt")
		if err := os.WriteFile(path, []byte("current"), 0o644); err != nil {
			t.Fatal(err)
		}
		spec := undoSpecForTest(t, fileUndoSnapshot{Op: "restore", Path: path, Existed: true, Prev: "previous" + id})
		if err := store.RecordUndo(ctx, UndoRecord{ReceiptID: id, ToolName: "file_write", UndoSpec: spec, EpisodeID: "ep1"}); err != nil {
			t.Fatalf("RecordUndo(%s) error = %v", id, err)
		}
	}
	entries, err := store.ListUndoableByEpisode(ctx, "ep1")
	if err != nil {
		t.Fatalf("ListUndoableByEpisode() error = %v", err)
	}
	gotOrder := make([]string, 0, len(entries))
	for _, entry := range entries {
		gotOrder = append(gotOrder, entry.ReceiptID)
	}
	wantOrder := []string{"r3", "r2", "r1"}
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
		}
	}

	reversed, err := store.UndoEpisode(ctx, root, "ep1")
	if err != nil {
		t.Fatalf("UndoEpisode() error = %v", err)
	}
	if reversed != 3 {
		t.Fatalf("reversed = %d, want 3", reversed)
	}
	for _, id := range []string{"r1", "r2", "r3"} {
		got, err := os.ReadFile(filepath.Join(root, id+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "previous"+id {
			t.Fatalf("%s content = %q, want previous%s", id, got, id)
		}
	}
	remaining, err := store.ListUndoableByEpisode(ctx, "ep1")
	if err != nil {
		t.Fatalf("ListUndoableByEpisode() error = %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining undoable = %d, want 0", len(remaining))
	}
}

func TestUndoEpisodePartialFailureReports(t *testing.T) {
	store := openActionTestStore(t)
	ctx := context.Background()
	root := t.TempDir()
	okPath := filepath.Join(root, "ok.txt")
	badPath := filepath.Join(root, "bad.txt")
	if err := os.WriteFile(okPath, []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badPath, []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	okSpec := undoSpecForTest(t, fileUndoSnapshot{Op: "restore", Path: okPath, Existed: true, Prev: "previous"})
	badSpec := undoSpecForTest(t, fileUndoSnapshot{Op: "restore", Path: badPath, Existed: true, Prev: "previous", Truncated: true})
	if err := store.RecordUndo(ctx, UndoRecord{ReceiptID: "ok", ToolName: "file_write", UndoSpec: okSpec, EpisodeID: "ep1"}); err != nil {
		t.Fatalf("RecordUndo(ok) error = %v", err)
	}
	if err := store.RecordUndo(ctx, UndoRecord{ReceiptID: "bad", ToolName: "file_write", UndoSpec: badSpec, EpisodeID: "ep1"}); err != nil {
		t.Fatalf("RecordUndo(bad) error = %v", err)
	}

	reversed, err := store.UndoEpisode(ctx, root, "ep1")
	if err == nil {
		t.Fatal("UndoEpisode() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "content not captured") {
		t.Fatalf("UndoEpisode() error = %v, want content not captured", err)
	}
	if reversed != 1 {
		t.Fatalf("reversed = %d, want 1", reversed)
	}
	got, err := os.ReadFile(okPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "previous" {
		t.Fatalf("ok content = %q, want previous", got)
	}
	okEntry, err := store.GetUndo(ctx, "ok")
	if err != nil {
		t.Fatalf("GetUndo(ok) error = %v", err)
	}
	if okEntry.UndoneAt == "" {
		t.Fatal("ok entry UndoneAt empty, want mark")
	}
	badEntry, err := store.GetUndo(ctx, "bad")
	if err != nil {
		t.Fatalf("GetUndo(bad) error = %v", err)
	}
	if badEntry.UndoneAt != "" {
		t.Fatalf("bad entry UndoneAt = %q, want empty", badEntry.UndoneAt)
	}
}

func TestGetUndoNotFound(t *testing.T) {
	store := openActionTestStore(t)
	if _, err := store.GetUndo(context.Background(), "missing"); !errors.Is(err, ErrUndoNotFound) {
		t.Fatalf("GetUndo() error = %v, want ErrUndoNotFound", err)
	}
}

func undoEntryForTest(t *testing.T, snap fileUndoSnapshot) UndoEntry {
	t.Helper()
	return UndoEntry{ReceiptID: "r1", ToolName: "file_write", UndoSpec: undoSpecForTest(t, snap)}
}

func undoSpecForTest(t *testing.T, snap fileUndoSnapshot) string {
	t.Helper()
	spec, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return string(spec)
}
