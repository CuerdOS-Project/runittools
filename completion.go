// SPDX-License-Identifier: GPL-3.0-or-later
//
// runitctl - administrador de servicios runit.
// Este programa es software libre: puedes redistribuirlo o modificarlo bajo
// los términos de la GNU General Public License publicada por la Free
// Software Foundation, ya sea la versión 3 o (a tu elección) cualquier
// versión posterior. Se distribuye sin ninguna garantía; consulta el
// archivo LICENSE.

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"runitctl/internal/runitlog"
)

// ---------------------------------------------------------------------
// Autocompletado de comandos, flags y servicios (bash, zsh y fish)
//
// Toda la lógica vive aquí: los scripts de cada shell son envoltorios finos
// que llaman a `runitctl __complete PROGRAMA PALABRAS...` y muestran lo que
// devuelve. Así los comandos, flags y servicios siempre coinciden con el
// binario instalado y las descripciones salen en el idioma del usuario.
//
// Protocolo de salida de __complete: la primera línea es una directiva
// ("none" o "dirs": completar carpetas) y cada línea siguiente es un
// candidato "valor<TAB>descripción" (la descripción es opcional).
// ---------------------------------------------------------------------

// completionShells son las shells para las que se genera script.
var completionShells = []string{"bash", "zsh", "fish"}

// supportedLangCodes devuelve los códigos de idioma ordenados.
func supportedLangCodes() []string {
	out := make([]string, 0, len(supportedLangs))
	for k := range supportedLangs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type compItem struct{ Value, Desc string }

// compFlag describe una opción: todos sus nombres, la clave de traducción
// de su descripción y si espera un valor.
type compFlag struct {
	Names []string
	Key   string
	Value bool
}

var (
	langFlag    = compFlag{[]string{"--lang", "-l"}, "comp_flag_lang", true}
	helpFlag    = compFlag{[]string{"--help", "-h"}, "comp_flag_help", false}
	versionFlag = compFlag{[]string{"--version", "-v", "-V"}, "help_version", false}

	globalFlags = []compFlag{langFlag, helpFlag, versionFlag}

	logFlags = []compFlag{
		{[]string{"--lines", "-n"}, "comp_flag_lines", true},
		{[]string{"--follow", "-f"}, "comp_flag_follow", false},
		{[]string{"--interactive", "-i"}, "comp_flag_interactive", false},
		{[]string{"--grab", "-g"}, "comp_flag_grab", false},
		{[]string{"--log-dir"}, "comp_flag_logdir", true},
		langFlag,
	}

	journalFlags = []compFlag{
		{[]string{"-n"}, "comp_flag_lines", true},
		{[]string{"-f"}, "comp_flag_follow", false},
		{[]string{"-g"}, "comp_flag_grab", false},
		{[]string{"--log-dir"}, "comp_flag_logdir", true},
		{[]string{"--home"}, "comp_flag_home", false},
		{[]string{"--version", "-version", "-v", "-V"}, "help_version", false},
		{[]string{"--help", "-h"}, "comp_flag_help", false},
	}
)

type compCommand struct{ Name, HelpKey string }

// completionCommands devuelve los comandos de runitctl en el orden de la ayuda.
func completionCommands() []compCommand {
	cmds := []compCommand{
		{"start", "help_start"}, {"stop", "help_stop"}, {"restart", "help_restart"},
		{"reload", "help_reload"}, {"pause", "help_pause"}, {"cont", "help_cont"},
		{"status", "help_status"},
	}
	for _, a := range advancedOrder {
		cmds = append(cmds, compCommand{a, "help_" + a})
	}
	return append(cmds,
		compCommand{"enable", "help_enable"}, compCommand{"disable", "help_disable"},
		compCommand{"mask", "help_mask"}, compCommand{"unmask", "help_unmask"},
		compCommand{"list-active", "help_list_active"}, compCommand{"list-all", "help_list_all"},
		compCommand{"search", "help_search"},
		compCommand{"log", "help_logs"}, compCommand{"logs", "help_logs"},
		compCommand{"runlevel", "help_runlevel"}, compCommand{"doctor", "help_doctor"},
		compCommand{"setup-logs", "help_setup_logs"}, compCommand{"tui", "help_tui"},
		compCommand{"completion", "help_completion"},
	)
}

// serviceKind indica de dónde salen los nombres que admite cada comando.
func serviceKind(cmd string) string {
	switch cmd {
	case "enable", "mask", "unmask":
		return "available" // definiciones en /etc/sv
	case "log", "logs":
		return "all"
	case "start", "stop", "restart", "reload", "pause", "cont", "status", "disable":
		return "active" // enlaces en el directorio supervisado
	}
	if isAdvancedAction(cmd) {
		return "active"
	}
	return ""
}

// listNames devuelve los nombres válidos de servicio de un directorio.
func listNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if runitlog.ValidateName(e.Name()) == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

func completeServices(kind string) []string {
	var names []string
	switch kind {
	case "active":
		names = listNames(RunsvDir)
	case "available":
		names = listNames(SvDir)
	default:
		names = append(listNames(SvDir), listNames(RunsvDir)...)
	}
	sort.Strings(names)
	out := names[:0]
	for i, n := range names {
		if i == 0 || n != names[i-1] {
			out = append(out, n)
		}
	}
	return out
}

func filterPrefix(items []compItem, prefix string) []compItem {
	out := []compItem{}
	for _, it := range items {
		if strings.HasPrefix(it.Value, prefix) {
			out = append(out, it)
		}
	}
	return out
}

func plainItems(values []string) []compItem {
	out := make([]compItem, len(values))
	for i, v := range values {
		out[i] = compItem{Value: v}
	}
	return out
}

// flagItems lista las opciones que aún no se escribieron en la línea.
func flagItems(flags []compFlag, used map[string]bool) []compItem {
	var out []compItem
	for _, f := range flags {
		taken := false
		for _, n := range f.Names {
			taken = taken || used[n]
		}
		if taken {
			continue
		}
		for _, n := range f.Names {
			out = append(out, compItem{n, T(f.Key)})
		}
	}
	return out
}

// valueFlags son las opciones que consumen la palabra siguiente.
func isValueFlag(prog, w string) bool {
	switch w {
	case "-n", "--lines", "--log-dir":
		return true
	case "--lang", "-l":
		return prog == "runitctl"
	case "-log-dir":
		return prog == "runit-journal"
	}
	return false
}

// complete calcula los candidatos para la línea de comandos. words son las
// palabras después del nombre del programa; la última es la que se está
// escribiendo (puede estar vacía). Devuelve la directiva y los candidatos.
func complete(prog string, words []string) (string, []compItem) {
	if len(words) == 0 {
		words = []string{""}
	}
	cur := words[len(words)-1]
	before := words[:len(words)-1]

	// --lang=xx en una sola palabra.
	if prog == "runitctl" && strings.HasPrefix(cur, "--lang=") {
		var out []compItem
		for _, l := range supportedLangCodes() {
			out = append(out, compItem{Value: "--lang=" + l})
		}
		return "none", filterPrefix(out, cur)
	}

	// Valor pendiente de una opción.
	if n := len(before); n > 0 && isValueFlag(prog, before[n-1]) {
		switch before[n-1] {
		case "--log-dir", "-log-dir":
			return "dirs", nil
		case "--lang", "-l":
			return "none", filterPrefix(plainItems(supportedLangCodes()), cur)
		}
		return "none", nil
	}

	// Comando y posicionales ya escritos.
	cmd, npos := "", 0
	used := map[string]bool{}
	for i := 0; i < len(before); i++ {
		w := before[i]
		switch {
		case strings.HasPrefix(w, "-") && w != "-":
			used[w] = true
			if isValueFlag(prog, w) {
				i++
			}
		case prog == "runitctl" && cmd == "":
			cmd = w
		default:
			npos++
		}
	}

	wantsFlag := strings.HasPrefix(cur, "-")
	filter := func(items []compItem) (string, []compItem) {
		return "none", filterPrefix(items, cur)
	}

	if prog == "runit-journal" {
		if wantsFlag {
			return filter(flagItems(journalFlags, used))
		}
		if npos == 0 {
			return filter(plainItems(completeServices("all")))
		}
		return "none", nil
	}

	if cmd == "" {
		if wantsFlag {
			return filter(flagItems(globalFlags, used))
		}
		var items []compItem
		for _, c := range completionCommands() {
			items = append(items, compItem{c.Name, T(c.HelpKey)})
		}
		return filter(items)
	}

	if wantsFlag {
		if cmd == "log" || cmd == "logs" {
			return filter(flagItems(logFlags, used))
		}
		return filter(flagItems([]compFlag{langFlag, helpFlag}, used))
	}
	if npos > 0 {
		return "none", nil
	}
	switch cmd {
	case "runlevel":
		levels, _ := listLevels()
		return filter(plainItems(levels))
	case "completion":
		return filter(plainItems(completionShells))
	}
	if kind := serviceKind(cmd); kind != "" {
		return filter(plainItems(completeServices(kind)))
	}
	return "none", nil
}

// cmdComplete implementa el comando oculto `__complete PROGRAMA PALABRAS...`.
func cmdComplete(args []string) {
	if len(args) == 0 {
		return
	}
	directive, items := complete(args[0], args[1:])
	fmt.Println(directive)
	for _, it := range items {
		if it.Desc != "" {
			fmt.Printf("%s\t%s\n", it.Value, strings.ReplaceAll(it.Desc, "\n", " "))
		} else {
			fmt.Println(it.Value)
		}
	}
}

// cmdCompletion imprime el script de autocompletado de la shell indicada.
func cmdCompletion(args []string) {
	if len(args) == 0 {
		errorMsg(T("comp_specify_shell"))
		os.Exit(1)
	}
	script, ok := completionScripts[args[0]]
	if !ok {
		errorMsg(T("comp_unknown_shell", args[0]))
		os.Exit(2)
	}
	fmt.Print(script)
}

var completionScripts = map[string]string{
	"bash": bashCompletion,
	"zsh":  zshCompletion,
	"fish": fishCompletion,
}

const bashCompletion = `# Autocompletado de bash para runitctl y runit-journal.
# Cargar con:  source <(runitctl completion bash)
_runitctl_complete() {
    local prog=${COMP_WORDS[0]##*/} bin=runitctl line directive=none first=1 out
    [ "$prog" = runitctl ] && command -v "${COMP_WORDS[0]}" >/dev/null 2>&1 && bin=${COMP_WORDS[0]}
    out=$("$bin" __complete "$prog" "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null) || return 0
    COMPREPLY=()
    while IFS= read -r line; do
        if [ "$first" = 1 ]; then directive=$line; first=0; continue; fi
        [ -n "$line" ] && COMPREPLY+=("${line%%$'\t'*}")
    done <<<"$out"
    if [ "$directive" = dirs ]; then
        compopt -o filenames 2>/dev/null
        COMPREPLY=($(compgen -d -- "${COMP_WORDS[COMP_CWORD]}"))
    fi
    return 0
}
complete -F _runitctl_complete runitctl runit-journal
`

const zshCompletion = `#compdef runitctl runit-journal
# Autocompletado de zsh para runitctl y runit-journal.
# Cargar con:  source <(runitctl completion zsh)
# o guardar como "_runitctl" en una carpeta de $fpath.
_runitctl_complete() {
    local prog=${words[1]:t} bin=runitctl line v d directive
    local -a out pairs
    [[ $prog == runitctl ]] && (( $+commands[${words[1]}] )) && bin=${words[1]}
    out=("${(@f)$("$bin" __complete "$prog" "${(@)words[2,CURRENT]}" 2>/dev/null)}")
    directive=${out[1]}
    out=("${(@)out[2,-1]}")
    if [[ $directive == dirs ]]; then
        _path_files -/
        return
    fi
    for line in "${out[@]}"; do
        [[ -z $line ]] && continue
        v=${line%%$'\t'*}
        if [[ $line == *$'\t'* ]]; then
            d=${line#*$'\t'}
            pairs+=("${v//:/\\:}:${d//:/\\:}")
        else
            pairs+=("${v//:/\\:}")
        fi
    done
    (( ${#pairs} )) && _describe -t items "$prog" pairs
}
if [[ $funcstack[1] == _runitctl ]]; then
    _runitctl_complete "$@"
else
    compdef _runitctl_complete runitctl runit-journal
fi
`

const fishCompletion = `# Autocompletado de fish para runitctl y runit-journal.
# Cargar con:  runitctl completion fish | source
function __runitctl_complete
    set -l toks (commandline -opc)
    set -l cur (commandline -ct)
    set -l prog (basename $toks[1])
    set -e toks[1]
    set -l out (runitctl __complete $prog $toks "$cur" 2>/dev/null)
    test (count $out) -gt 0; or return
    set -l directive $out[1]
    set -e out[1]
    if test "$directive" = dirs
        __fish_complete_directories "$cur"
        return
    end
    test (count $out) -gt 0; and printf '%s\n' $out
end
complete -c runitctl -f -a '(__runitctl_complete)'
complete -c runit-journal -f -a '(__runitctl_complete)'
`
