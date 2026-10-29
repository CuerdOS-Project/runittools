package main

import "github.com/gdamore/tcell/v2"

// nopScreen cumple tcell.Screen solo en lo que usa followLoop (PostEvent).
type nopScreen struct{ tcell.Screen }

func (nopScreen) PostEvent(tcell.Event) error { return nil }
