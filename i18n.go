package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

//go:embed locales/*.kn
var localesFS embed.FS

var (
	catalog     map[string]string
	fallback    map[string]string
	currentLang string
)

// supportedLangs mapea códigos cortos a nombres de archivo de locale.
// Los locales usan la extensión .kn (formato JSON de clave/valor).
var supportedLangs = map[string]string{
	"es": "es.kn",
	"en": "en.kn",
	"pt": "pt.kn",
	"ca": "ca.kn",
}

// detectLang determina el idioma a usar, en este orden de prioridad:
// 1. Flag --lang=xx o -l xx (procesado externamente y pasado aquí)
// 2. Variable de entorno RUNITCTL_LANG
// 3. Variable de entorno LANG / LC_ALL / LC_MESSAGES del sistema
// 4. Español como idioma por defecto
func detectLang(explicit string) string {
	if explicit != "" {
		if _, ok := supportedLangs[explicit]; ok {
			return explicit
		}
	}
	if v := os.Getenv("RUNITCTL_LANG"); v != "" {
		code := normalizeLangCode(v)
		if _, ok := supportedLangs[code]; ok {
			return code
		}
	}
	for _, envVar := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(envVar); v != "" {
			code := normalizeLangCode(v)
			if _, ok := supportedLangs[code]; ok {
				return code
			}
		}
	}
	return "es"
}

// normalizeLangCode convierte algo como "pt_BR.UTF-8" o "ca_ES" en "pt" o "ca".
func normalizeLangCode(v string) string {
	v = strings.ToLower(v)
	v = strings.SplitN(v, ".", 2)[0]
	v = strings.SplitN(v, "_", 2)[0]
	v = strings.SplitN(v, "-", 2)[0]
	return v
}

// loadLocale carga el catálogo de mensajes del idioma indicado desde los
// archivos embebidos en el binario.
func loadLocale(lang string) (map[string]string, error) {
	name, ok := supportedLangs[lang]
	if !ok {
		return nil, fmt.Errorf("idioma no soportado: %s", lang)
	}
	data, err := localesFS.ReadFile("locales/" + name)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// initI18n inicializa el catálogo de mensajes actual y el catálogo de
// respaldo en español, para cubrir claves que falten en otros idiomas.
func initI18n(explicitLang string) {
	currentLang = detectLang(explicitLang)

	es, err := loadLocale("es")
	if err != nil {
		// Si ni siquiera el catálogo español carga, seguimos con mapas
		// vacíos: T() caerá de vuelta a la propia clave.
		es = map[string]string{}
	}
	fallback = es

	if currentLang == "es" {
		catalog = es
		return
	}

	m, err := loadLocale(currentLang)
	if err != nil {
		catalog = es
		currentLang = "es"
		return
	}
	catalog = m
}

// T traduce una clave al idioma actual, aplicando fmt.Sprintf con los
// argumentos dados. Si la clave no existe en el idioma activo, recurre al
// catálogo español; si tampoco existe ahí, devuelve la clave tal cual para
// facilitar la depuración.
func T(key string, args ...interface{}) string {
	msg, ok := catalog[key]
	if !ok {
		msg, ok = fallback[key]
		if !ok {
			msg = key
		}
	}
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}
