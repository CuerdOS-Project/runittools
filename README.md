# runitctl y runit-journal

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
