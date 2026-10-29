package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// ---------------------------------------------------------------------
// Órdenes extra de 'sv' (once, term, kill, ...)
// ---------------------------------------------------------------------

// advancedActions relaciona el nombre que escribe la persona con la orden
// real de 'sv'. check no cambia nada, por eso no pide permisos de administrador.
var advancedActions = map[string]string{
	"once":  "once",
	"term":  "term",
	"kill":  "kill",
	"int":   "interrupt",
	"alarm": "alarm",
	"usr1":  "1",
	"usr2":  "2",
	"quit":  "quit",
	"check": "check",
}

// advancedOrder fija el orden en que se muestran en la ayuda.
var advancedOrder = []string{"once", "term", "kill", "int", "alarm", "usr1", "usr2", "quit", "check"}

func isAdvancedAction(a string) bool { _, ok := advancedActions[a]; return ok }

func runAdvanced(action, service string) {
	if service == "" {
		errorMsg(T("specify_service_generic"))
		os.Exit(1)
	}
	if !exists(filepath.Join(RunsvDir, service)) {
		fmt.Println(T("not_exists_generic", service))
		os.Exit(1)
	}
	svCmd := advancedActions[action]
	if action != "check" {
		info(T("adv_doing", action, service))
	}
	if !runSvCommand(svCmd, service) {
		errorMsg(T("adv_err", action, service))
		os.Exit(1)
	}
	if action != "check" {
		success(T("adv_ok", service, action))
	}
}

// ---------------------------------------------------------------------
// Niveles de runit (runsvchdir)
// ---------------------------------------------------------------------

// levelsDir es la carpeta que contiene los niveles (default, single, ...).
func levelsDir() string { return envOr("RUNITCTL_LEVELS_DIR", filepath.Dir(RunsvDir)) }

func listLevels() ([]string, error) {
	entries, err := os.ReadDir(levelsDir())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Name() == "current" {
			continue
		}
		if st, err := os.Stat(filepath.Join(levelsDir(), e.Name())); err == nil && st.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func cmdRunlevel(args []string) {
	if len(args) >= 2 { // cambiar de nivel
		name := args[1]
		found := false
		levels, _ := listLevels()
		for _, l := range levels {
			found = found || l == name
		}
		if !found {
			errorMsg(T("level_unknown", name))
			os.Exit(1)
		}
		if _, err := exec.LookPath("runsvchdir"); err != nil {
			errorMsg(T("level_no_tool"))
			os.Exit(1)
		}
		info(T("level_switching", name))
		if out, err := runCmd("runsvchdir", name); err != nil {
			errorMsg(T("level_failed", name, out))
			os.Exit(1)
		}
		success(T("level_switched", name))
		return
	}
	levels, err := listLevels()
	if err != nil {
		errorMsg(T("read_error", levelsDir(), err))
		os.Exit(1)
	}
	current := ""
	if t, err := filepath.EvalSymlinks(filepath.Join(levelsDir(), "current")); err == nil {
		current = filepath.Base(t)
	}
	fmt.Printf("%s%s%s\n\n", Bold, T("levels_title"), ColorReset)
	for _, l := range levels {
		if l == current {
			fmt.Printf("  %s●%s %s %s(%s)%s\n", ColorGreen, ColorReset, l, ColorGreen, T("level_current"), ColorReset)
		} else {
			fmt.Printf("  ○ %s\n", l)
		}
	}
}

// ---------------------------------------------------------------------
// doctor: ¿están las herramientas de runit?
// ---------------------------------------------------------------------

var runitTools = []string{"sv", "runsv", "runsvdir", "runsvchdir", "svlogd", "chpst", "utmpset", "svlogtail", "socklog"}

func cmdDoctor() {
	fmt.Printf("%s%s%s\n\n", Bold, T("doctor_title"), ColorReset)
	missing := 0
	for _, tool := range runitTools {
		if p, err := exec.LookPath(tool); err == nil {
			fmt.Printf("  %s✓%s %-11s %s\n", ColorGreen, ColorReset, tool, T("doctor_desc_"+tool))
			_ = p
		} else {
			missing++
			fmt.Printf("  %s✗%s %-11s %s (%s)\n", ColorRed, ColorReset, tool, T("doctor_desc_"+tool), T("doctor_missing"))
		}
	}
	fmt.Println()
	if missing == 0 {
		success(T("doctor_all_ok"))
		return
	}
	warn(T("doctor_some_missing"))
	os.Exit(1)
}
