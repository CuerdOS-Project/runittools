package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var binPath string

// safeBuf es un buffer seguro para leer mientras un proceso hijo sigue escribiendo.
type safeBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *safeBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TestMain compila runitctl una vez para las pruebas de integración.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "runitctl-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, "runitctl")
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "no compila: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fakeEnv crea un árbol runit temporal (/etc/sv y runsvdir) más un 'sv'
// falso en el PATH. 'sv status X' imprime "run:" salvo para servicios cuyo
// nombre empieza por "down", y cada llamada se registra en sv.log.
type fakeEnv struct {
	root, sv, run, bin, svlog string
}

func newFakeEnv(t *testing.T) *fakeEnv {
	t.Helper()
	root := t.TempDir()
	e := &fakeEnv{root: root, sv: filepath.Join(root, "sv"), run: filepath.Join(root, "run"), bin: filepath.Join(root, "bin"), svlog: filepath.Join(root, "sv.log")}
	for _, d := range []string{e.sv, e.run, e.bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
echo "$1 $2 SVDIR=$SVDIR" >> "` + e.svlog + `"
case "$1" in
  status)
    case "$2" in
      down*) echo "down: $2: 5s, normally up" ;;
      fail*) echo "fail: $2: runsv not running"; exit 1 ;;
      *) echo "run: $2: (pid 123) 45s; run: log: (pid 99) 45s" ;;
    esac ;;
  restart) echo "boom" >&2; exit 1 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(e.bin, "sv"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", e.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NO_COLOR", "1")
	oldSv, oldRun := SvDir, RunsvDir
	SvDir, RunsvDir = e.sv, e.run
	t.Cleanup(func() { SvDir, RunsvDir = oldSv, oldRun })
	return e
}

func (e *fakeEnv) addService(t *testing.T, name string, enabled bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(e.sv, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if enabled {
		if err := os.Symlink(filepath.Join(e.sv, name), filepath.Join(e.run, name)); err != nil {
			t.Fatal(err)
		}
	}
}

// cliEnv devuelve un entorno hermético para el binario: PATH solo con el 'sv'
// falso (sin journalctl/svlogtail del equipo) y rutas apuntando al árbol temporal.
func (e *fakeEnv) cliEnv(extra ...string) []string {
	env := []string{"PATH=" + e.bin, "HOME=" + e.root, "RUNITCTL_SV_DIR=" + e.sv, "RUNITCTL_SVDIR=" + e.run,
		"RUNITCTL_SOCKLOG_DIR=" + filepath.Join(e.root, "no-socklog"), "RUNITCTL_LANG=es", "NO_COLOR=1"}
	return append(env, extra...)
}

// runCLI ejecuta el binario con el entorno falso. Devuelve stdout+stderr y el código de salida.
func (e *fakeEnv) runCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = e.cliEnv("XDG_STATE_HOME=" + filepath.Join(e.root, "state"))
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), code
}

func TestLocalesInSync(t *testing.T) {
	load := func(lang string) map[string]string {
		m, err := loadLocale(lang)
		if err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		return m
	}
	verbs := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z]`)
	es := load("es")
	for lang := range supportedLangs {
		m := load(lang)
		var missing, extra []string
		for k := range es {
			if _, ok := m[k]; !ok {
				missing = append(missing, k)
			}
		}
		for k := range m {
			if _, ok := es[k]; !ok {
				extra = append(extra, k)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing)+len(extra) > 0 {
			t.Errorf("%s: faltan %v, sobran %v", lang, missing, extra)
		}
		for k, v := range m {
			if want := es[k]; len(verbs.FindAllString(v, -1)) != len(verbs.FindAllString(want, -1)) {
				t.Errorf("%s/%s: formato distinto al español: %q vs %q", lang, k, v, want)
			}
		}
	}
}

func TestLocaleFilesAreJSON(t *testing.T) {
	files, _ := filepath.Glob("locales/*.kn")
	if len(files) != len(supportedLangs) {
		t.Fatalf("hay %d locales y %d idiomas soportados", len(files), len(supportedLangs))
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestDetectLang(t *testing.T) {
	t.Setenv("RUNITCTL_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "pt_BR.UTF-8")
	if got := detectLang(""); got != "pt" {
		t.Errorf("LANG=pt_BR → %q", got)
	}
	if got := detectLang("en"); got != "en" {
		t.Errorf("explícito → %q", got)
	}
	t.Setenv("LANG", "C")
	if got := detectLang("xx"); got != "en" {
		t.Errorf("por defecto → %q", got)
	}
	t.Setenv("LANG", "es_HN.UTF-8")
	if got := detectLang(""); got != "es" {
		t.Errorf("LANG=es → %q", got)
	}
	t.Setenv("LC_ALL", "ca_ES.UTF-8") // LC_ALL manda sobre LANG
	if got := detectLang(""); got != "ca" {
		t.Errorf("LC_ALL=ca → %q", got)
	}
}

func TestEnableDisableMask(t *testing.T) {
	e := newFakeEnv(t)
	e.addService(t, "nginx", false)
	initI18n("es")

	if _, ok := enableService("nginx"); !ok || !lexists(filepath.Join(e.run, "nginx")) {
		t.Fatal("enable debería crear el enlace")
	}
	if msg, ok := enableService("nginx"); !ok || msg == "" {
		t.Fatal("enable repetido debería avisar sin error")
	}
	if _, ok := enableService("no-existe"); ok {
		t.Fatal("enable de servicio inexistente debería fallar")
	}

	// enlace roto: debe poder deshabilitarse (antes quedaba "ya deshabilitado")
	if err := os.RemoveAll(filepath.Join(e.sv, "nginx")); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(e.run, "nginx")) || !lexists(filepath.Join(e.run, "nginx")) {
		t.Fatal("precondición: enlace roto")
	}
	if _, ok := disableService("nginx"); !ok || lexists(filepath.Join(e.run, "nginx")) {
		t.Fatal("disable debería eliminar el enlace roto")
	}

	e.addService(t, "sshd", false)
	if _, ok := toggleMask("sshd"); !ok || !exists(filepath.Join(e.sv, "sshd", "down")) {
		t.Fatal("mask debería crear 'down'")
	}
	if _, ok := toggleMask("sshd"); !ok || exists(filepath.Join(e.sv, "sshd", "down")) {
		t.Fatal("segundo toggle debería quitar 'down'")
	}
}

func TestRenderStatusAndSvEnv(t *testing.T) {
	e := newFakeEnv(t)
	initI18n("es")
	e.addService(t, "nginx", true)
	out := renderStatus("nginx")
	for _, want := range []string{"123", "45s"} {
		if !strings.Contains(out, want) {
			t.Errorf("status no contiene %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\033") {
		t.Errorf("con NO_COLOR la salida no debe llevar ANSI: %q", out)
	}
	log, _ := os.ReadFile(e.svlog)
	if !strings.Contains(string(log), "SVDIR="+e.run) {
		t.Errorf("sv debe recibir SVDIR=%s, log: %s", e.run, log)
	}
	if out := renderStatus("downsvc"); strings.Contains(out, "123") {
		t.Errorf("servicio caído no debería mostrar pid:\n%s", out)
	}
}

func TestLoadServicesOrderAndStates(t *testing.T) {
	e := newFakeEnv(t)
	var want []string
	for i := 0; i < 30; i++ { // más que probeWorkers
		name := fmt.Sprintf("svc%02d", i)
		switch i % 3 {
		case 0:
			e.addService(t, name, true)
		case 1:
			name = fmt.Sprintf("down%02d", i)
			e.addService(t, name, true)
		default:
			name = fmt.Sprintf("off%02d", i)
			e.addService(t, name, false)
		}
		want = append(want, name)
	}
	// ruido que no debe aparecer: archivo suelto y nombre inválido
	os.WriteFile(filepath.Join(e.sv, "README"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(e.sv, "-raro"), 0o755)
	sort.Strings(want)

	got, err := loadServices()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d servicios, quiero %d", len(got), len(want))
	}
	for i, s := range got {
		if s.name != want[i] {
			t.Fatalf("orden: pos %d = %s, quiero %s", i, s.name, want[i])
		}
		switch {
		case strings.HasPrefix(s.name, "svc") && (!s.enabled || s.state != stateRunning):
			t.Errorf("%s debería estar corriendo: %+v", s.name, s)
		case strings.HasPrefix(s.name, "down") && s.state != stateStopped:
			t.Errorf("%s debería estar detenido: %+v", s.name, s)
		case strings.HasPrefix(s.name, "off") && s.enabled:
			t.Errorf("%s no debería estar habilitado", s.name)
		}
	}
}

func TestSvActionReportsFailure(t *testing.T) {
	newFakeEnv(t)
	initI18n("es")
	for _, a := range buildActions() {
		if a.key != "restart" {
			continue
		}
		msg, ok := a.run("nginx") // el sv falso falla en restart
		if ok || !strings.Contains(msg, "boom") {
			t.Errorf("restart debería reportar el fallo con la salida de sv, got ok=%v msg=%q", ok, msg)
		}
	}
}

func TestCLIBasics(t *testing.T) {
	e := newFakeEnv(t)
	e.addService(t, "nginx", true)
	e.addService(t, "sshd", false)

	if out, code := e.runCLI(t, "--version"); code != 0 || !strings.HasPrefix(out, "runitctl ") {
		t.Errorf("--version: %q (%d)", out, code)
	}
	out, code := e.runCLI(t, "list-all")
	if code != 0 || !strings.Contains(out, "nginx") || !strings.Contains(out, "sshd") || strings.Contains(out, "\033") {
		t.Errorf("list-all (%d): %q", code, out)
	}
	if out, _ := e.runCLI(t, "search", "NGI"); !strings.Contains(out, "nginx") {
		t.Errorf("search debería ser insensible a mayúsculas: %q", out)
	}
	if out, code := e.runCLI(t, "status", "nginx"); code != 0 || !strings.Contains(out, "123") {
		t.Errorf("status (%d): %q", code, out)
	}
	for _, bad := range [][]string{{"status", "../etc"}, {"status", "a b"}, {"logs", "-x"}} {
		if out, code := e.runCLI(t, bad...); code != 2 && code != 1 {
			t.Errorf("%v: código %d, out %q", bad, code, out)
		}
	}
	if _, code := e.runCLI(t, "status", "../etc"); code != 2 {
		t.Errorf("nombre inválido debe salir con 2, got %d", code)
	}
	if out, code := e.runCLI(t, "inexistente-accion"); code != 2 || !strings.Contains(out, "inexistente-accion") {
		t.Errorf("acción desconocida: (%d) %q", code, out)
	}
}

func TestCLILogsAndGrab(t *testing.T) {
	e := newFakeEnv(t)
	e.addService(t, "nginx", true)
	logDir := filepath.Join(e.sv, "nginx", "log", "main")
	os.MkdirAll(logDir, 0o755)
	data := "@4000000065a1b2c300000001 uno\n@4000000065a1b2c400000001 dos\n@4000000065a1b2c500000001 tres\n"
	os.WriteFile(filepath.Join(logDir, "current"), []byte(data), 0o600)

	out, code := e.runCLI(t, "logs", "nginx", "-n", "2", "-g")
	if code != 0 {
		t.Fatalf("logs (%d): %s", code, out)
	}
	if strings.Contains(out, "@4000000065") || !strings.Contains(out, "dos") || !strings.Contains(out, "tres") || strings.Contains(out, " uno") {
		t.Errorf("debe decodificar TAI64N y respetar -n: %q", out)
	}
	caps, _ := filepath.Glob(filepath.Join(e.root, "state", "runitctl", "logs", "nginx-*.log"))
	if len(caps) != 1 {
		t.Fatalf("esperaba 1 captura, hay %v", caps)
	}
	b, _ := os.ReadFile(caps[0])
	if !strings.Contains(string(b), "tres") || strings.Contains(string(b), "uno") {
		t.Errorf("captura incorrecta: %q", b)
	}
	if st, _ := os.Stat(caps[0]); st.Mode().Perm() != 0o600 {
		t.Errorf("permisos de captura %v", st.Mode().Perm())
	}
	if _, code := e.runCLI(t, "logs", "nginx", "-n", "0"); code != 2 {
		t.Errorf("-n 0 debe fallar con 2, got %d", code)
	}
	if _, code := e.runCLI(t, "logs", "sin-logs"); code != 1 {
		t.Errorf("servicio sin logs debe salir con 1, got %d", code)
	}
}

func TestCLILogsFollowSurvivesRotation(t *testing.T) {
	e := newFakeEnv(t)
	e.addService(t, "nginx", true)
	logDir := filepath.Join(e.sv, "nginx", "log", "main")
	os.MkdirAll(logDir, 0o755)
	cur := filepath.Join(logDir, "current")
	os.WriteFile(cur, []byte("viejo\n"), 0o600)

	cmd := exec.Command(binPath, "logs", "nginx", "-n", "1", "-f")
	cmd.Env = e.cliEnv()
	var out safeBuf
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	time.Sleep(700 * time.Millisecond)

	f, _ := os.OpenFile(cur, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("antes-de-rotar\n")
	f.Close()
	os.Rename(cur, filepath.Join(logDir, "@old.s"))
	os.WriteFile(cur, []byte("despues-de-rotar\n"), 0o600)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "despues-de-rotar") {
		time.Sleep(100 * time.Millisecond)
	}
	cmd.Process.Signal(os.Interrupt)
	cmd.Wait()
	if !strings.Contains(out.String(), "antes-de-rotar") || !strings.Contains(out.String(), "despues-de-rotar") {
		t.Fatalf("el seguimiento debe sobrevivir a la rotación, salida: %q", out.String())
	}
}

func TestSetupLogging(t *testing.T) {
	e := newFakeEnv(t)
	initI18n("es")
	for _, s := range logServices {
		e.addService(t, s, false)
	}
	t.Setenv("SUDO_USER", "")
	oldRun := runCmd
	t.Cleanup(func() { runCmd = oldRun })
	var calls []string
	installed := false
	runCmd = func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch name {
		case "xbps-query":
			if installed {
				return "", nil
			}
			return "", fmt.Errorf("no instalado")
		case "xbps-install":
			installed = true
		}
		return "", nil
	}
	// isVoid exige xbps-install o os-release de Void: simulamos el binario.
	os.WriteFile(filepath.Join(e.bin, "xbps-install"), []byte("#!/bin/sh\n"), 0o755)

	var kinds []string
	if !setupLogging(func(k, m string) { kinds = append(kinds, k) }) {
		t.Fatalf("setup falló: %v", kinds)
	}
	if !strings.Contains(strings.Join(calls, "|"), "xbps-install -Sy socklog-void") {
		t.Errorf("debía instalar el paquete: %v", calls)
	}
	for _, s := range logServices {
		if !lexists(filepath.Join(e.run, s)) {
			t.Errorf("servicio %s no habilitado", s)
		}
	}
	// segunda ejecución: idempotente, no reinstala
	calls = nil
	if !setupLogging(func(k, m string) {}) || strings.Contains(strings.Join(calls, "|"), "xbps-install") {
		t.Errorf("no debería reinstalar: %v", calls)
	}
}

func TestAskYesNoAndElevateArgs(t *testing.T) {
	cases := map[string]bool{"\n": true, "s\n": true, "Sí\n": true, "y\n": true, "n\n": false, "no\n": false, "x\n": false}
	for in, want := range cases {
		if got := askYesNo(strings.NewReader(in), io.Discard, "?", true); got != want {
			t.Errorf("%q → %v, quiero %v", in, got, want)
		}
	}
	if askYesNo(strings.NewReader("\n"), io.Discard, "?", false) {
		t.Error("enter con default=no debe ser no")
	}
	got := elevateArgs("sudo", "/usr/bin/runitctl", []string{"--lang", "en", "tui"})
	if strings.Join(got, " ") != "sudo /usr/bin/runitctl --lang en tui" {
		t.Errorf("elevateArgs: %v", got)
	}
}

func TestStateGlyphAndPaused(t *testing.T) {
	cases := []struct {
		e    serviceEntry
		want string
	}{
		{serviceEntry{enabled: true, state: stateRunning}, "●"},
		{serviceEntry{enabled: true, state: statePaused}, "‖"},
		{serviceEntry{enabled: true, state: stateStopped}, "○"},
		{serviceEntry{enabled: true, state: stateFailed}, "✖"},
		{serviceEntry{enabled: true, state: stateStopped, masked: true}, "⊘"},
		{serviceEntry{enabled: false, masked: true}, "⊘"},
	}
	for _, c := range cases {
		if g, _ := stateGlyph(c.e); g != c.want {
			t.Errorf("%+v → %s, quiero %s", c.e, g, c.want)
		}
	}
	_, st := stateGlyph(serviceEntry{enabled: true, state: stateRunning})
	_, st2 := stateGlyph(serviceEntry{enabled: true, state: stateStopped})
	if st == st2 {
		t.Error("activo y detenido deben tener colores distintos")
	}
}

func TestLogsFlagValidationAndSetupHint(t *testing.T) {
	e := newFakeEnv(t)
	e.addService(t, "nginx", true)
	for _, args := range [][]string{
		{"logs", "nginx", "-i", "-g"},
		{"logs", "nginx", "-i", "-n", "5"},
		{"logs", "nginx", "--log-dir", "/tmp/x"},
	} {
		if out, code := e.runCLI(t, args...); code != 2 {
			t.Errorf("%v: código %d, out %q", args, code, out)
		}
	}
	// Sugerencia de setup-logs en Void sin socklog
	initI18n("es")
	os.WriteFile(filepath.Join(e.bin, "xbps-install"), []byte("#!/bin/sh\n"), 0o755)
	old := SocklogDir
	SocklogDir = filepath.Join(e.root, "no-socklog")
	defer func() { SocklogDir = old }()
	if msg := buildNoLogSourceMessage("nginx", nil); !strings.Contains(msg, "setup-logs") {
		t.Errorf("falta la sugerencia de setup-logs:\n%s", msg)
	}
}

func TestVersionFlagsAndExtraTools(t *testing.T) {
	e := newFakeEnv(t)
	e.addService(t, "nginx", true)
	for _, f := range []string{"-v", "-V", "--version", "version"} {
		if out, code := e.runCLI(t, f); code != 0 || !strings.HasPrefix(out, "runitctl 1.0") {
			t.Errorf("%s: (%d) %q", f, code, out)
		}
	}
	// órdenes extra: llegan a 'sv' con el nombre real
	for action, svCmd := range map[string]string{"term": "term", "int": "interrupt", "usr1": "1", "check": "check"} {
		if out, code := e.runCLI(t, action, "nginx"); code != 0 {
			t.Errorf("%s: (%d) %s", action, code, out)
		}
		log, _ := os.ReadFile(e.svlog)
		if !strings.Contains(string(log), svCmd+" nginx") {
			t.Errorf("%s no llegó a sv como %q: %s", action, svCmd, log)
		}
	}
	if _, code := e.runCLI(t, "term"); code != 1 {
		t.Errorf("term sin servicio debe salir con 1, got %d", code)
	}
	// niveles
	os.MkdirAll(filepath.Join(e.root, "single"), 0o755)
	if out, code := e.runCLI(t, "runlevel"); code != 0 || !strings.Contains(out, "run") || !strings.Contains(out, "single") {
		t.Errorf("runlevel: (%d) %q", code, out)
	}
	if out, code := e.runCLI(t, "doctor"); code != 0 && code != 1 || !strings.Contains(out, "sv") {
		t.Errorf("doctor: (%d) %q", code, out)
	}
}
