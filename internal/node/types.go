package node

import (
	"time"

	"github.com/afonsodelgado/factory/internal/host"
)

type Snapshot struct {
	Revision   int64               `json:"revision"`
	ServerTime time.Time           `json:"server_time"`
	Factories  []host.LocalFactory `json:"factories"`
}

type Me struct {
	Caller     string    `json:"caller"`
	Version    string    `json:"version"`
	ServerTime time.Time `json:"server_time"`
}

type ActionBody struct {
	Operation   string    `json:"operation"`
	Incarnation string    `json:"incarnation"`
	Action      string    `json:"action"`
	Item        string    `json:"item,omitempty"`
	Since       time.Time `json:"since,omitzero"`
	Answers     []string  `json:"answers,omitempty"`
	Text        string    `json:"text,omitempty"`
	Option      string    `json:"option,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
}

func (b ActionBody) Request(caller, factory string) host.ActionRequest {
	return host.ActionRequest{
		Operation: b.Operation, Device: caller, Factory: factory, Incarnation: b.Incarnation, Action: b.Action,
		Item: b.Item, Since: b.Since, Answers: b.Answers, Text: b.Text, Option: b.Option, Fingerprint: b.Fingerprint,
	}
}
