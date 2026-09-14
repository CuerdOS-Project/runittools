package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
)

// ---------------------------------------------------------------------
// Backends de almacenamiento de logs soportados
// ---------------------------------------------------------------------
//
// runitctl no asume un único backend: distintas configuraciones de runit
// guardan los logs de maneras distintas. Se soportan, en orden de
// preferencia:
//
//  1. svlogd/vlogger "clásico": un subservicio log/ (multilog/svlogd)
//     escribiendo en .../log/main/current, casi siempre con marcas de
//     tiempo TAI64N al inicio de cada línea.
//  2. vlogger apuntando a un directorio central (p. ej. /var/log/sv/<svc>
//     o /var/log/<svc>), también con formato TAI64N si usa "svlogd -tt"
//     o TAI64N crudo si usa "svlogd" a secas.
//  3. Convención "runit-journal": un directorio de journal en texto plano
//     ya con timestamps legibles (/var/log/runit/<svc>/current), sin
//     necesidad de decodificar TAI64N.
//  4. socklog (https://docs.voidlinux.org/config/services/logging.html):
//     el syslog recomendado por Void Linux. No guarda un archivo por
//     servicio, sino por "clase" (facility) en /var/log/socklog/<clase>/
//     current, con el nombre del servicio como tag dentro de cada línea
//     (p. ej. "dbus[1234]: ..."). Si está disponible el binario oficial
//     'svlogtail' (paquete socklog-void) se usa directamente: runitctl
//     ejecuta 'svlogtail' (y 'svlogtail -f' para seguir en vivo) y filtra
//     su salida por el tag del servicio, igual que harías a mano con
//     'svlogtail | grep servicio'. Si 'svlogtail' no está en el PATH pero
//     sí existe /var/log/socklog, se cae a un escaneo manual equivalente
//     de las clases (mismo resultado, un poco más lento).
//  5. Fallback vía syslog genérico: si vlogger está en modo syslog puro y
//     hay journald disponible (poco común en Void, pero posible en
//     entornos híbridos), se puede leer con journalctl filtrando por tag.

type logSourceKind int

const (
	logSourceFile logSourceKind = iota
	logSourceSvlogtail
	logSourceSocklog
	logSourceJournalctl
)

// SocklogDir es la ubicación estándar de socklog-void.
const SocklogDir = "/var/log/socklog"

type logSource struct {
	kind         logSourceKind
	path         string   // para logSourceFile
	socklogFiles []string // para logSourceSocklog: uno o más archivos current a buscar/filtrar
	backend      string   // etiqueta legible del backend detectado
	decode       bool     // si hay que decodificar prefijos TAI64N
	tag          string   // nombre del servicio, usado como filtro/tag
}

// candidateLogPaths enumera, en orden de prioridad, las rutas de archivo
// donde distintos backends suelen dejar el log "current" de un servicio.
// Se cubren varias convenciones de runit (runsvdir en /etc/runit/... o en
// /var/service, subservicio log/main/ clásico o log/ directo) y las rutas
// típicas de vlogger/svlogd y socklog.
func candidateLogPaths(service string) []logSource {
	return []logSource{
		{kind: logSourceFile, path: RunsvDir + "/" + service + "/log/main/current", backend: "svlogd", decode: true},
		{kind: logSourceFile, path: RunsvDir + "/" + service + "/log/current", backend: "svlogd", decode: true},
		{kind: logSourceFile, path: SvDir + "/" + service + "/log/main/current", backend: "svlogd", decode: true},
		{kind: logSourceFile, path: SvDir + "/" + service + "/log/current", backend: "svlogd", decode: true},
		{kind: logSourceFile, path: "/var/service/" + service + "/log/main/current", backend: "svlogd", decode: true},
		{kind: logSourceFile, path: "/var/service/" + service + "/log/current", backend: "svlogd", decode: true},
		{kind: logSourceFile, path: "/var/log/sv/" + service + "/current", backend: "vlogger/svlogd", decode: true},
		{kind: logSourceFile, path: "/var/log/" + service + "/current", backend: "vlogger/svlogd", decode: true},
		{kind: logSourceFile, path: "/var/log/runit/" + service + "/current", backend: "runit-journal", decode: false},
	}
}

// serviceLogDirs devuelve los directorios log/ (sin importar si ya tienen
// contenido) configurados para el servicio, para poder distinguir entre
// "no hay logging configurado" y "logging configurado pero aún sin datos".
func serviceLogDirs(service string) []string {
	candidates := []string{
		RunsvDir + "/" + service + "/log",
		SvDir + "/" + service + "/log",
		"/var/service/" + service + "/log",
	}
	var found []string
	for _, d := range candidates {
		if exists(d) {
			found = append(found, d)
		}
	}
	return found
}

// socklogCandidateFiles devuelve los archivos 'current' de socklog donde
// buscar mensajes de un servicio. Si existe la clase "everything" (la que
// trae por defecto socklog-void y agrupa TODAS las facilities) se usa solo
// esa, ya que evita tener que escanear y fusionar cada clase por separado.
// Si no, se listan todas las clases disponibles, igual que hace la
// herramienta oficial 'svlogtail' (cat /var/log/socklog/*/current).
func socklogCandidateFiles() []string {
	everything := SocklogDir + "/everything/current"
	if exists(everything) {
		return []string{everything}
	}
	entries, err := os.ReadDir(SocklogDir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := SocklogDir + "/" + e.Name() + "/current"
		if exists(p) {
			files = append(files, p)
		}
	}
	return files
}

// serviceTagRegexp compila un patrón que reconoce el nombre del servicio
// como "tag" de syslog dentro de una línea de socklog, tal como lo escribe
// vlogger/socklog: "<hostname> tag[pid]: mensaje" o "<hostname> tag: mensaje".
// Se exige un separador antes (inicio de línea o espacio) para no matchear
// substrings de otras palabras.
func serviceTagRegexp(service string) *regexp.Regexp {
	pattern := `(^|\s)` + regexp.QuoteMeta(service) + `(\[[0-9]+\])?:`
	re, err := regexp.Compile(pattern)
	if err != nil {
		// No debería pasar (QuoteMeta escapa todo lo problemático), pero
		// por robustez caemos a un patrón que nunca matchea.
		return regexp.MustCompile(`\x00NEVER_MATCH\x00`)
	}
	return re
}

// resolveLogSource detecta cuál backend de log está disponible para un
// servicio dado, probando cada candidato en orden y cayendo por último a
// journalctl (útil cuando vlogger está en modo syslog puro). También
// devuelve la lista de rutas probadas, para poder dar un diagnóstico útil
// si no se encuentra nada.
func resolveLogSource(service string) (*logSource, []string) {
	candidates := candidateLogPaths(service)
	tried := make([]string, 0, len(candidates)+2)
	for _, c := range candidates {
		tried = append(tried, c.path)
		if exists(c.path) {
			src := c
			return &src, tried
		}
	}

	if exists(SocklogDir) {
		// Preferimos leer directamente los archivos current: svlogtail puede
		// quedarse siguiendo el stream sin entregar el backlog de forma fiable
		// y algunas versiones cambian el formato del prefijo.
		files := socklogCandidateFiles()
		if len(files) > 0 {
			tried = append(tried, SocklogDir+" (socklog, escaneo manual: "+strings.Join(files, ", ")+")")
			return &logSource{kind: logSourceSocklog, backend: "socklog (archivos current)", tag: service, socklogFiles: files}, tried
		}
		if _, err := exec.LookPath("svlogtail"); err == nil {
			tried = append(tried, "svlogtail | grep "+service+" (socklog)")
			return &logSource{kind: logSourceSvlogtail, backend: "socklog (svlogtail)", tag: service}, tried
		}
		tried = append(tried, SocklogDir+" (existe pero sin clases con 'current')")
	}

	if _, err := exec.LookPath("journalctl"); err == nil {
		return &logSource{kind: logSourceJournalctl, backend: "journal (vlogger→syslog)", tag: service}, tried
	}
	tried = append(tried, "journalctl (no disponible en PATH)")
	return nil, tried
}

// buildNoLogSourceMessage arma un diagnóstico útil cuando no se encontró
// ningún backend de log: distingue si el servicio directamente no tiene
// logging configurado (no existe log/) de si lo tiene pero aún no generó
// el archivo 'current' (p. ej. el subservicio log/ nunca arrancó), y en
// cualquier caso lista las rutas que se probaron.
func buildNoLogSourceMessage(service string, tried []string) string {
	var b strings.Builder
	logDirs := serviceLogDirs(service)
	if len(logDirs) == 0 {
		fmt.Fprintln(&b, T("logs_not_found", service))
		fmt.Fprintln(&b)
		svcPath := SvDir + "/" + service
		fmt.Fprintln(&b, T("logs_hint_no_logdir", svcPath, svcPath))
	} else {
		fmt.Fprintln(&b, T("logs_not_found", service))
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, T("logs_hint_logdir_empty", strings.Join(logDirs, ", "), service))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, T("logs_tried_paths"))
	for _, p := range tried {
		fmt.Fprintf(&b, "  - %s\n", p)
	}
	return b.String()
}

// ---------------------------------------------------------------------
// Decodificación de timestamps TAI64N
// ---------------------------------------------------------------------
//
// svlogd (con -t/-tt) y multilog anteponen a cada línea una marca TAI64N:
// '@' seguido de 24 dígitos hexadecimales (segundos TAI64) y, en la
// variante TAI64N, 8 dígitos hexadecimales más de nanosegundos.

const tai64Offset = int64(0x400000000000000a)

func decodeTai64n(line string) string {
	if len(line) < 25 || line[0] != '@' {
		return line
	}
	secHex := line[1:17]
	rest := line[17:]
	nsHex := ""
	if len(rest) >= 8 {
		maybeNs := rest[:8]
		if isHex(maybeNs) {
			nsHex = maybeNs
			rest = rest[8:]
		}
	}
	if !isHex(secHex) {
		return line
	}
	secRaw, err := strconv.ParseInt(secHex, 16, 64)
	if err != nil {
		return line
	}
	var ns int64
	if nsHex != "" {
		ns, _ = strconv.ParseInt(nsHex, 16, 64)
	}
	unixSec := secRaw - tai64Offset
	t := time.Unix(unixSec, ns).UTC()
	rest = strings.TrimPrefix(rest, " ")
	return t.Format("2006-01-02 15:04:05.000000") + "  " + rest
}

func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

func formatLogLine(raw string, decode bool) string {
	if decode {
		return decodeTai64n(raw)
	}
	return raw
}

// ---------------------------------------------------------------------
// Lectura de las últimas N líneas
// ---------------------------------------------------------------------

// readTailLines lee hasta 'n' líneas finales de un archivo, sin cargar
// archivos enormes completos en memoria: retrocede en bloques desde el
// final hasta acumular suficientes saltos de línea (o llegar al inicio).
func readTailLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()

	const blockSize = int64(64 * 1024)
	const maxScan = int64(8 * 1024 * 1024) // tope de 8MB para no leer logs gigantes enteros

	var data []byte
	pos := size
	newlines := 0
	for pos > 0 && newlines <= n && (size-pos) < maxScan {
		readSize := blockSize
		if readSize > pos {
			readSize = pos
		}
		pos -= readSize
		buf := make([]byte, readSize)
		if _, err := f.ReadAt(buf, pos); err != nil && err != io.EOF {
			return nil, err
		}
		newlines += strings.Count(string(buf), "\n")
		data = append(buf, data...)
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []string{}, nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

func readInitialLines(src *logSource, n int) ([]string, error) {
	switch src.kind {
	case logSourceFile:
		raw, err := readTailLines(src.path, n)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(raw))
		for i, l := range raw {
			out[i] = formatLogLine(l, src.decode)
		}
		return out, nil
	case logSourceSocklog:
		return readSocklogInitialLines(src, n)
	case logSourceSvlogtail:
		return readSvlogtailInitialLines(src, n)
	case logSourceJournalctl:
		out, err := exec.Command("journalctl", "-t", src.tagArg(), "-n", strconv.Itoa(n), "--no-pager", "-o", "short-iso").CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		text := strings.TrimRight(string(out), "\n")
		if text == "" {
			return []string{}, nil
		}
		return strings.Split(text, "\n"), nil
	}
	return nil, fmt.Errorf("backend de logs desconocido")
}

// readSocklogInitialLines lee las últimas líneas de cada archivo de clase
// de socklog, se queda solo con las que mencionan al servicio como tag, y
// fusiona el resultado de todas las clases ordenando por el timestamp que
// socklog ya escribe al inicio de cada línea (formato ISO, ordenable como
// texto), devolviendo como máximo 'n' líneas.
func readSocklogInitialLines(src *logSource, n int) ([]string, error) {
	// Se lee un margen bastante mayor que 'n' por archivo, porque la
	// mayoría de las líneas de cada clase no van a ser del servicio que
	// buscamos (sobre todo si no existe la clase "everything" y hay que
	// escanear varias clases no relacionadas).
	perFile := n * 20
	if perFile < 500 {
		perFile = 500
	}
	tagRe := serviceTagRegexp(src.tag)

	var matched []string
	var readErr error
	for _, path := range src.socklogFiles {
		raw, err := readTailLines(path, perFile)
		if err != nil {
			readErr = err
			continue
		}
		for _, l := range raw {
			if tagRe.MatchString(l) {
				matched = append(matched, l)
			}
		}
	}
	if len(matched) == 0 && readErr != nil {
		return nil, readErr
	}
	sort.Strings(matched)
	if len(matched) > n {
		matched = matched[len(matched)-n:]
	}
	return matched, nil
}

// readSvlogtailInitialLines usa el binario oficial 'svlogtail' de
// socklog-void (equivalente a "svlogtail | grep <servicio>") para obtener
// las líneas históricas y quedarse solo con las del servicio pedido.
// 'svlogtail' sin argumentos ya recorre todas las clases, deduplica
// (sort -u) e incluye los archivos rotados vía el propio script, así que
// no hace falta reimplementar nada de eso.
// readSvlogtailInitialLines no puede usar cmd.Output(): 'svlogtail' sin
// '-f' no termina por sí solo (es, en la práctica, un "tail -f" continuo
// sobre todas las clases de socklog, igual que 'svlogtail -f'), así que
// Output() se quedaría esperando para siempre un cierre de proceso que
// nunca llega. En su lugar se lee la salida por un pipe y se corta la
// captura del historial por inactividad: una vez que 'svlogtail' terminó
// de volcar el backlog existente, deja de escribir líneas nuevas durante
// un rato (hasta que ocurra un evento real), así que un lapso corto sin
// output es una señal fiable de "ya llegamos al final del historial".
func readSvlogtailInitialLines(src *logSource, n int) ([]string, error) {
	cmd := exec.Command("svlogtail")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	tagRe := serviceTagRegexp(src.tag)
	lineCh := make(chan string, 200)
	go func() {
		defer close(lineCh)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
	}()

	const idleTimeout = 500 * time.Millisecond
	const maxWait = 5 * time.Second
	deadline := time.Now().Add(maxWait)

	var matched []string
	rawSeen := 0
	closedEarly := false
readLoop:
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				closedEarly = true
				break readLoop
			}
			rawSeen++
			if tagRe.MatchString(line) {
				matched = append(matched, line)
			}
		case <-time.After(idleTimeout):
			break readLoop
		}
		if time.Now().After(deadline) {
			break readLoop
		}
	}

	_ = cmd.Process.Kill()
	go func() {
		_ = cmd.Wait()
		for range lineCh {
		}
	}()

	// Si 'svlogtail' terminó por su cuenta sin producir ni una sola línea
	// (closedEarly && rawSeen == 0), lo más probable es que haya fallado
	// (permisos, opción no soportada, binario roto, etc.) en vez de
	// simplemente no tener nada que mostrar todavía. En ese caso se
	// reporta el error real (incluyendo stderr, si lo hubo) en lugar de
	// devolver una lista vacía silenciosa.
	if closedEarly && rawSeen == 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		// Un svlogtail válido termina sin salida si todavía no hay registros.
		return []string{}, nil
	}

	if len(matched) > n {
		matched = matched[len(matched)-n:]
	}
	return matched, nil
}

// tagArg expone el service tag usado para filtrar journalctl; lo asigna el
// llamador de resolveLogSource (ver cmdLogs / logViewer).
func (s *logSource) tagArg() string { return s.tag }

// ---------------------------------------------------------------------
// Seguimiento en vivo (follow / -f)
// ---------------------------------------------------------------------

// tailLive sigue la fuente de logs y envía cada línea nueva por 'out',
// formateada según el backend. Se detiene cuando 'stop' se cierra.
func tailLive(src *logSource, out chan<- string, stop <-chan struct{}) {
	switch src.kind {
	case logSourceFile:
		tailFileLive(src.path, src.decode, nil, out, stop)
	case logSourceSocklog:
		tailSocklogLive(src, out, stop)
	case logSourceSvlogtail:
		tailSvlogtailLive(src, out, stop)
	case logSourceJournalctl:
		tailJournalctlLive(src, out, stop)
	}
}

// tailFileLive sigue un único archivo desde su final, opcionalmente
// filtrando cada línea con 'filter' (nil = sin filtro) antes de decodificar
// y enviarla por 'out'. Se usa tanto para el caso simple (un archivo por
// servicio) como, con filtro, para cada clase de socklog.
func tailFileLive(path string, decode bool, filter func(string) bool, out chan<- string, stop <-chan struct{}) {
	f, err := os.Open(path)
	if err != nil {
		out <- fmt.Sprintf("[runitctl] no se pudo abrir %s: %v", path, err)
		return
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil {
		f.Seek(info.Size(), io.SeekStart)
	}
	reader := bufio.NewReader(f)
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			for {
				line, err := reader.ReadString('\n')
				if line != "" {
					trimmed := strings.TrimRight(line, "\n")
					if filter == nil || filter(trimmed) {
						out <- formatLogLine(trimmed, decode)
					}
				}
				if err != nil {
					break
				}
			}
		}
	}
}

// tailSocklogLive sigue en paralelo todos los archivos de clase relevantes
// (uno solo si existe "everything"), filtrando por el tag del servicio, y
// fusiona las líneas nuevas de todos ellos en el mismo canal de salida.
func tailSocklogLive(src *logSource, out chan<- string, stop <-chan struct{}) {
	tagRe := serviceTagRegexp(src.tag)
	filter := func(line string) bool { return tagRe.MatchString(line) }

	var wg sync.WaitGroup
	for _, path := range src.socklogFiles {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			tailFileLive(p, false, filter, out, stop)
		}(path)
	}
	wg.Wait()
}

// tailSvlogtailLive sigue en vivo con 'svlogtail' (bare, sin '-f': la
// propia herramienta de socklog-void ya se comporta como un tail -f
// continuo por diseño, ver comentarios de readSvlogtailInitialLines más
// arriba) y filtra cada línea nueva por el tag del servicio antes de
// enviarla. Si el proceso no arranca o termina por su cuenta sin haber
// producido ni una línea (en vez de ser matado al cerrar el visor), se
// manda un diagnóstico por 'out' en lugar de fallar en silencio, para que
// no se quede el visor esperando para siempre sin explicación.
func tailSvlogtailLive(src *logSource, out chan<- string, stop <-chan struct{}) {
	tagRe := serviceTagRegexp(src.tag)
	// -f sigue desde el final y no duplica el historial que el visor carga
	// por separado mediante readSvlogtailInitialLines.
	cmd := exec.Command("svlogtail", "-f")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		out <- fmt.Sprintf("[runitctl] no se pudo abrir el pipe de 'svlogtail': %v", err)
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		out <- fmt.Sprintf("[runitctl] no se pudo iniciar 'svlogtail': %v", err)
		return
	}
	go func() {
		<-stop
		_ = cmd.Process.Kill()
	}()
	scanner := bufio.NewScanner(stdout)
	rawSeen := 0
	for scanner.Scan() {
		rawSeen++
		line := scanner.Text()
		if tagRe.MatchString(line) {
			out <- line
		}
	}
	_ = cmd.Wait()

	killed := false
	select {
	case <-stop:
		killed = true
	default:
	}
	if !killed && rawSeen == 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "'svlogtail' terminó sin producir salida. Verifica que socklog-void esté instalado y que el usuario tenga permisos sobre /var/log/socklog."
		}
		out <- "[runitctl] " + msg
	}
}

// tailJournalctlLive sigue con 'journalctl -f' filtrado por tag. Igual que
// tailSvlogtailLive, reporta por 'out' si journalctl no pudo arrancar o
// terminó por su cuenta sin producir nada, en vez de dejar el visor
// esperando para siempre sin explicación.
func tailJournalctlLive(src *logSource, out chan<- string, stop <-chan struct{}) {
	cmd := exec.Command("journalctl", "-t", src.tagArg(), "-n", "0", "-f", "--no-pager", "-o", "short-iso")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		out <- fmt.Sprintf("[runitctl] no se pudo abrir el pipe de 'journalctl': %v", err)
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		out <- fmt.Sprintf("[runitctl] no se pudo iniciar 'journalctl': %v", err)
		return
	}
	go func() {
		<-stop
		_ = cmd.Process.Kill()
	}()
	scanner := bufio.NewScanner(stdout)
	rawSeen := 0
	for scanner.Scan() {
		rawSeen++
		out <- scanner.Text()
	}
	_ = cmd.Wait()

	killed := false
	select {
	case <-stop:
		killed = true
	default:
	}
	if !killed && rawSeen == 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "'journalctl -f' terminó sin producir salida."
		}
		out <- "[runitctl] " + msg
	}
}

// ---------------------------------------------------------------------
// Grabación local de logs
// ---------------------------------------------------------------------

// defaultUserLogDir devuelve una carpeta escribible por el usuario actual.
// No usa /var/log porque -g está pensado para funcionar sin privilegios y no
// debe cambiar permisos ni propiedad de los logs del sistema.
func defaultUserLogDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "runitctl", "logs")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "runitctl", "logs")
	}
	return filepath.Join(".", ".runitctl-logs")
}

func safeLogName(service string) string {
	name := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, service)
	if name == "" {
		return "service"
	}
	return name
}

func openLogCapture(service, dir string) (*os.File, error) {
	if dir == "" {
		dir = defaultUserLogDir()
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	stamp := time.Now().Format("20060102-150405")
	path := filepath.Join(dir, safeLogName(service)+"-"+stamp+".log")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(f, "# runitctl capture\n# service: %s\n# started: %s\n\n", service, time.Now().Format(time.RFC3339))
	return f, nil
}

// ---------------------------------------------------------------------
// Comando de CLI: runitctl logs <servicio> [-n N] [-f] [-i] [-g]
// ---------------------------------------------------------------------
//
// Por defecto 'logs' imprime en texto plano: N líneas de historial y,
// con '-f'/'--follow', sigue el log en vivo (Ctrl-C para salir). Con
// '-i'/'--interactive' abre en cambio el visor curses (logViewer, la
// misma pantalla que usa 'runitctl tui'), que permite seguir en vivo,
// pausar (f), navegar por el historial y recargar la fuente de logs (r)
// sin salir del comando.

func cmdLogs(service string, extra []string) {
	if service == "" {
		errorMsg(T("specify_logs"))
		os.Exit(1)
	}
	n := 40
	follow := false
	interactive := false
	grab := false
	logDir := ""
	for i := 0; i < len(extra); i++ {
		switch extra[i] {
		case "-f", "--follow":
			follow = true
		case "-i", "--interactive", "--tui":
			interactive = true
		case "-g", "--grab", "--save":
			grab = true
		case "--log-dir":
			if i+1 >= len(extra) || extra[i+1] == "" {
				errorMsg("--log-dir requiere una carpeta")
				os.Exit(2)
			}
			logDir = extra[i+1]
			i++
		case "-n", "--lines":
			if i+1 >= len(extra) {
				errorMsg("-n/--lines requiere un número")
				os.Exit(2)
			}
			v, err := strconv.Atoi(extra[i+1])
			if err != nil || v < 1 || v > 100000 {
				errorMsg("-n/--lines debe ser un entero entre 1 y 100000")
				os.Exit(2)
			}
			n = v
			i++
		default:
			errorMsg(fmt.Sprintf("opción desconocida para logs: %s", extra[i]))
			os.Exit(2)
		}
	}

	if interactive {
		if runLogsViewerScreen(service) {
			return
		}
		warn(T("logs_interactive_unavailable"))
	}

	src, tried := resolveLogSource(service)
	if src == nil {
		errorMsg(buildNoLogSourceMessage(service, tried))
		os.Exit(1)
	}
	src.tag = service

	info(T("logs_backend_label", src.backend))

	var capture *os.File
	if grab {
		var err error
		capture, err = openLogCapture(service, logDir)
		if err != nil {
			errorMsg(T("logs_save_error", err))
			os.Exit(1)
		}
		defer capture.Close()
		info(T("logs_save_path", capture.Name()))
	}
	writeLine := func(line string) {
		fmt.Println(line)
		if capture != nil {
			_, _ = fmt.Fprintln(capture, line)
			_ = capture.Sync()
		}
	}

	// El historial se imprime antes del seguimiento para todos los backends.
	lines, err := readInitialLines(src, n)
	if err != nil {
		errorMsg(T("logs_read_error", err))
		os.Exit(1)
	}
	if len(lines) == 0 {
		warn(T("logs_empty"))
	}
	for _, l := range lines {
		writeLine(l)
	}

	if !follow {
		return
	}

	stop := make(chan struct{})
	out := make(chan string, 100)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go tailLive(src, out, stop)
	for {
		select {
		case l := <-out:
			writeLine(l)
		case <-signals:
			close(stop)
			if capture != nil {
				_ = capture.Sync()
				info(T("logs_saved", capturePath(capture)))
			}
			return
		}
	}
}

func capturePath(f *os.File) string {
	if f == nil {
		return ""
	}
	return f.Name()
}

// ---------------------------------------------------------------------
// Visor de logs interactivo dentro de la TUI
// ---------------------------------------------------------------------

// logRedrawEvent es un evento tcell "vacío" que solo sirve para despertar
// el bucle principal del visor cuando llega una línea nueva en modo follow.
type logRedrawEvent struct {
	tcell.EventTime
	line string
}

func logViewer(screen tcell.Screen, service string) {
	src, tried := resolveLogSource(service)
	if src == nil {
		showMessage(screen, service, buildNoLogSourceMessage(service, tried))
		return
	}
	src.tag = service

	var lines []string
	var stop chan struct{}
	var newLines chan string
	var reloadErr string

	// startSource (re)detecta el backend de logs y arranca su seguimiento en
	// vivo. Se usa tanto en el arranque del visor como al pulsar 'r' para
	// recargar: vuelve a resolver el backend desde cero (útil si el
	// servicio recién empezó a loguear, rotó el log, o cambió de backend) y
	// descarta todo lo que había en pantalla.
	startSource := func() {
		lines = nil
		reloadErr = ""

		// svlogtail -f sigue desde el final; el historial se carga
		// explícitamente antes para que no quede la pantalla vacía.
		l, err := readInitialLines(src, 1000)
		if err != nil {
			reloadErr = T("logs_read_error", err)
		} else {
			lines = l
		}

		stop = make(chan struct{})
		newLines = make(chan string, 200)
		go tailLive(src, newLines, stop)
		go func(ch <-chan string) {
			for line := range ch {
				// El consumidor real de la línea ocurre en el bucle
				// principal. El evento transporta la línea para no perderla.
				screen.PostEvent(&logRedrawEvent{line: line})
			}
		}(newLines)
	}
	startSource()
	defer func() {
		if stop != nil {
			close(stop)
		}
	}()

	follow := true
	offset := maxInt(0, len(lines)-1)
	autoscroll := true

	for {
		width, height := screen.Size()
		viewRows := maxInt(1, height-3)
		if autoscroll {
			offset = maxInt(0, len(lines)-viewRows)
		}
		offset = minInt(offset, maxInt(0, len(lines)-1))
		offset = maxInt(0, offset)

		screen.Clear()
		followLabel := T("logs_follow_off")
		if follow {
			followLabel = T("logs_follow_on")
		}
		title := fmt.Sprintf(" %s | %s | %s", T("logs_viewer_title", service), T("logs_backend_label", src.backend), followLabel)
		drawText(screen, 0, 0, width, title, styleTitle)

		if reloadErr != "" {
			drawText(screen, 0, 2, width, " "+reloadErr, styleDim)
		} else if len(lines) == 0 {
			drawText(screen, 0, 2, width, " "+T("logs_waiting"), styleDim)
		}
		for row := 0; row < viewRows && row+offset < len(lines); row++ {
			y := 1 + row
			drawText(screen, 0, y, width, " "+lines[row+offset], styleNormal)
		}

		drawText(screen, 0, height-1, width, " "+T("logs_help_bar"), styleFooter)
		screen.Show()

		switch ev := screen.PollEvent().(type) {
		case *logRedrawEvent:
			lines = append(lines, ev.line)
			if len(lines) > 5000 {
				lines = lines[len(lines)-5000:]
			}
			if follow {
				autoscroll = true
			}
		case *tcell.EventResize:
			screen.Sync()
		case *tcell.EventMouse:
			buttons := ev.Buttons()
			if buttons&tcell.WheelUp != 0 {
				offset = maxInt(0, offset-3)
				autoscroll = false
			} else if buttons&tcell.WheelDown != 0 {
				offset = minInt(maxInt(0, len(lines)-1), offset+3)
				if offset >= maxInt(0, len(lines)-viewRows) {
					autoscroll = true
				}
			}
		case *tcell.EventKey:
			switch ev.Key() {
			case tcell.KeyEscape:
				return
			case tcell.KeyUp:
				offset = maxInt(0, offset-1)
				autoscroll = false
			case tcell.KeyDown:
				offset = minInt(maxInt(0, len(lines)-1), offset+1)
			case tcell.KeyPgUp:
				offset = maxInt(0, offset-viewRows)
				autoscroll = false
			case tcell.KeyPgDn:
				offset = minInt(maxInt(0, len(lines)-1), offset+viewRows)
			case tcell.KeyHome:
				offset = 0
				autoscroll = false
			case tcell.KeyEnd:
				offset = maxInt(0, len(lines)-1)
				autoscroll = true
			default:
				switch ev.Rune() {
				case 'q', 'Q':
					return
				case 'f', 'F':
					follow = !follow
					if follow {
						autoscroll = true
					}
				case 'r', 'R':
					close(stop)
					newSrc, newTried := resolveLogSource(service)
					if newSrc == nil {
						reloadErr = buildNoLogSourceMessage(service, newTried)
						lines = nil
						stop = make(chan struct{}) // placeholder: evita doble close() en el defer
						continue
					}
					newSrc.tag = service
					src = newSrc
					startSource()
					offset = maxInt(0, len(lines)-1)
					follow = true
					autoscroll = true
				case 'g':
					offset = 0
					autoscroll = false
				case 'G':
					autoscroll = true
				}
			}
		}
	}
}

// ---------------------------------------------------------------------
// Entrada del visor interactivo desde el comando 'runitctl logs'
// ---------------------------------------------------------------------
//
// runLogsViewerScreen abre la misma pantalla curses (tcell) que usa
// 'runitctl tui' y corre sobre ella el visor de logs (logViewer, ver más
// arriba), que ya soporta seguir en vivo (f), pausar, navegar
// (flechas/PgUp/PgDn/Home/End/g/G) y salir (q/Esc). Reusar tcell en vez de
// dibujar a mano con ANSI evita problemas de renderizado en terminales con
// transparencia/efectos (celdas "en blanco" que dejan ver lo que hay detrás
// de la ventana): tcell gestiona la pantalla completa como corresponde.
//
// Si no se puede inicializar una pantalla (por ejemplo, la salida no es una
// terminal real: pipes, redirecciones, cron), devuelve false para que el
// llamador caiga al modo clásico de impresión en texto plano.
func runLogsViewerScreen(service string) bool {
	screen, err := tcell.NewScreen()
	if err != nil {
		return false
	}
	if err := screen.Init(); err != nil {
		return false
	}
	defer screen.Fini()
	screen.EnableMouse()
	screen.Clear()

	logViewer(screen, service)
	return true
}
