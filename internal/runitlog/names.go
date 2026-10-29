// Package runitlog reúne las utilidades compartidas por runitctl y
// runit-journal: validación de nombres de servicio, lectura de las últimas
// líneas de un log, decodificación de TAI64N, seguimiento robusto de
// archivos (con rotación) y captura de logs a disco.
package runitlog

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateName comprueba que name sea un nombre de servicio seguro: solo
// letras, números, punto, guion y guion bajo, sin empezar por símbolo. Evita
// que una entrada pueda escapar de los directorios de runit (../, /, etc.).
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("Escribe el nombre de un servicio.")
	}
	if len(name) > 255 || !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("El nombre %q no es válido: usa solo letras, números, punto, guion y guion bajo.", name)
	}
	return nil
}

// SafeLogName convierte un nombre de servicio en algo seguro para usar como
// parte de un nombre de archivo.
func SafeLogName(service string) string {
	name := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, service)
	if name == "" {
		return "service"
	}
	return name
}

// DefaultUserLogDir devuelve una carpeta escribible por el usuario actual
// para las capturas (-g). No usa /var/log: -g funciona sin privilegios.
func DefaultUserLogDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "runitctl", "logs")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "runitctl", "logs")
	}
	return filepath.Join(".", ".runitctl-logs")
}

// OpenCapture crea (0600, en una carpeta 0700) el archivo de captura
// <servicio>-<AAAAMMDD-HHMMSS>.log y escribe una cabecera. Si ya existe uno
// con el mismo segundo, añade un sufijo numérico en vez de fallar.
func OpenCapture(service, dir string) (*os.File, error) {
	if dir == "" {
		dir = DefaultUserLogDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	now := time.Now()
	base := SafeLogName(service) + "-" + now.Format("20060102-150405")
	var lastErr error
	for i := 0; i < 100; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		f, err := os.OpenFile(filepath.Join(dir, name+".log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			fmt.Fprintf(f, "# runitctl capture\n# service: %s\n# started: %s\n\n", service, now.Format(time.RFC3339))
			return f, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}
