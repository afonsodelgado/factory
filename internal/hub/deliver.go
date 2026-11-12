package hub

import (
	"github.com/afonsodelgado/factory/internal/host"
)

type Typist = host.Typist
type Target = host.Target

func Refusal(t Target) string { return host.Refusal(t) }
func Deliver(typist Typist, t Target, text string) (string, error) {
	return host.Deliver(typist, t, text)
}
func unqueueable(t Target) string { return host.Unqueueable(t) }
