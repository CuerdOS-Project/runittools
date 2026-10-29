package main

import (
	"bytes"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// Dependencias de lectura de logs en Void Linux.
//
// svlogd (log/current) viene con el propio paquete runit. Para los logs de
// sistema (syslog) Void recomienda socklog: el paquete socklog-void trae
// 'socklog', 'svlogtail' y los servicios de runit que lo alimentan.
const (
	logPackage = "socklog-void"
	logGroup   = "socklog" // los miembros pueden leer /var/log/socklog sin ser root
)

// logServices son los servicios que deben estar habilitados para que existan
// los logs de socklog: socklog-unix recibe /dev/log y nanoklogd el kernel.
var logServices = []string{"socklog-unix", "nanoklogd"}

// runCmd ejecuta un comando externo y devuelve su salida combinada. Es una
// variable para poder sustituirla en las pruebas.
var runCmd = func(name string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func isVoid() bool {
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "ID=") && strings.Trim(strings.TrimPrefix(l, "ID="), `"'`) == "void" {
				return true
			}
		}
	}
	_, err := exec.LookPath("xbps-install")
	return err == nil
}

func packageInstalled(pkg string) bool {
	_, err := runCmd("xbps-query", pkg)
	return err == nil
}

// setupLogging instala (si faltan) las dependencias de lectura de logs y
// habilita sus servicios. Es idempotente: se puede ejecutar varias veces.
// Devuelve false si algún paso falló.
func setupLogging(w func(kind, msg string)) bool {
	ok := true
	if !isVoid() {
		w("warn", T("setup_not_void"))
		return false
	}

	// 1. Paquete
	if packageInstalled(logPackage) {
		w("ok", T("setup_pkg_present", logPackage))
	} else {
		w("info", T("setup_pkg_installing", logPackage))
		if out, err := runCmd("xbps-install", "-Sy", logPackage); err != nil {
			w("err", T("setup_pkg_failed", logPackage, out))
			return false // sin paquete no hay servicios que habilitar
		}
		w("ok", T("setup_pkg_installed", logPackage))
	}

	// 2. Servicios
	for _, svc := range logServices {
		msg, good := enableService(svc)
		if good {
			w("ok", msg)
		} else {
			w("err", msg)
			ok = false
		}
	}

	// 3. Permiso de lectura para el usuario que invocó sudo/doas
	if u := invokingUser(); u != "" {
		if g, err := user.Lookup(u); err == nil && !inGroup(g, logGroup) {
			if out, err := runCmd("usermod", "-aG", logGroup, u); err != nil {
				w("err", T("setup_group_failed", u, logGroup, out))
				ok = false
			} else {
				w("ok", T("setup_group_added", u, logGroup))
			}
		}
	}
	if ok {
		w("info", T("setup_done"))
	}
	return ok
}

func invokingUser() string {
	for _, k := range []string{"SUDO_USER", "DOAS_USER"} {
		if v := os.Getenv(k); v != "" && v != "root" {
			return v
		}
	}
	return ""
}

func inGroup(u *user.User, group string) bool {
	gr, err := user.LookupGroup(group)
	if err != nil {
		return true // el grupo no existe: nada que añadir
	}
	ids, err := u.GroupIds()
	if err != nil {
		return false
	}
	for _, id := range ids {
		if id == gr.Gid {
			return true
		}
	}
	return false
}

func cmdSetupLogging() {
	ok := setupLogging(func(kind, msg string) {
		switch kind {
		case "ok":
			success(msg)
		case "warn":
			warn(msg)
		case "err":
			errorMsg(msg)
		default:
			info(msg)
		}
	})
	if !ok {
		os.Exit(1)
	}
}
