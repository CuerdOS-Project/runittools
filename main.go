package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Constantes de rutas por defecto de runit
const (
	SvDir    = "/etc/sv"
	RunsvDir = "/etc/runit/runsvdir/default"
)

// Códigos ANSI para color y formato
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	Bold        = "\033[1m"
)

func info(msg string)     { fmt.Printf("%s%s%s\n", ColorBlue, msg, ColorReset) }
func success(msg string)  { fmt.Printf("%s%s%s\n", ColorGreen, msg, ColorReset) }
func warn(msg string)     { fmt.Printf("%s%s%s\n", ColorYellow, msg, ColorReset) }
func errorMsg(msg string) { fmt.Fprintf(os.Stderr, "%s%s%s\n", ColorRed, msg, ColorReset) }

func checkRoot() {
	if os.Geteuid() != 0 {
		errorMsg(T("root_required"))
		fmt.Printf("%s%s%s\n", ColorGreen, T("try_again", os.Args[0], strings.Join(os.Args[1:], " ")), ColorReset)
		os.Exit(1)
	}
}

func requiresRoot(action string) bool {
	switch action {
	case "start", "stop", "restart", "reload", "pause", "cont", "enable", "disable", "mask", "unmask", "tui":
		return true
	default:
		return false
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func printHelp() {
	fmt.Printf("%s%s%s\n\n", Bold, T("help_title"), ColorReset)
	fmt.Printf("%s%s%s\n", Bold, T("help_usage"), ColorReset)
	fmt.Println("  runitctl [--lang es|en|pt|ca] <acción> [servicio]")
	fmt.Println()
	fmt.Printf("%s%s%s\n", Bold, T("help_service_actions"), ColorReset)
	fmt.Printf("  %sstart%s <servicio>      - %s\n", ColorGreen, ColorReset, T("help_start"))
	fmt.Printf("  %sstop%s <servicio>       - %s\n", ColorGreen, ColorReset, T("help_stop"))
	fmt.Printf("  %srestart%s <servicio>    - %s\n", ColorGreen, ColorReset, T("help_restart"))
	fmt.Printf("  %sreload%s <servicio>     - %s\n", ColorGreen, ColorReset, T("help_reload"))
	fmt.Printf("  %spause%s <servicio>      - %s\n", ColorGreen, ColorReset, T("help_pause"))
	fmt.Printf("  %scont%s <servicio>       - %s\n", ColorGreen, ColorReset, T("help_cont"))
	fmt.Printf("  %sstatus%s <servicio>     - %s\n", ColorGreen, ColorReset, T("help_status"))
	fmt.Println()
	fmt.Printf("%s%s%s\n", Bold, T("help_config_mgmt"), ColorReset)
	fmt.Printf("  %senable%s <servicio>     - %s\n", ColorGreen, ColorReset, T("help_enable"))
	fmt.Printf("  %sdisable%s <servicio>    - %s\n", ColorGreen, ColorReset, T("help_disable"))
	fmt.Printf("  %smask%s <servicio>       - %s\n", ColorGreen, ColorReset, T("help_mask"))
	fmt.Printf("  %sunmask%s <servicio>     - %s\n", ColorGreen, ColorReset, T("help_unmask"))
	fmt.Println()
	fmt.Printf("%s%s%s\n", Bold, T("help_queries"), ColorReset)
	fmt.Printf("  %slist-active%s          - %s\n", ColorGreen, ColorReset, T("help_list_active"))
	fmt.Printf("  %slist-all%s             - %s\n", ColorGreen, ColorReset, T("help_list_all"))
	fmt.Printf("  %ssearch%s <patrón>       - %s\n", ColorGreen, ColorReset, T("help_search"))
	fmt.Printf("  %slog%s <servicio> [-n N] [-f] [-i] [-g] [--log-dir DIR] - %s\n", ColorGreen, ColorReset, T("help_logs"))
	fmt.Printf("           (alias compatible: %slogs%s)\n", ColorGreen, ColorReset)
	fmt.Printf("           (%s)\n", T("help_logs_interactive"))
	fmt.Printf("  %srunit-journal%s       - selector/visor de logs de servicios (incluido como segundo binario)\n", ColorGreen, ColorReset)
	fmt.Printf("  %stui%s                  - %s\n", ColorGreen, ColorReset, T("help_tui"))
	os.Exit(0)
}

func runSvCommand(command, service string) bool {
	cmd := exec.Command("sv", command, service)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return err == nil
}

// parseArgs extrae un flag opcional --lang/-l (en cualquier posición) y
// devuelve el idioma solicitado junto con los argumentos restantes.
func parseArgs(args []string) (lang string, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--lang" || a == "-l":
			if i+1 < len(args) {
				lang = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--lang="):
			lang = strings.TrimPrefix(a, "--lang=")
		default:
			rest = append(rest, a)
		}
	}
	return
}

func main() {
	lang, args := parseArgs(os.Args[1:])
	initI18n(lang)

	if len(args) < 1 {
		printHelp()
	}

	action := args[0]

	if action == "--help" || action == "-h" || action == "help" {
		printHelp()
	}

	if action == "tui" {
		checkRoot()
		runTUI()
		return
	}

	if requiresRoot(action) {
		checkRoot()
	}

	var service string
	if len(args) >= 2 {
		service = args[1]
	}
	if service != "" {
		if err := validateServiceName(service); err != nil {
			errorMsg(err.Error())
			os.Exit(2)
		}
	}

	servicePath := filepath.Join(RunsvDir, service)
	serviceSv := filepath.Join(SvDir, service)

	switch action {
	case "start":
		if service == "" {
			errorMsg(T("specify_start"))
			os.Exit(1)
		}
		if !exists(servicePath) {
			if exists(serviceSv) {
				fmt.Println(T("not_enabled_hint", service, service))
			} else {
				fmt.Println(T("not_exists_all", service))
			}
			os.Exit(1)
		}
		info(T("starting", service))
		if runSvCommand("up", service) {
			success(T("started_ok", service))
		} else {
			errorMsg(T("started_err", service))
			os.Exit(1)
		}

	case "stop":
		if service == "" {
			errorMsg(T("specify_stop"))
			os.Exit(1)
		}
		if !exists(servicePath) {
			fmt.Println(T("not_exists_active", service))
			os.Exit(1)
		}
		info(T("stopping", service))
		if runSvCommand("down", service) {
			success(T("stopped_ok", service))
		} else {
			errorMsg(T("stopped_err", service))
			os.Exit(1)
		}

	case "restart":
		if service == "" {
			errorMsg(T("specify_restart"))
			os.Exit(1)
		}
		if !exists(servicePath) {
			fmt.Println(T("not_exists_generic", service))
			os.Exit(1)
		}
		info(T("restarting", service))
		if !runSvCommand("restart", service) {
			errorMsg(fmt.Sprintf("No se pudo reiniciar '%s'.", service))
			os.Exit(1)
		}

	case "reload":
		if service == "" {
			errorMsg(T("specify_reload"))
			os.Exit(1)
		}
		info(T("reloading", service))
		if !runSvCommand("hup", service) {
			errorMsg(fmt.Sprintf("No se pudo recargar '%s'.", service))
			os.Exit(1)
		}

	case "pause":
		if service == "" {
			errorMsg(T("specify_pause"))
			os.Exit(1)
		}
		info(T("pausing", service))
		if !runSvCommand("pause", service) {
			errorMsg(fmt.Sprintf("No se pudo pausar '%s'.", service))
			os.Exit(1)
		}

	case "cont":
		if service == "" {
			errorMsg(T("specify_cont"))
			os.Exit(1)
		}
		info(T("resuming", service))
		if !runSvCommand("cont", service) {
			errorMsg(fmt.Sprintf("No se pudo reanudar '%s'.", service))
			os.Exit(1)
		}

	case "status":
		if service == "" {
			errorMsg(T("specify_status"))
			os.Exit(1)
		}
		if !exists(servicePath) {
			fmt.Println(T("not_exists_generic", service))
			os.Exit(1)
		}
		fmt.Print(renderStatus(service))

	case "enable":
		if service == "" {
			errorMsg(T("specify_enable"))
			os.Exit(1)
		}
		if !exists(serviceSv) {
			fmt.Println(T("not_exists_svdir", service, SvDir))
			os.Exit(1)
		}
		if exists(servicePath) {
			warn(T("already_enabled", service))
			os.Exit(0)
		}

		info(T("enabling", service))
		err := os.Symlink(serviceSv, servicePath)
		if err == nil {
			success(T("enabled_ok", service))
		} else {
			errorMsg(T("enabled_err", service, err))
			os.Exit(1)
		}

	case "disable":
		if service == "" {
			errorMsg(T("specify_disable"))
			os.Exit(1)
		}
		if !exists(servicePath) {
			warn(T("already_disabled", service))
			os.Exit(0)
		}

		info(T("disabling", service))
		err := os.Remove(servicePath)
		if err == nil {
			success(T("disabled_ok", service))
		} else {
			errorMsg(T("disabled_err", service, err))
			os.Exit(1)
		}

	case "mask":
		if service == "" {
			errorMsg(T("specify_mask"))
			os.Exit(1)
		}
		if !exists(serviceSv) {
			errorMsg(T("not_exists_svdir", service, SvDir))
			os.Exit(1)
		}
		downFile := filepath.Join(serviceSv, "down")
		file, err := os.Create(downFile)
		if err != nil {
			errorMsg(T("masked_err", service, err))
			os.Exit(1)
		}
		if err := file.Close(); err != nil {
			errorMsg(T("masked_err", service, err))
			os.Exit(1)
		}
		success(T("masked_ok", service))

	case "unmask":
		if service == "" {
			errorMsg(T("specify_unmask"))
			os.Exit(1)
		}
		downFile := filepath.Join(serviceSv, "down")
		if exists(downFile) {
			err := os.Remove(downFile)
			if err != nil {
				errorMsg(T("unmasked_err", service, err))
				os.Exit(1)
			}
			success(T("unmasked_ok", service))
		} else {
			warn(T("unmask_not_masked", service))
		}

	case "list-active":
		fmt.Printf("%s%s%s\n\n", Bold, T("active_services"), ColorReset)
		files, err := os.ReadDir(RunsvDir)
		if err != nil {
			errorMsg(T("read_error", RunsvDir, err))
			os.Exit(1)
		}
		for _, file := range files {
			fmt.Printf("  %s●%s %s\n", ColorGreen, ColorReset, file.Name())
		}

	case "list-all":
		fmt.Printf("%s%s%s\n\n", Bold, T("all_services"), ColorReset)
		files, err := os.ReadDir(SvDir)
		if err != nil {
			errorMsg(T("read_error", SvDir, err))
			os.Exit(1)
		}
		for _, file := range files {
			name := file.Name()
			if exists(filepath.Join(RunsvDir, name)) {
				fmt.Printf("  %s●%s %-20s %s(%s)%s\n", ColorGreen, ColorReset, name, ColorGreen, T("enabled_tag"), ColorReset)
			} else {
				fmt.Printf("  %s○%s %-20s %s(%s)%s\n", ColorRed, ColorReset, name, ColorYellow, T("disabled_tag"), ColorReset)
			}
		}

	case "log", "logs":
		var extra []string
		if len(args) > 2 {
			extra = args[2:]
		}
		cmdLogs(service, extra)

	case "search":
		if service == "" {
			errorMsg(T("specify_search"))
			os.Exit(1)
		}
		fmt.Printf("%s%s%s\n\n", Bold, T("search_results", service), ColorReset)
		files, err := os.ReadDir(SvDir)
		if err != nil {
			errorMsg(T("read_error", SvDir, err))
			os.Exit(1)
		}
		count := 0
		for _, file := range files {
			name := file.Name()
			if strings.Contains(name, service) {
				count++
				if exists(filepath.Join(RunsvDir, name)) {
					fmt.Printf("  %s●%s %-20s %s(%s)%s\n", ColorGreen, ColorReset, name, ColorGreen, T("enabled_tag"), ColorReset)
				} else {
					fmt.Printf("  %s○%s %-20s %s(%s)%s\n", ColorRed, ColorReset, name, ColorYellow, T("disabled_tag"), ColorReset)
				}
			}
		}
		if count == 0 {
			fmt.Println(T("no_matches"))
		}

	default:
		errorMsg(T("unknown_action"))
		os.Exit(1)
	}
}

// renderStatus construye el mismo bloque de estado detallado que antes
// imprimía directamente el comando "status", pero devuelto como string para
// poder reutilizarlo tanto en el CLI como en la TUI.
func renderStatus(service string) string {
	var b strings.Builder

	out, err := exec.Command("sv", "status", service).CombinedOutput()
	output := string(out)

	fmt.Fprintf(&b, "%s%s%s %s\n", Bold, T("status_service"), ColorReset, service)
	if err != nil || strings.HasPrefix(output, "fail:") {
		fmt.Fprintf(&b, "  %s %s%s%s\n", T("status_state"), ColorRed, T("status_no_response"), ColorReset)
		fmt.Fprintf(&b, "  %s\n", T("status_detail", output))
		return b.String()
	}

	if strings.HasPrefix(output, "run:") {
		fields := strings.Fields(output)
		pid := "?"
		uptime := "0s"

		if len(fields) >= 5 {
			pid = strings.Trim(fields[3], "()")
			uptime = strings.Trim(fields[4], ";")
		}

		fmt.Fprintf(&b, "  %s %s%s%s\n", T("status_state"), ColorGreen, T("status_running"), ColorReset)
		fmt.Fprintf(&b, "  %s %s\n", T("status_pid"), pid)
		fmt.Fprintf(&b, "  %s %s\n", T("status_uptime"), uptime)
		fmt.Fprintf(&b, "  %s %s%s%s\n", T("status_enabled_label"), ColorGreen, T("yes"), ColorReset)
	} else {
		fmt.Fprintf(&b, "  %s %s%s%s\n", T("status_state"), ColorRed, T("status_stopped"), ColorReset)
	}

	return b.String()
}
