package main

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func screenText(s tcell.SimulationScreen) string {
	cells, w, _ := s.GetContents()
	var b strings.Builder
	for i, c := range cells {
		if len(c.Runes) > 0 {
			b.WriteRune(c.Runes[0])
		}
		if (i+1)%w == 0 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func TestActionMenuShowsFullLabels(t *testing.T) {
	t.Setenv("RUNITCTL_LANG", "es")
	initI18n("")
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Skip(err)
	}
	defer s.Fini()
	s.SetSize(100, 30)
	go func() { s.InjectKey(tcell.KeyEscape, 0, 0) }()
	actionMenu(s, "NetworkManager")
	// El menú ya se cerró, pero la última pantalla dibujada sigue en el buffer.
	txt := screenText(s)
	for _, a := range buildActions() {
		if !strings.Contains(txt, a.label) {
			t.Errorf("etiqueta cortada o ausente: %q", a.label)
		}
	}
}
