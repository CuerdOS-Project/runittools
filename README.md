# runitctl y runit-journal (versión 1.0)

Este paquete contiene dos ejecutables con responsabilidades separadas. `runitctl` administra servicios y ofrece el comando clásico `runitctl log SERVICIO`. `runit-journal` es una TUI autónoma basada en `tcell`, especializada en buscar servicios, leer su logging y seguir nuevas líneas; no delega la pantalla en `runitctl`.

## Capturar logs con `-g`

La flag `-g` (`--grab` o `--save`) guarda el historial mostrado y, si se combina con `-f`, todas las líneas nuevas hasta que se presione `Ctrl-C` o el proceso reciba `SIGTERM`:

```sh
runitctl logs nginx -n 100 -g
runitctl logs nginx -f -g
```

Por defecto, los archivos se crean en:

```text
$XDG_STATE_HOME/runitctl/logs/
```

Si `XDG_STATE_HOME` no está definido, se utiliza `~/.local/state/runitctl/logs/`. Cada captura se llama `<servicio>-<AAAAMMDD-HHMMSS>.log`, se crea con permisos `0600` y la carpeta con permisos `0700`. Puede elegirse otra carpeta con:

```sh
runitctl logs nginx -f -g --log-dir "$HOME/logs/runit"
```

La salida conserva el formato que muestra el visor, incluidos los timestamps decodificados de `svlogd` cuando corresponda. La captura se sincroniza después de cada línea y se cierra correctamente al detener el seguimiento.

## Selector `runit-journal`

Sin argumentos, abre una TUI autónoma basada en `tcell` con los servicios disponibles y sus logs. La lista se navega con las flechas, `Enter` abre el visor, `/` activa el filtro incremental, `Home`/`End` saltan al inicio o al final, `PageUp`/`PageDown` desplazan por páginas y `Esc` o `q` salen:

```sh
runit-journal
```

También se puede indicar el servicio directamente, evitando el selector TUI:

```sh
runit-journal nginx
runit-journal nginx -n 200
```

Para una sesión que además se guarde en un archivo del usuario, `-g` conserva el historial inicial y las líneas nuevas dentro de la TUI:

```sh
runit-journal nginx -f -g --log-dir "$HOME/logs/runit"
```

## Más herramientas de runit

- Órdenes extra de `sv`: `once`, `term`, `kill`, `int`, `alarm`, `usr1`, `usr2`, `quit` y `check` (`runitctl term nginx`).
- `runitctl runlevel` muestra los niveles de arranque; `sudo runitctl runlevel single` cambia de nivel con `runsvchdir`.
- `runitctl doctor` revisa que existan `sv`, `runsv`, `runsvdir`, `runsvchdir`, `svlogd`, `chpst`, `utmpset`, `svlogtail` y `socklog`.
- La versión se ve con `-v`, `-V` o `--version`.

## Dependencias de logs en Void Linux

`svlogd` ya viene con runit. Para los logs de sistema (syslog) hace falta `socklog-void` (incluye `svlogtail`) y los servicios `socklog-unix` y `nanoklogd`. Un solo comando lo instala si falta, habilita los servicios y añade al usuario que invocó `sudo`/`doas` al grupo `socklog` para leer sin root (requiere volver a iniciar sesión). Es idempotente:

```sh
sudo runitctl setup-logs      # o: sudo make setup-logs
```

## Elevación de privilegios, TUI y `logs`

- Si una acción necesita root y estás en una terminal, `runitctl` pregunta «¿Reiniciar el comando con sudo? [S/n]» y se relanza con `sudo` (o `doas`). Sin terminal (scripts, cron) solo muestra el error.
- La TUI muestra el estado con color: verde `●` activo, rojo `○`/`✖` detenido o con fallo, amarillo `‖` pausado y `⊘` enmascarado, gris `○` deshabilitado.
- `logs`: `-n` indica cuántas líneas de historial mostrar y, con `-f`, se sigue desde ahí. `-i` no se combina con `-g`, `--log-dir` ni `-n`, y `--log-dir` exige `-g` (código de salida 2). Si no hay logging configurado en Void, se sugiere `sudo runitctl setup-logs`.

## Configuración por entorno

| Variable | Efecto | Por defecto |
| --- | --- | --- |
| `RUNITCTL_SV_DIR` | Directorio con las definiciones de servicio | `/etc/sv` |
| `RUNITCTL_SVDIR` | Directorio supervisado por `runsvdir` (se pasa a `sv` como `SVDIR`); si no existe se usa `$SVDIR` | `/etc/runit/runsvdir/default` |
| `RUNITCTL_SOCKLOG_DIR` | Directorio de socklog | `/var/log/socklog` |
| `RUNITCTL_LANG` | Idioma (`es`, `en`, `pt`, `ca`) | detectado de `LC_ALL`/`LC_MESSAGES`/`LANG`; inglés si no coincide |
| `RUNITCTL_NO_ELEVATE` / `RUNITCTL_AUTO_ELEVATE` | `1` desactiva la oferta de `sudo`/`doas`; `1` eleva sin preguntar | pregunta en terminales |
| `NO_COLOR` | Desactiva los colores ANSI (también se desactivan si la salida no es una terminal) | — |

`runitctl --version` y `runit-journal -version` muestran la versión inyectada en la compilación. Las llamadas a `sv` tienen un tiempo máximo de 10 s para que un `runsv` colgado no bloquee la CLI ni la TUI.

## Compilación e instalación

Se requieren Go 1.22 o posterior y las dependencias declaradas en `go.mod`:

```sh
make
sudo make install
```

El `Makefile` construye e instala ambos binarios: `runitctl` y `runit-journal`, junto con los catálogos de idioma. Para cambiar el prefijo:

```sh
make PREFIX="$HOME/.local"
make PREFIX="$HOME/.local" install
```

Las acciones que cambian el estado o la configuración (`start`, `stop`, `restart`,
`reload`, `pause`, `cont`, `enable`, `disable`, `mask`, `unmask` y `tui`) requieren
root. Las consultas y la lectura/captura de logs no requieren root por parte de
`runitctl`, aunque el usuario debe tener permisos de lectura sobre los archivos
correspondientes. Los nombres de servicio se validan y solo aceptan letras,
números, punto, guion y guion bajo; esto evita que una entrada pueda escapar de
los directorios de runit.

Para verificar el árbol antes de instalarlo:

```sh
make check
make race
```

El visor sigue detectando los backends existentes de runit: `svlogd`, `vlogger`, `runit-journal` en `/var/log/runit`, `socklog` y `journalctl` cuando están disponibles.
