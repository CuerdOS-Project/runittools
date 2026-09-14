package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateServiceName(t *testing.T) {
	if err := validateServiceName("nginx"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "../etc", "a/b", "a b"} {
		if err := validateServiceName(name); err == nil {
			t.Errorf("%q should be rejected", name)
		}
	}
}

func TestSafeLogName(t *testing.T) {
	if got := safeLogName("../secret"); got != ".._secret" {
		t.Fatalf("unexpected safe name: %q", got)
	}
}

func TestTailLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "current")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, _, err := tailLines(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "two" || got[1] != "three" {
		t.Fatalf("unexpected tail: %#v", got)
	}
}
