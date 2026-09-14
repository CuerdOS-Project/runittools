package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
)

const serviceDir = "/etc/sv"

type logEvent struct{ at time.Time }

func (e *logEvent) When() time.Time { return e.at }

type logSource struct {
	paths   []string
	filter  bool
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
	stop       chan struct{}
	capture    *os.File
}

func listServices() []string {
	entries, err := os.ReadDir(serviceDir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
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

func tailLines(path string, n int) ([]string, int64, error) {
	if n < 1 {
		return nil, 0, fmt.Errorf("el número de líneas debe ser positivo")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := st.Size()
	const blockSize int64 = 64 * 1024
	const maxScan int64 = 8 * 1024 * 1024
	var data []byte
	pos := size
	newlines := 0
	for pos > 0 && newlines <= n && size-pos < maxScan {
		readSize := blockSize
		if readSize > pos {
			readSize = pos
		}
		pos -= readSize
		buf := make([]byte, readSize)
		if _, err := f.ReadAt(buf, pos); err != nil && err != io.EOF {
			return nil, size, err
		}
		newlines += strings.Count(string(buf), "\n")
		data = append(buf, data...)
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil, size, nil
	}
	all := strings.Split(text, "\n")
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, size, nil
}

func resolveLog(service string) (logSource, error) {
	if err := validateServiceName(service); err != nil {
		return logSource{}, err
	}
	classic := []string{
		"/etc/runit/runsvdir/default/" + service + "/log/main/current",
		"/etc/runit/runsvdir/default/" + service + "/log/current",
		"/var/service/" + service + "/log/main/current",
		"/var/service/" + service + "/log/current",
		"/etc/sv/" + service + "/log/main/current",
		"/etc/sv/" + service + "/log/current",
		"/var/log/runit/" + service + "/current",
	}
	for _, p := range classic {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return logSource{[]string{p}, false, "runit log/current"}, nil
		}
	}
	root := "/var/log/socklog"
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
			return logSource{paths, true, "socklog (todas las clases)"}, nil
		}
	}
	return logSource{}, fmt.Errorf("no se encontró un log para %s", service)
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
	for _, path := range src.paths {
		part, _, err := tailLines(path, 10000)
		if err != nil {
			continue
		}
		lines = append(lines, part...)
	}
	sort.Strings(lines)
	if !src.filter {
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		return lines, nil
	}
	out := make([]string, 0, n)
	for _, line := range lines {
		if matchesService(line, service) {
			out = append(out, line)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
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
	draw(s, 1, 0, title, "runit-journal — visor de logs runit (tcell)")
	draw(s, 1, 1, dim, "↑/↓ seleccionar  Enter abrir  / buscar  Esc/q salir")
	draw(s, 1, 2, dim, "Servicio: "+u.filter)
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
		draw(s, 1, 4, dim, "No hay servicios que coincidan.")
	}
	draw(s, 1, h-1, dim, "Enter: ver logging   /: buscar   q/Esc: salir")
	s.Show()
}

func prompt(s tcell.Screen, current string) (string, bool) {
	v := []rune(current)
	for {
		s.Clear()
		draw(s, 1, 1, tcell.StyleDefault.Foreground(tcell.ColorWhite), "Buscar servicio: "+string(v))
		draw(s, 1, 3, tcell.StyleDefault.Foreground(tcell.ColorGray), "Enter aplicar   Esc cancelar   Backspace borrar")
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
	state := "pausado"
	if u.following && !u.paused {
		state = "en vivo"
	}
	draw(s, 0, 0, title, fmt.Sprintf(" log: %s | %s | %s", u.service, u.source.backend, state))
	view := h - 3
	if view < 1 {
		view = 1
	}
	u.mu.Lock()
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
		draw(s, 1, 2, dim, "No hay historial; esperando nuevas líneas...")
	}
	draw(s, 0, h-1, dim, " ↑/↓/Pg mover  ←/→ horizontal  f pausa  r recargar  g/G inicio/fin  q/Esc volver")
	if w > 0 {
		s.Show()
	}
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (u *journalUI) appendLine(line string) {
	u.mu.Lock()
	u.log = append(u.log, line)
	if u.following && !u.paused {
		u.autoScroll = true
	}
	if len(u.log) > 10000 {
		u.log = u.log[len(u.log)-10000:]
	}
	if u.capture != nil {
		_, _ = fmt.Fprintln(u.capture, line)
		_ = u.capture.Sync()
	}
	u.mu.Unlock()
}

func (u *journalUI) followLoop(screen tcell.Screen) {
	positions := make(map[string]int64)
	for _, path := range u.source.paths {
		if st, err := os.Stat(path); err == nil {
			positions[path] = st.Size()
		}
	}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-u.stop:
			return
		case <-ticker.C:
			if u.paused {
				continue
			}
			for _, path := range u.source.paths {
				f, err := os.Open(path)
				if err != nil {
					continue
				}
				pos := positions[path]
				if st, statErr := f.Stat(); statErr == nil && st.Size() < pos {
					pos = 0
					positions[path] = 0
				}
				if _, err = f.Seek(pos, 0); err != nil {
					f.Close()
					continue
				}
				sc := bufio.NewScanner(f)
				sc.Buffer(make([]byte, 64*1024), 1024*1024)
				for sc.Scan() {
					line := sc.Text()
					if !u.source.filter || matchesService(line, u.service) {
						u.appendLine(line)
					}
				}
				if err := sc.Err(); err != nil {
					u.appendLine(fmt.Sprintf("[runit-journal] error leyendo %s: %v", path, err))
				}
				if st, err := f.Stat(); err == nil {
					positions[path] = st.Size()
				}
				f.Close()
			}
			screen.PostEvent(&logEvent{at: time.Now()})
		}
	}
}

func (u *journalUI) openLog(s tcell.Screen, service string, follow bool, lines int, grab bool, dir string) {
	if err := validateServiceName(service); err != nil {
		show(s, err.Error())
		return
	}
	if lines < 1 || lines > 100000 {
		show(s, "-n debe estar entre 1 y 100000")
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
		if dir == "" {
			dir = os.Getenv("HOME") + "/.local/state/runitctl/logs"
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			show(s, fmt.Sprintf("no se pudo crear la carpeta de captura: %v", err))
			return
		}
		p := filepath.Join(dir, safeLogName(service)+"-"+time.Now().Format("20060102-150405.000000000")+".log")
		var captureErr error
		u.capture, captureErr = os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if captureErr != nil {
			show(s, fmt.Sprintf("no se pudo abrir la captura: %v", captureErr))
			return
		}
		if u.capture != nil {
			for _, line := range u.log {
				_, _ = fmt.Fprintln(u.capture, line)
			}
			_ = u.capture.Sync()
		}
	}
	if follow {
		u.stop = make(chan struct{})
		go u.followLoop(s)
	}
	defer func() {
		if u.stop != nil {
			close(u.stop)
		}
		if u.capture != nil {
			u.capture.Close()
			u.capture = nil
		}
	}()
	for {
		u.drawLog(s)
		ev := s.PollEvent()
		k, ok := ev.(*tcell.EventKey)
		if !ok {
			continue
		}
		switch k.Key() {
		case tcell.KeyEscape:
			return
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
				return
			case 'f':
				u.paused = !u.paused
				if !u.paused {
					u.autoScroll = true
				}
			case 'r':
				h, _ := loadHistory(src, service, lines)
				u.mu.Lock()
				u.log = h
				u.mu.Unlock()
			case 'g':
				u.offset = 0
			case 'G':
				u.offset = 999999
			}
		}
	}
}
func show(s tcell.Screen, msg string) {
	s.Clear()
	draw(s, 1, 1, tcell.StyleDefault.Foreground(tcell.ColorRed), msg)
	draw(s, 1, 3, tcell.StyleDefault.Foreground(tcell.ColorGray), "Presiona una tecla para volver")
	s.Show()
	s.PollEvent()
}

func main() {
	follow := flag.Bool("f", true, "seguir el log en vivo")
	lines := flag.Int("n", 200, "líneas históricas")
	grab := flag.Bool("g", false, "guardar en .log")
	dir := flag.String("log-dir", "", "carpeta de captura")
	flag.Parse()
	if *lines < 1 || *lines > 100000 {
		fmt.Fprintln(os.Stderr, "-n debe ser un entero entre 1 y 100000")
		os.Exit(2)
	}
	if flag.NArg() > 0 {
		if err := validateServiceName(flag.Arg(0)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	list := listServices()
	if flag.NArg() > 0 {
		list = []string{flag.Arg(0)}
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
	if flag.NArg() > 0 {
		u.openLog(s, flag.Arg(0), *follow, *lines, *grab, *dir)
		return
	}
	for {
		u.drawSelect(s)
		ev := s.PollEvent()
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
