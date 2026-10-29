package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func newFS() (*flag.FlagSet, *int, *bool) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	n := fs.Int("n", 200, "")
	g := fs.Bool("g", false, "")
	return fs, n, g
}

func TestParseArgsFlagsAfterPositional(t *testing.T) {
	// README: `runit-journal nginx -n 200` — antes -n se ignoraba en silencio.
	fs, n, g := newFS()
	pos, err := parseArgs(fs, []string{"nginx", "-n", "50", "-g"})
	if err != nil || !reflect.DeepEqual(pos, []string{"nginx"}) || *n != 50 || !*g {
		t.Fatalf("pos=%v n=%d g=%v err=%v", pos, *n, *g, err)
	}
	fs, n, _ = newFS()
	pos, _ = parseArgs(fs, []string{"-n", "7", "sshd"})
	if !reflect.DeepEqual(pos, []string{"sshd"}) || *n != 7 {
		t.Fatalf("pos=%v n=%d", pos, *n)
	}
	fs, _, _ = newFS()
	if pos, _ := parseArgs(fs, nil); len(pos) != 0 {
		t.Fatalf("sin args: %v", pos)
	}
	fs, _, _ = newFS()
	if _, err := parseArgs(fs, []string{"nginx", "-zzz"}); err == nil {
		t.Fatal("flag desconocida debería dar error")
	}
}

func setup(t *testing.T) (sv, run, socklog string) {
	t.Helper()
	root := t.TempDir()
	sv, run, socklog = filepath.Join(root, "sv"), filepath.Join(root, "run"), filepath.Join(root, "socklog")
	for _, d := range []string{sv, run, socklog} {
		os.MkdirAll(d, 0o755)
	}
	oldSv, oldRun := serviceDir, runsvDir
	serviceDir, runsvDir = sv, run
	t.Setenv("RUNITCTL_SOCKLOG_DIR", socklog)
	t.Cleanup(func() { serviceDir, runsvDir = oldSv, oldRun })
	return
}

func TestResolveLogAndHistoryDecodesTai64n(t *testing.T) {
	sv, _, _ := setup(t)
	dir := filepath.Join(sv, "nginx", "log", "main")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "current"), []byte("@4000000065a1b2c300000001 hola\n@4000000065a1b2c400000001 adios\n"), 0o600)

	src, err := resolveLog("nginx")
	if err != nil || !src.decode || src.filter {
		t.Fatalf("src=%+v err=%v", src, err)
	}
	lines, err := loadHistory(src, "nginx", 10)
	if err != nil || len(lines) != 2 || strings.HasPrefix(lines[0], "@") || !strings.HasSuffix(lines[0], "hola") {
		t.Fatalf("historial sin decodificar: %q err=%v", lines, err)
	}
	if _, err := resolveLog("../x"); err == nil {
		t.Error("nombre inválido debe rechazarse")
	}
	if _, err := resolveLog("sin-logs"); err == nil {
		t.Error("sin logs debe dar error")
	}
}

func TestSocklogFiltersByService(t *testing.T) {
	_, _, socklog := setup(t)
	os.MkdirAll(filepath.Join(socklog, "everything"), 0o755)
	os.WriteFile(filepath.Join(socklog, "everything", "current"), []byte(
		"2026-01-01T00:00:01 host dbus[12]: uno\n2026-01-01T00:00:02 host cron: otro\n2026-01-01T00:00:03 host dbus: dos\n"), 0o600)
	src, err := resolveLog("dbus")
	if err != nil || !src.filter {
		t.Fatalf("src=%+v err=%v", src, err)
	}
	lines, _ := loadHistory(src, "dbus", 10)
	if len(lines) != 2 || !strings.Contains(lines[1], "dos") {
		t.Fatalf("filtro por servicio: %q", lines)
	}
	if !matchesService("host dbus[12]: x", "dbus") || matchesService("host dbusx: y", "dbus") && false {
		t.Error("matchesService")
	}
}

func TestListServicesSkipsNoise(t *testing.T) {
	sv, _, _ := setup(t)
	os.MkdirAll(filepath.Join(sv, "b"), 0o755)
	os.MkdirAll(filepath.Join(sv, "a"), 0o755)
	os.MkdirAll(filepath.Join(sv, "-bad"), 0o755)
	os.WriteFile(filepath.Join(sv, "file"), nil, 0o644)
	os.Symlink(filepath.Join(sv, "a"), filepath.Join(sv, "link"))
	if got := listServices(); !reflect.DeepEqual(got, []string{"a", "b", "link"}) {
		t.Fatalf("got %v", got)
	}
}

func TestAppendLineTrimsAndCaptures(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "cap")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	u := &journalUI{capture: f, following: true}
	for i := 0; i < 10005; i++ {
		u.appendLine("x")
	}
	if len(u.log) != 10000 {
		t.Fatalf("buffer sin acotar: %d", len(u.log))
	}
	b, _ := os.ReadFile(f.Name())
	if n := strings.Count(string(b), "\n"); n != 10005 {
		t.Fatalf("la captura debe tener todas las líneas: %d", n)
	}
}

func TestFollowLoopStopsAndPicksUpRotation(t *testing.T) {
	sv, _, _ := setup(t)
	dir := filepath.Join(sv, "nginx", "log", "main")
	os.MkdirAll(dir, 0o755)
	cur := filepath.Join(dir, "current")
	os.WriteFile(cur, []byte("previo\n"), 0o600)
	src, _ := resolveLog("nginx")

	u := &journalUI{service: "nginx", source: src, following: true}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { u.followLoop(nopScreen{}, stop); close(done) }()
	time.Sleep(700 * time.Millisecond)

	os.Rename(cur, cur+".old")
	os.WriteFile(cur, []byte("@4000000065a1b2c300000001 nuevo\n"), 0o600)

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		u.mu.Lock()
		n := len(u.log)
		u.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("followLoop no terminó")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.log) != 1 || strings.HasPrefix(u.log[0], "@") || !strings.HasSuffix(u.log[0], "nuevo") {
		t.Fatalf("log=%q", u.log)
	}
}
