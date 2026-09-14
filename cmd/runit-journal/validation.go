package main

import (
	"fmt"
	"regexp"
	"strings"
)

var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validateServiceName(name string) error {
	if name == "" || len(name) > 255 || !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("nombre de servicio inválido: %q", name)
	}
	return nil
}

func safeLogName(service string) string {
	name := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, service)
	if name == "" {
		return "service"
	}
	return name
}
