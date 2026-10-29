package runitlog

import (
	"strconv"
	"strings"
	"time"
)

// svlogd (con -t/-tt) y multilog anteponen a cada línea una marca TAI64N:
// '@' seguido de 16 dígitos hexadecimales (segundos TAI64) y, en la variante
// TAI64N, 8 dígitos hexadecimales más de nanosegundos.
const tai64Offset = int64(0x400000000000000a)

// DecodeTai64n sustituye el prefijo TAI64N de una línea por un timestamp
// legible en UTC. Si la línea no empieza con una marca válida, se devuelve
// sin cambios.
func DecodeTai64n(line string) string {
	if len(line) < 25 || line[0] != '@' {
		return line
	}
	secHex := line[1:17]
	if !isHex(secHex) {
		return line
	}
	rest := line[17:]
	var ns int64
	if len(rest) >= 8 && isHex(rest[:8]) {
		ns, _ = strconv.ParseInt(rest[:8], 16, 64)
		rest = rest[8:]
	}
	secRaw, err := strconv.ParseUint(secHex, 16, 64)
	if err != nil || int64(secRaw) < tai64Offset {
		return line
	}
	t := time.Unix(int64(secRaw)-tai64Offset, ns).UTC()
	rest = strings.TrimPrefix(rest, " ")
	return t.Format("2006-01-02 15:04:05.000000") + "  " + rest
}

func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

// FormatLine decodifica el prefijo TAI64N solo si decode es true.
func FormatLine(raw string, decode bool) string {
	if decode {
		return DecodeTai64n(raw)
	}
	return raw
}
