package phone

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
)

var ErrUnavailable = node.ErrUnavailable

const ownerWait = 10 * time.Second

type Owner interface {
	Call(ctx context.Context, m host.Message) (host.Message, error)
}

type SocketOwner struct{ Socket string }

func (o SocketOwner) Call(ctx context.Context, m host.Message) (host.Message, error) {
	return Call(ctx, o.Socket, m)
}

func Call(ctx context.Context, socket string, m host.Message) (host.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, ownerWait)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return host.Message{}, ErrUnavailable
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(m); err != nil {
		return host.Message{}, ErrUnavailable
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), host.MaxMessage)
	for scanner.Scan() {
		var reply host.Message
		if json.Unmarshal(scanner.Bytes(), &reply) == nil && isReply(reply) {
			return reply, nil
		}
	}
	return host.Message{}, ErrUnavailable
}

func isReply(m host.Message) bool {
	return m.T == "done" || m.T == "error" || m.T == "uncertain"
}

func request(ctx context.Context, owner Owner, kind, op string, payload, into any) (host.Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return host.Message{}, err
	}
	reply, err := owner.Call(ctx, host.Message{T: kind, Op: op, Pane: "phone", Data: data})
	if err == nil && reply.T == "done" && into != nil {
		err = json.Unmarshal(reply.Data, into)
	}
	return reply, err
}
