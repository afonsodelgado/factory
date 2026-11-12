package hub

import "github.com/afonsodelgado/factory/internal/host"

type Message = host.Message
type Event = host.Event
type Conn = host.Conn
type Server = host.Server

func Listen(path string, deliver func(Event)) (*Server, error) { return host.Listen(path, deliver) }
