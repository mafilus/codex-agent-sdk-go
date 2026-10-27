package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func testMemoryStore(t *testing.T, base string) *MemoryStore {
	t.Helper()
	store, err := NewMemoryStore(base)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func TestMemoryStoreOperations(t *testing.T) {
	store := testMemoryStore(t, filepath.Join(t.TempDir(), "store"))
	require.NoError(t, store.Write("/memories/nested/note", "first"))
	require.NoError(t, store.Append("./memories/nested/note", " second"))
	content, err := store.Read("memories/nested/note")
	require.NoError(t, err)
	require.Equal(t, "first second", content)
	require.NoError(t, store.Rename("memories/nested/note", "memories/renamed/note"))
	names, err := store.List("memories/renamed")
	require.NoError(t, err)
	require.Equal(t, []string{"note"}, names)
	listing, err := store.Read("memories")
	require.NoError(t, err)
	require.Contains(t, listing, "renamed/")
	require.NoError(t, store.Delete("memories/renamed/note"))
	_, err = store.Read("memories/renamed/note")
	require.Error(t, err)
}

func TestMemoryStoreRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	store := testMemoryStore(t, filepath.Join(root, "store"))
	for _, path := range []string{
		"memories/../../store-escape/probe", "memories/../probe", "memories-other/probe", "../memories/probe", "memories/../../../probe",
	} {
		t.Run(path, func(t *testing.T) { require.Error(t, store.Write(path, "blocked")) })
	}
	_, err := os.Stat(filepath.Join(root, "store-escape"))
	require.True(t, os.IsNotExist(err), "rejected writes must not create outside directories")
}

func TestMemoryStoreRejectsExternalSymlinks(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "store")
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.Mkdir(outside, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "note"), []byte("unchanged"), 0o600))
	store := testMemoryStore(t, base)
	if err := os.Symlink(outside, filepath.Join(base, "memories", "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	require.NoError(t, store.Write("memories/local", "local"))
	_, err := store.Read("memories/link/note")
	require.Error(t, err)
	require.Error(t, store.Write("memories/link/note", "changed"))
	require.Error(t, store.Append("memories/link/note", "changed"))
	require.Error(t, store.Delete("memories/link/note"))
	require.Error(t, store.Rename("memories/local", "memories/link/note"))
	require.Error(t, store.Rename("memories/link/note", "memories/copied"))
	_, err = store.List("memories/link")
	require.Error(t, err)
	content, err := os.ReadFile(filepath.Join(outside, "note"))
	require.NoError(t, err)
	require.Equal(t, "unchanged", string(content))
}

func TestMemoryStorePinsRootAcrossReplacement(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "store")
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.Mkdir(outside, 0o700))
	store := testMemoryStore(t, base)
	require.NoError(t, os.Rename(filepath.Join(base, "memories"), filepath.Join(base, "original")))
	if err := os.Symlink(outside, filepath.Join(base, "memories")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	require.NoError(t, store.Write("memories/note", "pinned"))
	_, err := os.Stat(filepath.Join(outside, "note"))
	require.True(t, os.IsNotExist(err))
	content, err := os.ReadFile(filepath.Join(base, "original", "note"))
	require.NoError(t, err)
	require.Equal(t, "pinned", string(content))
}

func TestMemoryStoreRejectsExternalRootSymlink(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "memories")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store, err := NewMemoryStore(base)
	if store != nil {
		require.NoError(t, store.Close())
	}
	require.Error(t, err)
}

func TestPrintStoredMemories(t *testing.T) {
	store := testMemoryStore(t, filepath.Join(t.TempDir(), "store"))
	require.NoError(t, store.Write("memories/nested/note", "nested"))
	require.NoError(t, store.Write("memories/note", " first \n"))
	var output bytes.Buffer
	printStoredMemories(&output, store)
	require.Equal(t, "  nested/\n  note: first\n", output.String())
}

func TestPrintStoredMemoriesRejectsExternalSymlink(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "store")
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("EXTERNAL_CONTENT_MUST_NOT_APPEAR"), 0o600))
	store := testMemoryStore(t, base)
	require.NoError(t, store.Write("memories/safe", "visible"))
	if err := os.Symlink(outside, filepath.Join(base, "memories", "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	var output bytes.Buffer
	printStoredMemories(&output, store)
	require.Contains(t, output.String(), "link: (error reading)")
	require.Contains(t, output.String(), "safe: visible")
	require.NotContains(t, output.String(), "EXTERNAL_CONTENT_MUST_NOT_APPEAR")
}

func TestPrintStoredMemoriesUsesPinnedRoot(t *testing.T) {
	base := t.TempDir()
	store := testMemoryStore(t, base)
	require.NoError(t, store.Write("memories/note", "original"))
	require.NoError(t, os.Rename(filepath.Join(base, "memories"), filepath.Join(base, "saved")))
	require.NoError(t, os.Mkdir(filepath.Join(base, "memories"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(base, "memories", "note"), []byte("replacement"), 0o600))
	var output bytes.Buffer
	printStoredMemories(&output, store)
	require.Equal(t, "  note: original\n", output.String())
}

func TestPrintStoredMemoriesPreservesExactFileNames(t *testing.T) {
	base := t.TempDir()
	store := testMemoryStore(t, base)
	require.NoError(t, os.WriteFile(filepath.Join(base, "memories", "note"), []byte("plain"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "memories", "note "), []byte("spaced"), 0o600))
	var output bytes.Buffer
	printStoredMemories(&output, store)
	require.Equal(t, "  note: plain\n  note : spaced\n", output.String())
}
