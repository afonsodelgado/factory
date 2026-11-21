package node

import (
	"slices"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/phone"
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
	User        string              `json:"user,omitempty"`
	Can         []string            `json:"can,omitempty"`
}

const (
	CanStream         = "stream"
	CanHome           = "home"
	CanNeeds          = "needs"
	CanWatchComments  = "watch_comments"
	CanToggleComments = "toggle_comments"
)

var Capabilities = []string{CanStream, CanHome, CanNeeds, CanWatchComments, CanToggleComments}

func (s Snapshot) Supports(capability string) bool { return slices.Contains(s.Can, capability) }

type Me struct {
	Caller     string       `json:"caller"`
	Node       string       `json:"node"`
	Version    string       `json:"version"`
	ServerTime time.Time    `json:"server_time"`
	Awake      *AwakeStatus `json:"awake,omitempty"`
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
	Generation  int64     `json:"generation,omitempty"`
	Source      string    `json:"source,omitempty"`
}

func (b ActionBody) Request(caller, factory string) host.ActionRequest {
	return host.ActionRequest{
		Operation: b.Operation, Device: caller, Factory: factory, Incarnation: b.Incarnation, Action: b.Action,
		Item: b.Item, Since: b.Since, Answers: b.Answers, Text: b.Text, Option: b.Option, Fingerprint: b.Fingerprint, Generation: b.Generation, Source: b.Source,
	}
}

type NodeRequest struct {
	Operation  string `json:"operation,omitempty"`
	Name       string `json:"name"`
	ReplaceKey bool   `json:"replace_push_key,omitempty"`
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
	Join       string        `json:"join"`
	Challenge  string        `json:"challenge"`
	Ledger     Ledger        `json:"ledger"`
	Push       *phone.Shared `json:"push,omitempty"`
	SharedKey  string        `json:"shared_key,omitempty"`
	ReplaceKey bool          `json:"replace_push_key,omitempty"`
}

type Joined struct {
	Node  string          `json:"node"`
	Clock Clock           `json:"clock,omitempty"`
	Push  *phone.KeyState `json:"push,omitempty"`
}

type JoinRequest struct {
	Operation string `json:"operation"`
	Name      string `json:"name"`
}
