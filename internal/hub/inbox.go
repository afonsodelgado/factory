package hub

import "github.com/afonsodelgado/factory/internal/host"

type inboxMessage = host.InboxMessage

func (m *Model) queueInbox() { m.execution().QueueInbox() }
func inboxMessages(stateDir string, it Item, delivered []string) []inboxMessage {
	return host.InboxMessages(stateDir, it, delivered)
}
