package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

const (
	HolderSchema    = 1
	holderRefPrefix = "refs/factory/holders/"
)

type Holder struct {
	Schema      int    `json:"schema"`
	Incarnation string `json:"incarnation"`
	Node        string `json:"node"`
	Previous    string `json:"previous,omitempty"`
	Generation  int64  `json:"generation"`
	Checkpoint  string `json:"checkpoint"`
	Operation   string `json:"operation"`
}

func HolderRef(incarnation string) string { return holderRefPrefix + incarnation }

func (h Holder) Next(node, checkpoint, operation string) Holder {
	return Holder{Schema: HolderSchema, Incarnation: h.Incarnation, Node: node, Previous: h.Node, Generation: h.Generation + 1, Checkpoint: checkpoint, Operation: operation}
}

func (h Holder) Validate() error {
	var problems []error
	if h.Schema != HolderSchema {
		problems = append(problems, fmt.Errorf("holder schema %d is not %d; update factory on this node", h.Schema, HolderSchema))
	}
	for name, id := range map[string]string{"incarnation": h.Incarnation, "node": h.Node, "operation": h.Operation} {
		if uuid.Validate(id) != nil {
			problems = append(problems, fmt.Errorf("holder %s %q is not a UUID", name, id))
		}
	}
	if h.Previous != "" && uuid.Validate(h.Previous) != nil {
		problems = append(problems, fmt.Errorf("holder previous node %q is not a UUID", h.Previous))
	}
	if h.Generation < 1 {
		problems = append(problems, errors.New("holder generation starts at 1"))
	}
	if !isDigest(h.Checkpoint) {
		problems = append(problems, fmt.Errorf("holder checkpoint %q is not a sha256 digest", h.Checkpoint))
	}
	return errors.Join(problems...)
}

func (h Holder) Encode() ([]byte, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(h)
}

func DecodeHolder(data []byte) (Holder, error) {
	var h Holder
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&h); err != nil {
		return Holder{}, fmt.Errorf("the holder record is not one this node reads: %w", err)
	}
	return h, h.Validate()
}

func isDigest(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == 32
}
