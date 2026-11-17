package node

import (
	"time"

	"github.com/afonsodelgado/factory/internal/host"
)

const OperationHeader = "Factory-Operation"

type Snapshot struct {
	Node        string              `json:"node"`
	Version     string              `json:"version"`
	Protocol    int                 `json:"protocol"`
	MinProtocol int                 `json:"min_protocol"`
	Boot        string              `json:"boot"`
	Revision    int64               `json:"revision"`
	ServerTime  time.Time           `json:"server_time"`
	Factories   []host.LocalFactory `json:"factories"`
	Roster      *Roster             `json:"roster,omitempty"`
}

type Me struct {
	Caller     string    `json:"caller"`
	Node       string    `json:"node"`
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

type NodeRequest struct {
	Operation string `json:"operation,omitempty"`
	Name      string `json:"name"`
}

type RemoveRequest struct {
	Operation string `json:"operation"`
}

type Pull struct {
	From string `json:"from"`
}

type Kicked struct {
	Clock Clock `json:"clock"`
}

type Prepare struct {
	Join        string `json:"join"`
	Coordinator string `json:"coordinator"`
	Intent      string `json:"intent,omitempty"`
}

type Verify struct {
	Join      string `json:"join"`
	Node      string `json:"node"`
	Name      string `json:"name"`
	Challenge string `json:"challenge"`
}

type Commit struct {
	Join      string `json:"join"`
	Challenge string `json:"challenge"`
	Ledger    Ledger `json:"ledger"`
}

type Joined struct {
	Node  string `json:"node"`
	Clock Clock  `json:"clock,omitempty"`
}

type JoinRequest struct {
	Operation string `json:"operation"`
	Name      string `json:"name"`
}
