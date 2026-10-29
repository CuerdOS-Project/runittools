package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"

	"runitctl/internal/runitlog"
)

// version se sobrescribe en la compilación: -ldflags "-X main.version=1.2.3".
var version = "1.0"

// Mismas variables de entorno que runitctl (ver su documentación).
var (
	serviceDir = envOr("RUNITCTL_SV_DIR", "/etc/sv")
	runsvDir   = envOr("RUNITCTL_SVDIR", envOr("SVDIR", "/etc/runit/runsvdir/default"))
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type logEvent struct{ at time.Time }

func (e *logEvent) When() time.Time { return e.at }

type logSource struct {
	paths   []string
	filter  bool // filtrar por servicio (logs compartidos de socklog)
	decode  bool // decodificar prefijos TAI64N de svlogd
	backend string
}

type journalUI struct {
	services   []string
	cursor     int
	offset     int
	hOffset    int
	autoScroll bool
	filter     string
	log        []string
	service    string
	source     logSource
	following  bool
	paused     bool
	mu         sync.Mutex
	capture    *os.File
}

func listServices() []string {
	entries, err := os.ReadDir(serviceDir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if runitlog.ValidateName(e.Name()) != nil {
			continue
		}
		// os.Stat sigue enlaces: /etc/sv suele contener symlinks a directorios.
		if st, err := os.Stat(filepath.Join(serviceDir, e.Name())); err != nil || !st.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func (u *journalUI) visibleServices() []string {
	q := strings.ToLower(u.filter)
	out := []string{}
	for _, s := range u.services {
		if q == "" || strings.Contains(strings.ToLower(s), q) {
			out = append(out, s)
		}
	}
	return out
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func resolveLog(service string) (logSource, error) {
	if err := runitlog.ValidateName(service); err != nil {
		return logSource{}, err
	}
	type cand struct {
		path   string
		decode bool
	}
	classic := []cand{
		{runsvDir + "/" + service + "/log/main/current", true},
		{runsvDir + "/" + service + "/log/current", true},
		{"/var/service/" + service + "/log/main/current", true},
		{"/var/service/" + service + "/log/current", true},
		{serviceDir + "/" + service + "/log/main/current", true},
		{serviceDir + "/" + service + "/log/current", true},
		{"/var/log/sv/" + service + "/current", true},
		{"/var/log/" + service + "/current", true},
		{"/var/log/runit/" + service + "/current", false}, // texto plano con timestamps legibles
	}
	for _, c := range classic {
		if st, err := os.Stat(c.path); err == nil && !st.IsDir() {
			return logSource{paths: []string{c.path}, decode: c.decode, backend: "registro del servicio"}, nil
		}
	}
	root := envOr("RUNITCTL_SOCKLOG_DIR", "/var/log/socklog")
	// "everything" (socklog-void) ya agrupa todas las clases: usarla sola
	// evita duplicar líneas.
	if every := filepath.Join(root, "everything", "current"); fileExists(every) {
		return logSource{paths: []string{every}, filter: true, backend: "registro del sistema (socklog)"}, nil
	}
	entries, err := os.ReadDir(root)
	if err == nil {
		var paths []string
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(root, e.Name(), "current")
			if st, statErr := os.Stat(p); statErr == nil && !st.IsDir() {
				paths = append(paths, p)
			}
		}
		if len(paths) > 0 {
			return logSource{paths: paths, filter: true, backend: "registro del sistema (socklog)"}, nil
		}
	}
	return logSource{}, fmt.Errorf("No encontré registros para %s", service)
}

func matchesService(line, service string) bool {
	if strings.Contains(line, service+":") || strings.Contains(line, service+"[") {
		return true
	}
	fields := strings.Fields(line)
	for _, f := range fields {
		if strings.TrimSuffix(strings.SplitN(f, "[", 2)[0], ":") == service {
			return true
		}
	}
	return false
}

func loadHistory(src logSource, service string, n int) ([]string, error) {
	var lines []string
	var firstErr error
	for _, path := range src.paths {
		part, err := runitlog.ReadTailLines(path, 10000)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		lines = append(lines, part...)
	}
	if len(lines) == 0 && firstErr != nil {
		return nil, firstErr
	}
	sort.Strings(lines)
	out := lines
	if src.filter {
		out = make([]string, 0, n)
		for _, line := range lines {
			if matchesService(line, service) {
				out = append(out, line)
			}
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	if src.decode {
		for i, l := range out {
			out[i] = runitlog.DecodeTai64n(l)
		}
	}
	return out, nil
}

func draw(s tcell.Screen, x, y int, style tcell.Style, text string) {
	for _, r := range text {
		s.SetContent(x, y, r, nil, style)
		x++
	}
}
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (u *journalUI) drawSelect(s tcell.Screen) {
	s.Clear()
	_, h := s.Size()
	title := tcell.StyleDefault.Foreground(tcell.ColorLightCyan).Bold(true)
	normal := tcell.StyleDefault.Foreground(tcell.ColorWhite)
	selected := tcell.StyleDefault.Foreground(tcell.ColorBlack).Background(tcell.ColorLightCyan).Bold(true)
	dim := tcell.StyleDefault.Foreground(tcell.ColorGray)
	draw(s, 1, 0, title, "runit-journal — ver los registros de tus servicios")
	draw(s, 1, 1, dim, "↑/↓ moverse  Enter abrir  / buscar  Esc/q salir")
	draw(s, 1, 2, dim, "Buscando: "+u.filter)
	list := u.visibleServices()
	u.cursor = clamp(u.cursor, 0, len(list)-1)
	rows := h - 5
	if rows < 1 {
		rows = 1
	}
	if u.cursor < u.offset {
		u.offset = u.cursor
	}
	if u.cursor >= u.offset+rows {
		u.offset = u.cursor - rows + 1
	}
	for row := 0; row < rows && u.offset+row < len(list); row++ {
		i := u.offset + row
		st := normal
		if i == u.cursor {
			st = selected
		}
		draw(s, 0, 4+row, st, fmt.Sprintf("%4d  %s", i+1, list[i]))
	}
	if len(list) == 0 {
		draw(s, 1, 4, dim, "No hay servicios con ese nombre.")
	}
	draw(s, 1, h-1, dim, "Enter: ver registros   /: buscar   q/Esc: salir")
	s.Show()
}

func prompt(s tcell.Screen, current string) (string, bool) {
	v := []rune(current)
	for {
		s.Clear()
		draw(s, 1, 1, tcell.StyleDefault.Foreground(tcell.ColorWhite), "Buscar servicio: "+string(v))
		draw(s, 1, 3, tcell.StyleDefault.Foreground(tcell.ColorGray), "Enter: buscar   Esc: cancelar   Retroceso: borrar")
		s.Show()
		ev := s.PollEvent()
		k, ok := ev.(*tcell.EventKey)
		if !ok {
			continue
		}
		switch k.Key() {
		case tcell.KeyEnter:
			return string(v), true
		case tcell.KeyEscape:
			return current, false
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if len(v) > 0 {
				v = v[:len(v)-1]
			}
		default:
			if k.Rune() != 0 {
				v = append(v, k.Rune())
			}
		}
	}
}

func (u *journalUI) drawLog(s tcell.Screen) {
	s.Clear()
	w, h := s.Size()
	title := tcell.StyleDefault.Foreground(tcell.ColorLightCyan).Bold(true)
	dim := tcell.StyleDefault.Foreground(tcell.ColorGray)
	normal := tcell.StyleDefault.Foreground(tcell.ColorWhite)
	view := h - 3
	if view < 1 {
		view = 1
	}
	u.mu.Lock()
	state := "en pausa"
	if u.following && !u.paused {
		state = "en vivo"
	}
	draw(s, 0, 0, title, fmt.Sprintf(" Registros de %s | %s | %s", u.service, u.source.backend, state))
	if u.autoScroll {
		u.offset = max(0, len(u.log)-view)
	}
	u.offset = clamp(u.offset, 0, max(0, len(u.log)-1))
	for i := 0; i < view && u.offset+i < len(u.log); i++ {
		line := []rune(u.log[u.offset+i])
		start := clamp(u.hOffset, 0, max(0, len(line)-1))
		end := start + max(0, w-1)
		if end > len(line) {
			end = len(line)
		}
		if start < len(line) {
			draw(s, 0, 1+i, normal, string(line[start:end]))
		}
	}
	empty := len(u.log) == 0
	u.mu.Unlock()
	if empty {
		draw(s, 1, 2, dim, "Todavía no hay registros; esperando nuevas líneas...")
	}
	draw(s, 0, h-1, dim, " ↑/↓/Pg moverse  ←/→ desplazar  f pausar  r recargar  g/G inicio/final  q/Esc volver")
	if w > 0 {
		s.Show()
	}
}

// appendLine añade una línea nueva al buffer y a la captura. En pausa la
// línea se guarda igualmente (y se captura), solo se congela la vista.
func (u *journalUI) appendLine(line string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.log = append(u.log, line)
	if u.following && !u.paused {
		u.autoScroll = true
	}
	if len(u.log) > 10000 {
		drop := len(u.log) - 10000
		u.log = u.log[drop:]
		if !u.autoScroll {
			u.offset = max(0, u.offset-drop) // mantiene la vista sobre las mismas líneas
		}
	}
	if u.capture != nil {
		_, _ = fmt.Fprintln(u.capture, line)
		_ = u.capture.Sync()
	}
}

// followLoop sigue cada archivo de la fuente en su propia goroutine
// (runitlog.FollowFile: tolera rotación de svlogd, truncado y líneas
// parciales) y avisa a la pantalla para que redibuje.
func (u *journalUI) followLoop(screen tcell.Screen, stop <-chan struct{}) {
	var wg sync.WaitGroup
	for _, path := range u.source.paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			runitlog.FollowFile(p, 300*time.Millisecond, stop, func(line string) {
				if u.source.filter && !matchesService(line, u.service) {
					return
				}
				if u.source.decode {
					line = runitlog.DecodeTai64n(line)
				}
				u.appendLine(line)
				_ = screen.PostEvent(&logEvent{at: time.Now()}) // si la cola está llena ya hay un redibujo pendiente
			})
		}(path)
	}
	wg.Wait()
}

func (u *journalUI) openLog(s tcell.Screen, service string, follow bool, lines int, grab bool, dir string) {
	if err := runitlog.ValidateName(service); err != nil {
		show(s, err.Error())
		return
	}
	if lines < 1 || lines > 100000 {
		show(s, "Después de -n escribe un número entre 1 y 100000.")
		return
	}
	src, err := resolveLog(service)
	if err != nil {
		show(s, err.Error())
		return
	}
	u.service = service
	u.source = src
	u.following = follow
	u.paused = false
	u.offset = 0
	u.hOffset = 0
	u.autoScroll = true
	u.log = nil
	h, err := loadHistory(src, service, lines)
	if err == nil {
		u.log = h
	}
	if grab {
		f, err := runitlog.OpenCapture(service, dir)
		if err != nil {
			show(s, fmt.Sprintf("No se pudo guardar la copia: %v", err))
			return
		}
		for _, line := range u.log {
			_, _ = fmt.Fprintln(f, line)
		}
		_ = f.Sync()
		u.mu.Lock()
		u.capture = f
		u.mu.Unlock()
	}
	var stop chan struct{}
	var followDone chan struct{}
	if follow {
		stop = make(chan struct{})
		followDone = make(chan struct{})
		go func() { u.followLoop(s, stop); close(followDone) }()
	}
	defer func() {
		if stop != nil {
			close(stop)
			<-followDone // evita escribir en la captura ya cerrada
		}
		u.mu.Lock()
		if u.capture != nil {
			_ = u.capture.Sync()
			u.capture.Close()
			u.capture = nil
		}
		u.mu.Unlock()
	}()
	for {
		u.drawLog(s)
		ev := s.PollEvent()
		k, ok := ev.(*tcell.EventKey)
		if !ok {
			if _, resize := ev.(*tcell.EventResize); resize {
				s.Sync()
			}
			continue
		}
		u.mu.Lock() // el seguimiento en vivo toca offset/autoScroll/log desde otra goroutine
		quit := u.handleLogKey(s, k, src, service, lines)
		u.mu.Unlock()
		if quit {
			return
		}
	}
}

// handleLogKey procesa una tecla del visor de log. Debe llamarse con u.mu
// tomado. Devuelve true si hay que salir del visor.
func (u *journalUI) handleLogKey(s tcell.Screen, k *tcell.EventKey, src logSource, service string, lines int) bool {
	{
		switch k.Key() {
		case tcell.KeyEscape:
			return true
		case tcell.KeyUp:
			u.offset = max(0, u.offset-1)
			u.autoScroll = false
		case tcell.KeyDown:
			u.offset++
			u.autoScroll = false
		case tcell.KeyLeft:
			u.hOffset = max(0, u.hOffset-4)
		case tcell.KeyRight:
			u.hOffset += 4

		case tcell.KeyPgUp:
			_, height := s.Size()
			u.offset = max(0, u.offset-height/2)
			u.autoScroll = false
		case tcell.KeyPgDn:
			_, height := s.Size()
			u.offset += height / 2
			u.autoScroll = false
		case tcell.KeyHome:
			u.offset = 0
			u.autoScroll = false
		case tcell.KeyEnd:
			u.offset = 999999
			u.autoScroll = true
		case tcell.KeyRune:
			switch k.Rune() {
			case 'q':
				return true
			case 'f':
				u.paused = !u.paused
				if !u.paused {
					u.autoScroll = true
				}
			case 'r':
				if h, err := loadHistory(src, service, lines); err == nil {
					u.log = h
				}
			case 'g':
				u.offset = 0
				u.autoScroll = false
			case 'G':
				u.offset = 999999
				u.autoScroll = true
			}
		}
	}
	return false
}

func show(s tcell.Screen, msg string) {
	s.Clear()
	draw(s, 1, 1, tcell.StyleDefault.Foreground(tcell.ColorRed), msg)
	draw(s, 1, 3, tcell.StyleDefault.Foreground(tcell.ColorGray), "Pulsa una tecla para volver")
	s.Show()
	s.PollEvent()
}

// parseArgs analiza args permitiendo flags antes y después de los
// argumentos posicionales (`runit-journal nginx -n 200`, como documenta el
// README). El paquete flag por sí solo se detiene en el primer posicional e
// ignoraría en silencio todo lo que viene después.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func main() {
	fs := flag.NewFlagSet("runit-journal", flag.ContinueOnError)
	follow := fs.Bool("f", true, "seguir el log en vivo (-f=false para solo historial)")
	lines := fs.Int("n", 200, "líneas históricas (1-100000)")
	grab := fs.Bool("g", false, "guardar el historial y las líneas nuevas en un .log")
	dir := fs.String("log-dir", "", "carpeta de captura (por defecto $XDG_STATE_HOME/runitctl/logs)")
	showVersion := fs.Bool("version", false, "mostrar la versión y salir")
	fs.BoolVar(showVersion, "v", false, "mostrar la versión y salir")
	fs.BoolVar(showVersion, "V", false, "mostrar la versión y salir")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Uso: runit-journal [opciones] [servicio]")
		fs.PrintDefaults()
	}
	pos, err := parseArgs(fs, os.Args[1:])
	if err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		os.Exit(2)
	}
	if *showVersion {
		fmt.Println("runit-journal " + version)
		return
	}
	if len(pos) > 1 {
		fmt.Fprintln(os.Stderr, "Indica un solo servicio.")
		os.Exit(2)
	}
	if *lines < 1 || *lines > 100000 {
		fmt.Fprintln(os.Stderr, "Después de -n escribe un número entre 1 y 100000.")
		os.Exit(2)
	}
	if len(pos) == 1 {
		if err := runitlog.ValidateName(pos[0]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	list := listServices()
	if len(pos) == 1 {
		list = []string{pos[0]}
	}
	s, err := tcell.NewScreen()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err = s.Init(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer s.Fini()
	u := &journalUI{services: list}
	if len(pos) == 1 {
		u.openLog(s, pos[0], *follow, *lines, *grab, *dir)
		return
	}
	for {
		u.drawSelect(s)
		ev := s.PollEvent()
		if _, ok := ev.(*tcell.EventResize); ok {
			s.Sync()
			continue
		}
		k, ok := ev.(*tcell.EventKey)
		if !ok {
			continue
		}
		switch k.Key() {
		case tcell.KeyEscape:
			return
		case tcell.KeyUp:
			u.cursor = max(0, u.cursor-1)
		case tcell.KeyDown:
			u.cursor++
		case tcell.KeyPgUp:
			u.cursor = max(0, u.cursor-10)
		case tcell.KeyPgDn:
			u.cursor += 10
		case tcell.KeyHome:
			u.cursor = 0
		case tcell.KeyEnd:
			u.cursor = 1 << 30 // drawSelect lo acota al último
		case tcell.KeyEnter:
			vis := u.visibleServices()
			if len(vis) > 0 {
				u.openLog(s, vis[clamp(u.cursor, 0, len(vis)-1)], *follow, *lines, *grab, *dir)
			}
		case tcell.KeyRune:
			if k.Rune() == 'q' || k.Rune() == 'Q' {
				return
			}
			if k.Rune() == '/' {
				if q, yes := prompt(s, u.filter); yes {
					u.filter = q
					u.cursor = 0
					u.offset = 0
				}
			}
		}
	}
}
