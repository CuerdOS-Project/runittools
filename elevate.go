package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Elevación de privilegios. Si una acción necesita root y el usuario no lo
// es, en una terminal interactiva se ofrece relanzar el mismo comando con
// sudo/doas (por defecto: sí). Variables de entorno:
//
//	RUNITCTL_NO_ELEVATE=1    nunca ofrecer elevación (solo mostrar el error)
//	RUNITCTL_AUTO_ELEVATE=1  elevar sin preguntar
//
// Fuera de una terminal (pipes, cron, scripts) nunca se pregunta ni se eleva.

// findElevator devuelve el programa para elevar privilegios, o "".
func findElevator() string {
	for _, name := range []string{"sudo", "doas"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// askYesNo lee una respuesta de r. Una línea vacía devuelve def; acepta
// s/si/sí/y/yes como sí y cualquier otra cosa como no.
func askYesNo(r io.Reader, w io.Writer, prompt string, def bool) bool {
	fmt.Fprint(w, prompt)
	line, _ := bufio.NewReader(r).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "s", "si", "sí", "y", "yes", "sim", "oui":
		return true
	}
	return false
}

// elevateArgs arma la línea de comando para relanzar runitctl con el elevador.
func elevateArgs(elevator, exe string, args []string) []string {
	return append([]string{elevator, exe}, args...)
}

func checkRoot() {
	if os.Geteuid() == 0 {
		return
	}
	if os.Getenv("RUNITCTL_NO_ELEVATE") == "" && stdinIsTerminal() {
		if elev := findElevator(); elev != "" {
			auto := os.Getenv("RUNITCTL_AUTO_ELEVATE") != ""
			name := strings.TrimSuffix(elev[strings.LastIndex(elev, "/")+1:], "")
			if auto || askYesNo(os.Stdin, os.Stderr, T("elevate_prompt", name), true) {
				exe, err := os.Executable()
				if err == nil {
					err = syscall.Exec(elev, elevateArgs(name, exe, os.Args[1:]), os.Environ())
				}
				errorMsg(T("elevate_failed", err)) // Exec solo regresa si falla
				os.Exit(1)
			}
		}
	}
	errorMsg(T("root_required"))
	fmt.Printf("%s%s%s\n", ColorGreen, T("try_again", os.Args[0], strings.Join(os.Args[1:], " ")), ColorReset)
	os.Exit(1)
}
