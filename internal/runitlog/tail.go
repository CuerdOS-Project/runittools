package runitlog

import (
	"bytes"
	"io"
	"os"
	"strings"
	"time"
)

const (
	tailBlockSize = int64(64 * 1024)
	tailMaxScan   = int64(8 * 1024 * 1024) // tope para no leer logs gigantes enteros
	maxLineLen    = 1 << 20                // línea sin '\n' más larga que esto se emite tal cual
)

// ReadTailLines devuelve hasta n líneas finales de un archivo sin cargarlo
// completo: retrocede por bloques desde el final hasta reunir suficientes
// saltos de línea (o llegar al inicio / al tope de 8 MiB). Una primera línea
// cortada por el límite de lectura se descarta, nunca se devuelve truncada.
func ReadTailLines(path string, n int) ([]string, error) {
	if n < 1 {
		return []string{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()

	var chunks [][]byte // en orden inverso (último bloque primero)
	pos := size
	newlines := 0
	for pos > 0 && newlines <= n && size-pos < tailMaxScan {
		readSize := tailBlockSize
		if readSize > pos {
			readSize = pos
		}
		pos -= readSize
		buf := make([]byte, readSize)
		if _, err := f.ReadAt(buf, pos); err != nil && err != io.EOF {
			return nil, err
		}
		newlines += bytes.Count(buf, []byte{'\n'})
		chunks = append(chunks, buf)
	}

	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	data := make([]byte, 0, total)
	for i := len(chunks) - 1; i >= 0; i-- {
		data = append(data, chunks[i]...)
	}

	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return []string{}, nil
	}
	lines := strings.Split(text, "\n")
	if pos > 0 && len(lines) > 0 {
		lines = lines[1:] // primera línea posiblemente cortada a mitad
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// FollowFile sigue un archivo desde su final y llama a emit por cada línea
// COMPLETA nueva, hasta que stop se cierre. A diferencia de un simple
// "tail", tolera lo que hace svlogd con 'current':
//   - rotación (el archivo se renombra y aparece uno nuevo): termina de leer
//     el viejo y continúa con el nuevo desde su inicio;
//   - truncado: vuelve a leer desde el principio;
//   - archivo ausente temporalmente: espera a que reaparezca;
//   - escrituras parciales: no emite media línea, espera al '\n'.
func FollowFile(path string, poll time.Duration, stop <-chan struct{}, emit func(string)) {
	if poll <= 0 {
		poll = 300 * time.Millisecond
	}
	var (
		f       *os.File
		pending []byte
		buf     = make([]byte, 32*1024)
	)
	defer func() {
		if f != nil {
			f.Close()
		}
	}()

	if nf, err := os.Open(path); err == nil {
		f = nf
		_, _ = f.Seek(0, io.SeekEnd)
	}

	drain := func() {
		for {
			n, err := f.Read(buf)
			if n > 0 {
				pending = append(pending, buf[:n]...)
				for {
					i := bytes.IndexByte(pending, '\n')
					if i < 0 {
						break
					}
					emit(strings.TrimSuffix(string(pending[:i]), "\r"))
					pending = pending[i+1:]
				}
				if len(pending) > maxLineLen {
					emit(string(pending))
					pending = nil
				}
			}
			if err != nil || n == 0 {
				return
			}
		}
	}

	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		if f == nil {
			nf, err := os.Open(path)
			if err != nil {
				continue
			}
			f, pending = nf, nil // archivo nuevo: se lee entero
			drain()
			continue
		}

		drain()
		cur, err := os.Stat(path)
		if err != nil {
			continue // desapareció; seguimos sobre el descriptor viejo hasta que reaparezca
		}
		fi, err := f.Stat()
		if err != nil {
			continue
		}
		if !os.SameFile(cur, fi) {
			// rotado: el viejo ya se vació arriba; pasamos al nuevo
			f.Close()
			f, pending = nil, nil
			if nf, err := os.Open(path); err == nil {
				f = nf
				drain()
			}
			continue
		}
		if pos, err := f.Seek(0, io.SeekCurrent); err == nil && cur.Size() < pos {
			_, _ = f.Seek(0, io.SeekStart) // truncado
			pending = nil
			drain()
		}
	}
}
