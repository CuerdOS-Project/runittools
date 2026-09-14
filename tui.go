package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
)

// ---------------------------------------------------------------------
// Modelo de datos de servicios
// ---------------------------------------------------------------------

type svState int

const (
	stateRunning svState = iota
	stateStopped
	stateFailed
)

type serviceEntry struct {
	name    string
	enabled bool
	state   svState
	detail  string
}

func loadServiceNames() ([]string, error) {
	files, err := os.ReadDir(SvDir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name())
	}
	sort.Strings(names)
	return names, nil
}

// probeService consulta 'sv status' de un servicio y clasifica su estado.
func probeService(name string) (svState, string) {
	out, err := exec.Command("sv", "status", name).CombinedOutput()
	output := string(out)
	if err != nil || strings.HasPrefix(output, "fail:") {
		return stateFailed, strings.TrimSpace(output)
	}
	if strings.HasPrefix(output, "run:") {
		return stateRunning, strings.TrimSpace(output)
	}
	return stateStopped, strings.TrimSpace(output)
}

func loadServices() ([]serviceEntry, error) {
	names, err := loadServiceNames()
	if err != nil {
		return nil, err
	}
	entries := make([]serviceEntry, 0, len(names))
	for _, name := range names {
		enabled := exists(RunsvDir + "/" + name)
		state, detail := stateStopped, ""
		if enabled {
			state, detail = probeService(name)
		}
		entries = append(entries, serviceEntry{name: name, enabled: enabled, state: state, detail: detail})
	}
	return entries, nil
}

func matchesQuery(name, query string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), strings.ToLower(query))
}

// ---------------------------------------------------------------------
// Utilidades de dibujo
// ---------------------------------------------------------------------

func drawText(screen tcell.Screen, x, y, width int, text string, style tcell.Style) {
	if width <= 0 || y < 0 {
		return
	}
	runes := []rune(text)
	if len(runes) > width {
		runes = runes[:width]
	}
	for _, r := range runes {
		screen.SetContent(x, y, r, nil, style)
		x++
	}
}

func fillRow(screen tcell.Screen, y, width int, style tcell.Style) {
	for x := 0; x < width; x++ {
		screen.SetContent(x, y, ' ', nil, style)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var (
	styleTitle    = tcell.StyleDefault.Foreground(tcell.NewRGBColor(0, 200, 200)).Bold(true)
	styleNormal   = tcell.StyleDefault.Foreground(tcell.ColorWhite)
	styleDim      = tcell.StyleDefault.Foreground(tcell.ColorGray)
	styleQuery    = tcell.StyleDefault.Foreground(tcell.ColorYellow)
	styleSelected = tcell.StyleDefault.Background(tcell.ColorBlue).Foreground(tcell.ColorWhite).Bold(true)
	styleRunning  = tcell.StyleDefault.Foreground(tcell.ColorGreen)
	styleStopped  = tcell.StyleDefault.Foreground(tcell.ColorYellow)
	styleFailed   = tcell.StyleDefault.Foreground(tcell.ColorRed)
	styleFooter   = tcell.StyleDefault.Background(tcell.ColorYellow).Foreground(tcell.ColorBlack)
	styleMenuBox  = tcell.StyleDefault.Foreground(tcell.ColorWhite)
)

func stateGlyph(s svState) (string, tcell.Style) {
	switch s {
	case stateRunning:
		return "●", styleRunning
	case stateFailed:
		return "✖", styleFailed
	default:
		return "○", styleStopped
	}
}

// ---------------------------------------------------------------------
// Punto de entrada de la TUI
// ---------------------------------------------------------------------

func runTUI() {
	screen, err := tcell.NewScreen()
	if err != nil {
		errorMsg(fmt.Sprintf("%v", err))
		return
	}
	if err := screen.Init(); err != nil {
		errorMsg(fmt.Sprintf("%v", err))
		return
	}
	defer screen.Fini()
	screen.EnableMouse()
	screen.Clear()

	services, err := loadServices()
	if err != nil {
		errorMsg(T("read_error", SvDir, err))
		return
	}

	query := []rune{}
	cursor, offset := 0, 0
	prevButtons := tcell.ButtonNone

	for {
		filtered := make([]int, 0, len(services))
		for i, s := range services {
			if matchesQuery(s.name, string(query)) {
				filtered = append(filtered, i)
			}
		}
		if cursor >= len(filtered) {
			cursor = maxInt(0, len(filtered)-1)
		}

		width, height := screen.Size()
		listRows := maxInt(1, height-6)
		if cursor < offset {
			offset = cursor
		}
		if cursor >= offset+listRows {
			offset = cursor - listRows + 1
		}

		screen.Clear()
		drawText(screen, 0, 0, width, " "+T("tui_title"), styleTitle)
		drawText(screen, 0, 1, width, " "+T("tui_filter_label")+": "+string(query)+"▌", styleQuery)
		drawText(screen, 0, 2, width, fmt.Sprintf(" %d %s", len(filtered), T("tui_results")), styleDim)

		headerStyle := styleDim
		drawText(screen, 0, 3, width, fmt.Sprintf(" %-28s %-3s %-12s", T("tui_col_service"), "", T("tui_col_enabled")), headerStyle)

		if len(filtered) == 0 {
			drawText(screen, 0, 5, width, " "+T("tui_no_services"), styleDim)
		}

		for row := 0; row < listRows && row+offset < len(filtered); row++ {
			pos := row + offset
			s := services[filtered[pos]]
			glyph, glyphStyle := stateGlyph(s.state)
			enabledLabel := T("disabled_tag")
			if s.enabled {
				enabledLabel = T("enabled_tag")
			}
			lineStyle := styleNormal
			if pos == cursor {
				lineStyle = styleSelected
				glyphStyle = lineStyle
			}
			y := 4 + row
			fillRow(screen, y, width, lineStyle)
			drawText(screen, 1, y, width-1, fmt.Sprintf("%-28s", s.name), lineStyle)
			drawText(screen, 30, y, 3, glyph, glyphStyle)
			drawText(screen, 34, y, width-34, enabledLabel, lineStyle)
		}

		drawText(screen, 0, height-1, width, " "+T("tui_help_bar"), styleFooter)
		screen.Show()

		switch ev := screen.PollEvent().(type) {
		case *tcell.EventResize:
			screen.Sync()

		case *tcell.EventMouse:
			buttons := ev.Buttons()
			_, y := ev.Position()
			pressed := buttons &^ prevButtons // botones que acaban de presionarse en este evento
			if buttons&tcell.WheelUp != 0 {
				cursor = maxInt(0, cursor-3)
			} else if buttons&tcell.WheelDown != 0 {
				cursor = minInt(maxInt(0, len(filtered)-1), cursor+3)
			} else if pressed&tcell.Button1 != 0 {
				if y >= 4 && y < 4+listRows {
					idxPos := y - 4 + offset
					if idxPos < len(filtered) {
						cursor = idxPos
						if len(filtered) > 0 {
							name := services[filtered[cursor]].name
							actionMenu(screen, name)
							services, _ = loadServices()
						}
					}
				}
			}
			prevButtons = buttons

		case *tcell.EventKey:
			switch ev.Key() {
			case tcell.KeyEscape:
				return
			case tcell.KeyUp:
				if cursor > 0 {
					cursor--
				}
			case tcell.KeyDown:
				if cursor < len(filtered)-1 {
					cursor++
				}
			case tcell.KeyPgUp:
				cursor = maxInt(0, cursor-listRows)
			case tcell.KeyPgDn:
				cursor = minInt(maxInt(0, len(filtered)-1), cursor+listRows)
			case tcell.KeyHome:
				cursor = 0
			case tcell.KeyEnd:
				cursor = maxInt(0, len(filtered)-1)
			case tcell.KeyBackspace, tcell.KeyBackspace2:
				if len(query) > 0 {
					query = query[:len(query)-1]
					cursor, offset = 0, 0
				}
			case tcell.KeyEnter:
				if len(filtered) > 0 {
					name := services[filtered[cursor]].name
					actionMenu(screen, name)
					services, _ = loadServices()
				}
			default:
				r := ev.Rune()
				switch r {
				case 'q', 'Q':
					if len(query) == 0 {
						return
					}
					query = append(query, r)
					cursor, offset = 0, 0
				case 'r', 'R':
					if len(query) == 0 {
						services, _ = loadServices()
					} else {
						query = append(query, r)
						cursor, offset = 0, 0
					}
				default:
					if r >= 32 {
						query = append(query, r)
						cursor, offset = 0, 0
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------
// Menú de acciones para un servicio
// ---------------------------------------------------------------------

type menuAction struct {
	key   string
	label string
	run   func(service string) (string, bool)
}

func buildActions() []menuAction {
	return []menuAction{
		{"start", T("help_start"), func(s string) (string, bool) {
			ok := runSvCommandCaptured("up", s)
			if ok {
				return T("started_ok", s), true
			}
			return T("started_err", s), false
		}},
		{"stop", T("help_stop"), func(s string) (string, bool) {
			ok := runSvCommandCaptured("down", s)
			if ok {
				return T("stopped_ok", s), true
			}
			return T("stopped_err", s), false
		}},
		{"restart", T("help_restart"), func(s string) (string, bool) {
			runSvCommandCaptured("restart", s)
			return T("restarting", s), true
		}},
		{"reload", T("help_reload"), func(s string) (string, bool) {
			runSvCommandCaptured("hup", s)
			return T("reloading", s), true
		}},
		{"pause", T("help_pause"), func(s string) (string, bool) {
			runSvCommandCaptured("pause", s)
			return T("pausing", s), true
		}},
		{"cont", T("help_cont"), func(s string) (string, bool) {
			runSvCommandCaptured("cont", s)
			return T("resuming", s), true
		}},
		{"status", T("help_status"), func(s string) (string, bool) {
			return renderStatus(s), true
		}},
		{"logs", T("help_logs"), nil}, // manejado de forma especial: abre el visor interactivo en vez de un mensaje
		{"enable", T("help_enable"), func(s string) (string, bool) {
			return enableService(s)
		}},
		{"disable", T("help_disable"), func(s string) (string, bool) {
			return disableService(s)
		}},
		{"mask", T("help_mask"), func(s string) (string, bool) {
			return toggleMask(s)
		}},
	}
}

// actionMenu dibuja un pequeño modal con las acciones disponibles para el
// servicio y ejecuta la elegida, mostrando el resultado en otro modal.
func actionMenu(screen tcell.Screen, service string) {
	actions := buildActions()
	cursor := 0
	prevButtons := tcell.ButtonNone

	for {
		width, height := screen.Size()
		boxW := minInt(width-4, 50)
		boxH := minInt(height-4, len(actions)+4)
		x0 := (width - boxW) / 2
		y0 := (height - boxH) / 2

		screen.Clear()
		for y := y0; y < y0+boxH; y++ {
			fillRow(screen, y, width, styleMenuBox)
		}
		title := fmt.Sprintf(T("tui_action_menu", service))
		drawText(screen, x0+1, y0, boxW-2, title, styleTitle)
		for i, a := range actions {
			style := styleNormal
			if i == cursor {
				style = styleSelected
			}
			y := y0 + 2 + i
			if y >= y0+boxH-1 {
				break
			}
			fillRow(screen, y, width, style)
			drawText(screen, x0+2, y, boxW-4, a.label, style)
		}
		drawText(screen, x0+1, y0+boxH-1, boxW-2, T("tui_esc_back"), styleDim)
		screen.Show()

		switch ev := screen.PollEvent().(type) {
		case *tcell.EventResize:
			screen.Sync()
		case *tcell.EventMouse:
			buttons := ev.Buttons()
			_, y := ev.Position()
			pressed := buttons &^ prevButtons
			if buttons&tcell.WheelUp != 0 {
				cursor = maxInt(0, cursor-1)
			} else if buttons&tcell.WheelDown != 0 {
				cursor = minInt(len(actions)-1, cursor+1)
			} else if pressed&tcell.Button1 != 0 {
				idx := y - (y0 + 2)
				if idx >= 0 && idx < len(actions) {
					cursor = idx
					if actions[cursor].run == nil {
						logViewer(screen, service)
					} else {
						msg, _ := actions[cursor].run(service)
						showMessage(screen, service, msg)
					}
					prevButtons = tcell.ButtonNone
					continue
				}
			}
			prevButtons = buttons
		case *tcell.EventKey:
			switch ev.Key() {
			case tcell.KeyEscape:
				return
			case tcell.KeyUp:
				if cursor > 0 {
					cursor--
				}
			case tcell.KeyDown:
				if cursor < len(actions)-1 {
					cursor++
				}
			case tcell.KeyEnter:
				if actions[cursor].run == nil {
					logViewer(screen, service)
				} else {
					msg, _ := actions[cursor].run(service)
					showMessage(screen, service, msg)
				}
			default:
				if ev.Rune() == 'q' || ev.Rune() == 'Q' {
					return
				}
			}
		}
	}
}

// showMessage muestra el resultado de una acción (incluyendo salidas
// multilínea como el estado detallado) y espera una pulsación de tecla o
// clic para continuar.
func showMessage(screen tcell.Screen, service, msg string) {
	lines := strings.Split(strings.TrimRight(msg, "\n"), "\n")
	for {
		width, height := screen.Size()
		boxW := minInt(width-4, maxInt(40, len(service)+20))
		boxH := minInt(height-4, len(lines)+4)
		x0 := (width - boxW) / 2
		y0 := (height - boxH) / 2

		screen.Clear()
		for y := y0; y < y0+boxH; y++ {
			fillRow(screen, y, width, styleMenuBox)
		}
		for i, line := range lines {
			if i >= boxH-3 {
				break
			}
			drawText(screen, x0+1, y0+1+i, boxW-2, stripAnsi(line), styleNormal)
		}
		drawText(screen, x0+1, y0+boxH-1, boxW-2, T("tui_press_key"), styleDim)
		screen.Show()

		switch screen.PollEvent().(type) {
		case *tcell.EventResize:
			screen.Sync()
		case *tcell.EventKey, *tcell.EventMouse:
			return
		}
	}
}

// stripAnsi retira los códigos de color ANSI que algunas funciones
// reutilizadas del CLI (como renderStatus) incluyen en su salida, ya que
// dentro de la TUI el color se aplica mediante estilos de tcell.
func stripAnsi(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ---------------------------------------------------------------------
// Operaciones reutilizadas por el menú (misma lógica que el CLI)
// ---------------------------------------------------------------------

func runSvCommandCaptured(command, service string) bool {
	return runSvCommand(command, service)
}

func enableService(service string) (string, bool) {
	serviceSv := SvDir + "/" + service
	servicePath := RunsvDir + "/" + service
	if !exists(serviceSv) {
		return T("not_exists_svdir", service, SvDir), false
	}
	if exists(servicePath) {
		return T("already_enabled", service), true
	}
	if err := os.Symlink(serviceSv, servicePath); err != nil {
		return T("enabled_err", service, err), false
	}
	return T("enabled_ok", service), true
}

func disableService(service string) (string, bool) {
	servicePath := RunsvDir + "/" + service
	if !exists(servicePath) {
		return T("already_disabled", service), true
	}
	if err := os.Remove(servicePath); err != nil {
		return T("disabled_err", service, err), false
	}
	return T("disabled_ok", service), true
}

func toggleMask(service string) (string, bool) {
	downFile := SvDir + "/" + service + "/down"
	if exists(downFile) {
		if err := os.Remove(downFile); err != nil {
			return T("unmasked_err", service, err), false
		}
		return T("unmasked_ok", service), true
	}
	f, err := os.Create(downFile)
	if err != nil {
		return T("masked_err", service, err), false
	}
	f.Close()
	return T("masked_ok", service), true
}
