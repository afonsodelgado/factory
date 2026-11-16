package host

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/remote"
)

func (m *Runtime) Run(ctx context.Context, events <-chan Event) error {
	if err := m.Recover(); err != nil {
		return err
	}
	m.Tick()
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			m.Tick()
		case e, ok := <-events:
			if !ok {
				return nil
			}
			if e.T == "hello" || e.T == "gone" {
				continue
			}
			m.Tick()
			e.Respond(m.Handle(e.Message))
		}
	}
}

func (m *Runtime) Handle(request Message) Message {
	if request.Op == "" {
		return m.handle(request)
	}
	return m.Execute(request.Op, request.T, request.Data, func() Message { return m.handle(request) })
}

func (m *Runtime) handle(request Message) Message {
	var value any
	var err error
	switch request.T {
	case "open":
		var r OpenRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.Open(r)
		}
	case "delete":
		var r DeleteRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			var steps []factory.RemovalStep
			steps, err = m.Delete(r)
			value = factory.Report(steps)
		}
	case "wake", "pause":
		var r struct{ Key string }
		if err = json.Unmarshal(request.Data, &r); err == nil {
			if request.T == "wake" {
				err = m.Wake(r.Key)
			} else {
				err = m.Pause(r.Key)
			}
		}
	case "archive":
		var r ArchiveRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			err = m.Archive(r)
		}
	case "unarchive":
		var r struct{ Key string }
		if err = json.Unmarshal(request.Data, &r); err == nil {
			if it, ok := m.snap.Find(r.Key); ok {
				err = m.Unarchive(it)
			}
		}
	case "stage":
		var r StageRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.Stage(r)
		}
	case "review":
		var r ReviewRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.Review(r)
		}
	case "repo":
		var r RepoRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			m.Register(r)
		}
	case "create":
		var r CreateRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.Create(r)
		}
	case "wake-all":
		err = m.WakeAll()
	case "removal":
		var r DeleteRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.Removal(r)
		}
	case "resolve":
		var r ResolveRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.Resolve(r)
		}
	case "attach":
		var r AttachRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.Attach(r)
		}
	case "queue-answer":
		var r QueuedAnswer
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.QueueAnswerFor(r)
		}
	case "put-claim":
		var a Answer
		if err = json.Unmarshal(request.Data, &a); err == nil {
			m.PutClaim(a)
		}
	case "drop-claim":
		var key string
		if err = json.Unmarshal(request.Data, &key); err == nil {
			m.DropClaim(key)
		}
	case "forget-claims":
		var prefix string
		if err = json.Unmarshal(request.Data, &prefix); err == nil {
			m.ForgetClaims(func(key string) bool { return strings.HasPrefix(key, prefix) })
		}
	case "record-sent":
		var r SentRecord
		if err = json.Unmarshal(request.Data, &r); err == nil {
			m.RecordSent(r.Answer, r.At)
		}
	case "link":
		var r remote.LinkRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.Link(r)
		}
	case "opened":
		var ids []string
		if err = json.Unmarshal(request.Data, &ids); err == nil {
			err = m.Opened(ids)
		}
	case "events":
		var cursor int64
		if err = json.Unmarshal(request.Data, &cursor); err == nil {
			value = m.Events(cursor)
		}
	case "push":
		var settings remote.PushSettings
		if err = json.Unmarshal(request.Data, &settings); err == nil {
			value, err = m.configurePush(settings)
		}
	case "push-test":
		err = m.testPush()
	case "qa-claim":
		var c remote.QAClaim
		if err = json.Unmarshal(request.Data, &c); err == nil {
			value, err = m.ClaimQA(c)
		}
	case "qa-return":
		var r remote.QAReturn
		if err = json.Unmarshal(request.Data, &r); err == nil {
			err = m.ReturnQA(r)
		}
	case "snapshot":
		value = m.Presentation()
	case "view":
		value = m.View()
	case "answer":
		var r AnswerRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.TypeAnswer(r)
		}
	case "local":
		value = m.Local()
	case "act":
		var r ActionRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.act(request.Op, r)
		}
	case "preview":
		var r ActionRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.Preview(r)
		}
	case "receipt":
		var r ReceiptRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.LookupReceipt(r)
		}
	default:
		return Message{T: "error", Error: "unknown host action: " + request.T}
	}
	data, marshalErr := json.Marshal(value)
	if err == nil {
		err = marshalErr
	}
	if err != nil {
		return Message{T: "error", Error: err.Error(), Data: data}
	}
	return Message{T: "done", Data: data}
}
