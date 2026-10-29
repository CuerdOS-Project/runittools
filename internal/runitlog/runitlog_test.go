package runitlog

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidateName(t *testing.T) {
	for _, name := range []string{"nginx", "api.v2", "my-service_1", "a", "9lives"} {
		if err := ValidateName(name); err != nil {
			t.Errorf("%q debería ser válido: %v", name, err)
		}
	}
	bad := []string{"", ".", "..", "../etc", "a/b", "a b", "-bad", ".hidden", "a\nb", "a;b", "$(x)", "ñandú", strings.Repeat("a", 256)}
	for _, name := range bad {
		if err := ValidateName(name); err == nil {
			t.Errorf("%q debería rechazarse", name)
		}
	}
}

func TestSafeLogName(t *testing.T) {
	cases := map[string]string{"nginx": "nginx", "a/b c": "a_b_c", "": "service", "x.y-z_1": "x.y-z_1"}
	for in, want := range cases {
		if got := SafeLogName(in); got != want {
			t.Errorf("SafeLogName(%q)=%q, quiero %q", in, got, want)
		}
	}
}

func TestDecodeTai64n(t *testing.T) {
	// 0x4000000065a1b2c3 = TAI de unix 0x65a1b2b9 (restando el offset de 10 s)
	want := time.Unix(0x65a1b2b9, 1).UTC().Format("2006-01-02 15:04:05.000000") + "  mensaje"
	if got := DecodeTai64n("@4000000065a1b2c300000001 mensaje"); got != want {
		t.Fatalf("got %q, quiero %q", got, want)
	}
	for _, in := range []string{
		"plain line",
		"@short",
		"@zzzzzzzzzzzzzzzz00000001 xx",
		"@0000000000000000" + "00000000 x", // anterior al offset TAI → no se decodifica
		"",
	} {
		if got := DecodeTai64n(in); got != in {
			t.Errorf("%q no debería cambiar, got %q", in, got)
		}
	}
	if got := FormatLine("@4000000065a1b2c300000001 x", false); got != "@4000000065a1b2c300000001 x" {
		t.Errorf("decode=false no debe tocar la línea: %q", got)
	}
}

func TestReadTailLines(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	check := func(p string, n int, want []string) {
		t.Helper()
		got, err := ReadTailLines(p, n)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("n=%d got %#v, quiero %#v", n, got, want)
		}
	}
	p := write("a", "one\ntwo\nthree\n")
	check(p, 2, []string{"two", "three"})
	check(p, 10, []string{"one", "two", "three"})
	check(p, 0, []string{})
	check(write("noeol", "one\ntwo"), 5, []string{"one", "two"})
	check(write("empty", ""), 5, []string{})

	// archivo grande (varios bloques): las últimas líneas deben salir completas
	var sb strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&sb, "linea-%06d\n", i)
	}
	big := write("big", sb.String())
	check(big, 3, []string{"linea-019997", "linea-019998", "linea-019999"})

	// línea única mayor que el tope: la primera línea cortada se descarta
	long := write("long", "inicio\n"+strings.Repeat("x", int(tailMaxScan)+100)+"\nfin\n")
	got, err := ReadTailLines(long, 5)
	if err != nil || len(got) != 1 || got[0] != "fin" {
		t.Fatalf("got %d líneas err=%v", len(got), err)
	}

	if _, err := ReadTailLines(filepath.Join(dir, "no-existe"), 3); err == nil {
		t.Error("debería fallar con archivo inexistente")
	}
}

type collector struct {
	mu    sync.Mutex
	lines []string
}

func (c *collector) add(s string) { c.mu.Lock(); c.lines = append(c.lines, s); c.mu.Unlock() }
func (c *collector) get() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

func waitFor(t *testing.T, c *collector, want []string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if reflect.DeepEqual(c.get(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("got %#v, quiero %#v", c.get(), want)
}

func appendTo(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestFollowFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "current")
	appendTo(t, p, "viejo\n") // no debe emitirse: se sigue desde el final

	stop := make(chan struct{})
	done := make(chan struct{})
	c := &collector{}
	go func() { FollowFile(p, 5*time.Millisecond, stop, c.add); close(done) }()
	defer func() {
		close(stop)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("FollowFile no terminó tras cerrar stop")
		}
	}()

	time.Sleep(50 * time.Millisecond) // deja que FollowFile abra el archivo y se ubique al final
	appendTo(t, p, "uno\ndos")
	waitFor(t, c, []string{"uno"}) // "dos" aún incompleta
	appendTo(t, p, "\ntres\n")
	waitFor(t, c, []string{"uno", "dos", "tres"})

	// rotación estilo svlogd: current → @archivado, nuevo current
	appendTo(t, p, "ultima-del-viejo\n")
	if err := os.Rename(p, p+".old"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	appendTo(t, p, "primera-del-nuevo\n")
	waitFor(t, c, []string{"uno", "dos", "tres", "ultima-del-viejo", "primera-del-nuevo"})

	// truncado
	if err := os.WriteFile(p, []byte("tras-truncar\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, c, []string{"uno", "dos", "tres", "ultima-del-viejo", "primera-del-nuevo", "tras-truncar"})
}

func TestOpenCapture(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub", "logs")
	f1, err := OpenCapture("ng/inx", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f1.Close()
	f2, err := OpenCapture("ng/inx", dir) // mismo segundo: no debe fallar
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	if f1.Name() == f2.Name() {
		t.Fatal("las capturas deben tener nombres distintos")
	}
	if !strings.HasPrefix(filepath.Base(f1.Name()), "ng_inx-") {
		t.Errorf("nombre inesperado: %s", f1.Name())
	}
	st, _ := f1.Stat()
	if st.Mode().Perm() != 0o600 {
		t.Errorf("permisos %v, quiero 0600", st.Mode().Perm())
	}
	if d, _ := os.Stat(dir); d.Mode().Perm() != 0o700 {
		t.Errorf("permisos de carpeta %v, quiero 0700", d.Mode().Perm())
	}
}
