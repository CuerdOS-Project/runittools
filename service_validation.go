package main

import (
	"fmt"
	"regexp"
)

var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validateServiceName(name string) error {
	if name == "" {
		return fmt.Errorf("el nombre del servicio no puede estar vacío")
	}
	if len(name) > 255 || !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("nombre de servicio inválido: %q", name)
	}
	return nil
}
