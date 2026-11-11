// SPDX-License-Identifier: GPL-3.0-or-later
//
// runitctl - administrador de servicios runit.
// Este programa es software libre: puedes redistribuirlo o modificarlo bajo
// los términos de la GNU General Public License publicada por la Free
// Software Foundation, ya sea la versión 3 o (a tu elección) cualquier
// versión posterior. Se distribuye sin ninguna garantía; consulta el
// archivo LICENSE.

package main

import "github.com/gdamore/tcell/v2"

// nopScreen cumple tcell.Screen solo en lo que usa followLoop (PostEvent).
type nopScreen struct{ tcell.Screen }

func (nopScreen) PostEvent(tcell.Event) error { return nil }
