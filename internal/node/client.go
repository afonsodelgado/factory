package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
)

const (
	answerWait  = 2 * time.Second
	connectWait = 5 * time.Second
	streamIdle  = 2*heartbeat + 5*time.Second
)

var errStalled = errors.New("the node's event stream went quiet")

type Client struct {
	HTTP *http.Client
	Base string
}

type StatusError struct {
	Status  int
	Message string
}

func (e StatusError) Error() string { return e.Message }

func Unix(socket string) *Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}
	return &Client{HTTP: &http.Client{Transport: &http.Transport{DialContext: dial}}, Base: "http://node"}
}

func (c *Client) Do(ctx context.Context, method, path string, body, into any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		var failure struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &failure)
		if into != nil {
			json.Unmarshal(data, into)
		}
		return StatusError{Status: resp.StatusCode, Message: failure.Error}
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(data, into)
}

func (c *Client) Call(ctx context.Context, m host.Message) (host.Message, error) {
	data := m.Data
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/api/commands/"+url.PathEscape(m.T), bytes.NewReader(data))
	if err != nil {
		return host.Message{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.Op != "" {
		req.Header.Set(OperationHeader, m.Op)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return host.Message{}, ErrUnavailable
	}
	defer resp.Body.Close()
	var answer host.Message
	switch resp.StatusCode {
	case http.StatusOK, http.StatusConflict, http.StatusUnprocessableEntity:
		if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
			return host.Message{}, err
		}
		return answer, nil
	case http.StatusServiceUnavailable:
		return host.Message{}, ErrUnavailable
	}
	var failure struct {
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&failure)
	return host.Message{}, StatusError{Status: resp.StatusCode, Message: failure.Error}
}

func (c *Client) Request(ctx context.Context, kind, op string, payload, into any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	answer, err := c.Call(ctx, host.Message{T: kind, Op: op, Data: data})
	switch {
	case err != nil:
		return err
	case answer.T == "uncertain":
		return UncertainError{Message: answer.Error}
	case answer.T != "done":
		return errors.New(answer.Error)
	case into == nil:
		return nil
	}
	return json.Unmarshal(answer.Data, into)
}

type UncertainError struct{ Message string }

func (e UncertainError) Error() string {
	return e.Message + "; the outcome is unknown, so check before trying again"
}

func Answers(socket string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), answerWait)
	defer cancel()
	var me Me
	return Unix(socket).Do(ctx, http.MethodGet, "/api/me", nil, &me) == nil
}

func AttachCommand(ctx context.Context, c *Client, name string, r host.AttachRequest) ([]string, error) {
	var view string
	if err := c.Request(ctx, "attach", "", r, &view); err != nil {
		return nil, err
	}
	return []string{"ssh", "-t", "-o", "ClearAllForwardings=yes", name, "factory", "host", "attach", view}, nil
}

func (c *Client) Follow(ctx context.Context, snapshot func(Snapshot), beat func()) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stalled atomic.Bool
	idle := time.AfterFunc(connectWait, func() {
		stalled.Store(true)
		cancel()
	})
	defer idle.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/api/events", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&failure)
		return StatusError{Status: resp.StatusCode, Message: failure.Error}
	}
	idle.Reset(streamIdle)
	lines := bufio.NewScanner(resp.Body)
	lines.Buffer(make([]byte, 64*1024), 16<<20)
	kind := ""
	for lines.Scan() {
		idle.Reset(streamIdle)
		line := lines.Text()
		data, isData := strings.CutPrefix(line, "data: ")
		switch {
		case strings.HasPrefix(line, "event: "):
			kind = strings.TrimPrefix(line, "event: ")
		case isData && kind == "unavailable":
			return ErrUnavailable
		case isData && kind == "snapshot":
			var s Snapshot
			if json.Unmarshal([]byte(data), &s) == nil {
				snapshot(s)
			}
		case strings.HasPrefix(line, ":"):
			beat()
		}
	}
	if stalled.Load() {
		return errStalled
	}
	if err := lines.Err(); err != nil {
		return err
	}
	return errors.New("the node closed its event stream")
}
