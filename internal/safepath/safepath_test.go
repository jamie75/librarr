package safepath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExistingUnderRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "author", "book.mp3")
	if err := os.MkdirAll(filepath.Dir(nested), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	resolved, err := ExistingUnderRoot(root, nested)
	canonicalRoot, _ := filepath.EvalSymlinks(root)
	want := filepath.Join(canonicalRoot, "author", "book.mp3")
	if err != nil || resolved != want {
		t.Fatalf("resolved = %q, err = %v", resolved, err)
	}

	outside := filepath.Join(t.TempDir(), "outside.mp3")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape.mp3")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ExistingUnderRoot(root, link); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "nested.mp3"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	nestedLinkDir := filepath.Join(root, "linked-dir")
	if err := os.Symlink(outsideDir, nestedLinkDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ExistingUnderRoot(root, filepath.Join(nestedLinkDir, "nested.mp3")); err == nil {
		t.Fatal("expected nested symlink escape to be rejected")
	}
}

func TestUnderRootRejectsTraversalAndAbsoluteEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := UnderRoot(root, filepath.Join(root, "nested", "book.jpg")); err != nil {
		t.Fatalf("valid future path rejected: %v", err)
	}
	if _, err := UnderRoot(root, filepath.Join(root, "..", "outside.jpg")); err == nil {
		t.Fatal("expected traversal escape to be rejected")
	}
	if _, err := UnderRoot(root, "/tmp/outside.jpg"); err == nil {
		t.Fatal("expected absolute escape to be rejected")
	}
	if _, err := UnderRoot(root, filepath.Join(root, "bad\x00name.jpg")); err == nil {
		t.Fatal("expected NUL path to be rejected")
	}
}
