package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"runitctl/internal/runitlog"
)

// version se sobrescribe en la compilación: -ldflags "-X main.version=1.2.3".
var version = "1.0"

// svCommandTimeout acota las consultas/acciones de 'sv' para que un runsv
// colgado no congele la CLI ni la TUI.
const svCommandTimeout = 10 * time.Second

// Rutas de runit. Los valores por defecto sirven para Void Linux y derivadas;
// pueden cambiarse con variables de entorno (útil en otras distribuciones y
// para las pruebas):
//
//	RUNITCTL_SV_DIR  directorio con las definiciones de servicio (/etc/sv)
//	RUNITCTL_SVDIR   directorio supervisado por runsvdir (por defecto $SVDIR
//	                 si está definido; si no, /etc/runit/runsvdir/default)
var (
	SvDir    = envOr("RUNITCTL_SV_DIR", "/etc/sv")
	RunsvDir = envOr("RUNITCTL_SVDIR", envOr("SVDIR", "/etc/runit/runsvdir/default"))
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Códigos ANSI para color y formato. Se vacían cuando la salida no es una
// terminal o el usuario definió NO_COLOR (https://no-color.org), para que
// `runitctl status x | grep ...` o las redirecciones no lleven basura.
var (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	Bold        = "\033[1m"
)

func init() {
	if !colorEnabled() {
		ColorReset, ColorRed, ColorGreen, ColorYellow, ColorBlue, Bold = "", "", "", "", "", ""
	}
}

func colorEnabled() bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	st, err := os.Stdout.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func info(msg string)     { fmt.Printf("%s%s%s\n", ColorBlue, msg, ColorReset) }
func success(msg string)  { fmt.Printf("%s%s%s\n", ColorGreen, msg, ColorReset) }
func warn(msg string)     { fmt.Printf("%s%s%s\n", ColorYellow, msg, ColorReset) }
func errorMsg(msg string) { fmt.Fprintf(os.Stderr, "%s%s%s\n", ColorRed, msg, ColorReset) }

func requiresRoot(action string) bool {
	switch action {
	case "start", "stop", "restart", "reload", "pause", "cont", "enable", "disable", "mask", "unmask", "tui", "setup-logs":
		return true
	default:
		return false
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// lexists es como exists pero sin seguir enlaces simbólicos: un servicio
// habilitado cuyo destino desapareció (enlace roto) sigue "existiendo" como
// enlace y debe poder deshabilitarse, en vez de quedar atascado entre
// "ya deshabilitado" y "el archivo ya existe".
func lexists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func printHelp() {
	fmt.Printf("%s%s%s  v%s\n\n", Bold, T("help_title"), ColorReset, version)
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
	for _, a := range advancedOrder {
		fmt.Printf("  %s%-5s%s <servicio>      - %s\n", ColorGreen, a, ColorReset, T("help_"+a))
	}
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
	fmt.Printf("  %srunit-journal%s       - pantalla para elegir un servicio y ver sus registros (programa aparte)\n", ColorGreen, ColorReset)
	fmt.Printf("  %srunlevel%s [nivel]     - %s\n", ColorGreen, ColorReset, T("help_runlevel"))
	fmt.Printf("  %sdoctor%s               - %s\n", ColorGreen, ColorReset, T("help_doctor"))
	fmt.Printf("  %s--version%s, -v, -V    - %s\n", ColorGreen, ColorReset, T("help_version"))
	fmt.Printf("  %ssetup-logs%s           - %s\n", ColorGreen, ColorReset, T("help_setup_logs"))
	fmt.Printf("  %stui%s                  - %s\n", ColorGreen, ColorReset, T("help_tui"))
	os.Exit(0)
}

// svEnv apunta SVDIR de 'sv' al mismo directorio que runitctl usa para decidir
// si un servicio está habilitado, de modo que ambos hablen del mismo árbol.
func svEnv() []string { return append(os.Environ(), "SVDIR="+RunsvDir) }

// runSvCommand ejecuta 'sv' mostrando su salida directamente (modo CLI).
func runSvCommand(command, service string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), svCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sv", command, service)
	cmd.Env = svEnv()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run() == nil
}

// runSvCaptured ejecuta 'sv' capturando su salida (modo TUI, donde escribir
// directo a stdout/stderr corrompería la pantalla de tcell) y con tiempo
// máximo. Devuelve la salida combinada y si terminó con éxito.
func runSvCaptured(command, service string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), svCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sv", command, service)
	cmd.Env = svEnv()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	if ctx.Err() == context.DeadlineExceeded {
		text = strings.TrimSpace(text + "\nEl servicio tardó demasiado en responder.")
	}
	return text, err == nil
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
	if action == "setup-logs" {
		checkRoot()
		cmdSetupLogging()
		return
	}
	if action == "--version" || action == "version" || action == "-v" || action == "-V" {
		fmt.Println("runitctl " + version)
		return
	}

	if action == "tui" {
		checkRoot()
		runTUI()
		return
	}

	if requiresRoot(action) || (isAdvancedAction(action) && action != "check") || (action == "runlevel" && len(args) >= 2) {
		checkRoot()
	}

	var service string
	if len(args) >= 2 {
		service = args[1]
	}
	if service != "" {
		if err := runitlog.ValidateName(service); err != nil {
			errorMsg(err.Error())
			os.Exit(2)
		}
	}

	switch {
	case action == "doctor":
		cmdDoctor()
		return
	case action == "runlevel":
		cmdRunlevel(args)
		return
	case isAdvancedAction(action):
		runAdvanced(action, service)
		return
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
			errorMsg(T("restart_err", service))
			os.Exit(1)
		}

	case "reload":
		if service == "" {
			errorMsg(T("specify_reload"))
			os.Exit(1)
		}
		info(T("reloading", service))
		if !runSvCommand("hup", service) {
			errorMsg(T("reload_err", service))
			os.Exit(1)
		}

	case "pause":
		if service == "" {
			errorMsg(T("specify_pause"))
			os.Exit(1)
		}
		info(T("pausing", service))
		if !runSvCommand("pause", service) {
			errorMsg(T("pause_err", service))
			os.Exit(1)
		}

	case "cont":
		if service == "" {
			errorMsg(T("specify_cont"))
			os.Exit(1)
		}
		info(T("resuming", service))
		if !runSvCommand("cont", service) {
			errorMsg(T("cont_err", service))
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
		if lexists(servicePath) {
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
		if !lexists(servicePath) {
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
		file, err := os.OpenFile(downFile, os.O_WRONLY|os.O_CREATE, 0o644)
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
		needle := strings.ToLower(service)
		for _, file := range files {
			name := file.Name()
			if strings.Contains(strings.ToLower(name), needle) {
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
		errorMsg(T("unknown_action") + " (" + action + ")")
		os.Exit(2)
	}
}

// renderStatus construye el mismo bloque de estado detallado que antes
// imprimía directamente el comando "status", pero devuelto como string para
// poder reutilizarlo tanto en el CLI como en la TUI.
func renderStatus(service string) string {
	var b strings.Builder

	output, ok := runSvCaptured("status", service)
	var err error
	if !ok {
		err = fmt.Errorf("sv status falló")
	}

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
