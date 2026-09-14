module runitctl

go 1.22.2

require github.com/gdamore/tcell/v2 v2.8.1

require (
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/rivo/uniseg v0.4.3 // indirect
	golang.org/x/sys v0.29.0 // indirect
	golang.org/x/term v0.28.0 // indirect
	golang.org/x/text v0.21.0 // indirect
)

// Nota: golang.org/x/* usa "vanity import paths" (redirecciones HTTP) que
// pueden no estar accesibles en redes restringidas (p. ej. sandboxes de
// compilación sin acceso a golang.org). Estos replace apuntan a los mismos
// commits/tags servidos directamente desde sus mirrors en GitHub. En una
// máquina con acceso normal a internet estas líneas son inofensivas: el
// contenido es idéntico, solo cambia el host de descarga.
replace golang.org/x/sys => github.com/golang/sys v0.29.0
replace golang.org/x/term => github.com/golang/term v0.28.0
replace golang.org/x/text => github.com/golang/text v0.21.0
