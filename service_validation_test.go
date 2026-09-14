package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateServiceName(t *testing.T) {
	for _, name := range []string{"nginx", "api.v2", "my-service_1"} {
		if err := validateServiceName(name); err != nil {
			t.Errorf("%q should be valid: %v", name, err)
		}
	}
	for _, name := range []string{"", "../etc", "a/b", "a b", "-bad", string(make([]byte, 256))} {
		if err := validateServiceName(name); err == nil {
			t.Errorf("%q should be rejected", name)
		}
	}
}

func TestDecodeTai64n(t *testing.T) {
	got := decodeTai64n("@4000000065a1b2c300000001 mensaje")
	if got == "@4000000065a1b2c300000001 mensaje" || got == "" {
		t.Fatalf("timestamp was not decoded: %q", got)
	}
	if got := decodeTai64n("plain line"); got != "plain line" {
		t.Fatalf("plain line changed: %q", got)
	}
}

func TestReadTailLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "current")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readTailLines(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "two" || got[1] != "three" {
		t.Fatalf("unexpected tail: %#v", got)
	}
}
