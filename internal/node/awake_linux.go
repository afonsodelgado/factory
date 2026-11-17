package node

func NewAwake(bool) *Awake {
	return &Awake{Unsupported: "not applicable on Linux nodes"}
}
