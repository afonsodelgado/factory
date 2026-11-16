package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

var testNow = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

const factoryKey = "/r/app:csv"

type typist struct {
	mu    sync.Mutex
	typed []string
}

func (t *typist) Type(pane, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.typed = append(t.typed, pane+": "+text)
	return nil
}

func (t *typist) all() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.typed)
}

func asking(t *testing.T, typed host.Typist) *host.Runtime {
	t.Helper()
	pane := tmux.Pane{ID: "%1", Command: "claude", Token: "sup"}
	question := state.Attention{ID: "q1", Kind: "question", Questions: []string{"Which region?"}, CreatedAt: state.Time{Time: testNow.Add(-2 * time.Minute)}}
	f := factory.Factory{Repo: "/r/app", Slug: "csv", State: &state.State{ID: "csv", Stage: "plan", Attention: []state.Attention{question}}}
	idle := state.HookEvent{Event: "Stop", At: testNow.Add(-time.Minute)}
	r := host.New(host.Runtime{Config: &state.Config{StateDir: t.TempDir()}, TM: tmux.Client{Socket: "factory-test-" + uuid.NewString(), ConfigFile: "/dev/null"}, Typist: typed, Now: func() time.Time { return testNow }})
	r.SetSnapshot(host.Snapshot{At: testNow, Panes: []tmux.Pane{pane}, Items: []host.Item{{Factory: f, Status: factory.NeedsYou, Hook: &state.HookFile{Main: &idle, Last: idle}, Pane: &pane}}})
	return r
}

func serveNode(t *testing.T, r *host.Runtime) (Queue, *Client) {
	t.Helper()
	queue := NewQueue()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- r.Run(ctx, queue.Events()) }()
	dir, err := os.MkdirTemp("/tmp", "fnode-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "node.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	api := &API{Commands: Commands{Runtime: queue}, Now: func() time.Time { return testNow }}
	server := &http.Server{Handler: api.Handler(func(*http.Request) (string, bool) { return "owner", true })}
	go server.Serve(listener)
	go api.Poll(ctx)
	t.Cleanup(func() {
		server.Close()
		cancel()
		if err := <-stopped; err != nil {
			t.Error(err)
		}
		os.RemoveAll(dir)
	})
	return queue, Unix(socket)
}

func answerQ1() ActionBody {
	return ActionBody{Operation: uuid.NewString(), Incarnation: factoryKey, Action: host.ActAnswer, Item: "q1", Since: testNow.Add(-2 * time.Minute), Answers: []string{"eu-west"}}
}

func TestTheHTTPDoorReachesTheSameRuntimeChecksAsTheSocket(t *testing.T) {
	ctx := context.Background()
	actions := "/api/factories/" + url.PathEscape(factoryKey) + "/actions"

	t.Run("an answer over HTTP types exactly what the same answer through the queue types", func(t *testing.T) {
		overHTTP, overQueue := &typist{}, &typist{}
		_, client := serveNode(t, asking(t, overHTTP))
		var receipt host.Receipt
		if err := client.Do(ctx, http.MethodPost, actions, answerQ1(), &receipt); err != nil || receipt.Status == host.StatusStale {
			t.Fatalf("the HTTP answer gave %+v, %v", receipt, err)
		}
		queue, _ := serveNode(t, asking(t, overQueue))
		body := answerQ1()
		data, _ := json.Marshal(body.Request("owner", factoryKey))
		if reply, err := queue.Call(ctx, host.Message{T: "act", Op: body.Operation, Data: data}); err != nil || reply.T != "done" {
			t.Fatalf("the queued answer gave %+v, %v", reply, err)
		}
		if len(overHTTP.all()) != 1 || !slices.Equal(overHTTP.all(), overQueue.all()) {
			t.Fatalf("HTTP typed %q and the queue typed %q; every client must reach the same text and checks", overHTTP.all(), overQueue.all())
		}
	})

	t.Run("the snapshot over HTTP is the runtime's own view of its factories", func(t *testing.T) {
		queue, client := serveNode(t, asking(t, &typist{}))
		reply, err := queue.Call(ctx, host.Message{T: "local"})
		var local host.Local
		if err != nil || json.Unmarshal(reply.Data, &local) != nil || len(local.Factories) != 1 {
			t.Fatalf("local gave %+v, %v", reply, err)
		}
		var snap Snapshot
		for deadline := time.Now().Add(5 * time.Second); client.Do(ctx, http.MethodGet, "/api/snapshot", nil, &snap) != nil; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the snapshot never became available over HTTP")
			}
		}
		want, _ := json.Marshal(local.Factories)
		got, _ := json.Marshal(snap.Factories)
		if !bytes.Equal(got, want) {
			t.Fatalf("HTTP snapshot %s; want the runtime's %s", got, want)
		}
	})

	t.Run("an action accepted before a crash answers 409 unknown and is never typed again", func(t *testing.T) {
		typed := &typist{}
		r := asking(t, typed)
		body := answerQ1()
		data, _ := json.Marshal(body.Request("owner", factoryKey))
		accepted, _ := json.Marshal(host.Operation{ID: body.Operation, Action: "act", Data: data, Status: "accepted"})
		if err := host.WriteFile(filepath.Join(r.Config.StateDir, "operations", body.Operation+".json"), string(accepted)); err != nil {
			t.Fatal(err)
		}
		_, client := serveNode(t, r)
		var uncertain struct {
			Status string `json:"status"`
		}
		var failure StatusError
		err := client.Do(ctx, http.MethodPost, actions, body, &uncertain)
		if !errors.As(err, &failure) || failure.Status != http.StatusConflict || uncertain.Status != host.StatusUnknown || len(typed.all()) != 0 {
			t.Fatalf("a retry of an interrupted answer gave %v, status %q, typed %q; want 409 unknown with nothing typed", err, uncertain.Status, typed.all())
		}
	})
}

func TestASupersededCheckNeverPostsItsResultAndNeverHoldsUpAnswers(t *testing.T) {
	queue := NewQueue()
	jobs := &Jobs{Queue: queue}
	posted := make(chan string, 4)
	go func() {
		for e := range queue.Events() {
			if e.Reply != nil {
				e.Respond(host.Message{T: "done"})
				continue
			}
			posted <- e.Text
		}
	}()
	release := make(chan struct{})
	jobs.Start(context.Background(), "ci", func(context.Context) host.Message {
		<-release
		return host.Message{T: "checked", Text: "stale"}
	})
	if reply, err := queue.Call(context.Background(), host.Message{T: "local"}); err != nil || reply.T != "done" {
		t.Fatalf("a running check held up an answer: %+v, %v", reply, err)
	}
	jobs.Start(context.Background(), "ci", func(context.Context) host.Message { return host.Message{T: "checked", Text: "fresh"} })
	close(release)
	select {
	case text := <-posted:
		if text != "fresh" {
			t.Fatalf("the queue received %q first; want only the newer check's result", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the newer check never posted its result")
	}
	select {
	case text := <-posted:
		t.Fatalf("the superseded check posted %q", text)
	case <-time.After(200 * time.Millisecond):
	}
}
