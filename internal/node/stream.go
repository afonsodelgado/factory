package node

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/host"
)

const StreamChunk = 16 << 10

var ErrNoStream = errors.New("this node's release cannot stream sessions")

type Resize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func (s Resize) winsize() *pty.Winsize { return &pty.Winsize{Cols: s.Cols, Rows: s.Rows} }

func DialStream(ctx context.Context, h *http.Client, machine, view string, size Resize, term string) (*websocket.Conn, error) {
	query := url.Values{"cols": {strconv.Itoa(int(size.Cols))}, "rows": {strconv.Itoa(int(size.Rows))}, "term": {term}}
	address := "https://" + machine + "/api/node/stream/" + url.PathEscape(view) + "?" + query.Encode()
	conn, resp, err := websocket.Dial(ctx, address, &websocket.DialOptions{HTTPClient: h})
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil, ErrNoStream
	}
	return conn, err
}

func (a *API) streamRoute(w http.ResponseWriter, r *http.Request, _ string) {
	view := r.PathValue("view")
	if _, err := uuid.Parse(view); err != nil {
		fail(w, http.StatusBadRequest, "a view id is a UUID from the node's attach preparation")
		return
	}
	attach := a.Tmux.Command("attach-session", "-t", "="+host.ViewSession(view))
	attach.Env = append(outsideTmux(os.Environ()), "TERM="+cmp.Or(r.URL.Query().Get("term"), "xterm-256color"))
	tty, err := pty.StartWithSize(attach, sizeOf(r.URL.Query()).winsize())
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer detach(attach, tty)
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	go sendOutput(r.Context(), conn, tty)
	receiveInput(r.Context(), conn, tty)
}

func outsideTmux(env []string) []string {
	return slices.DeleteFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TERM=") })
}

func sizeOf(q url.Values) Resize {
	cols, colsErr := strconv.ParseUint(q.Get("cols"), 10, 16)
	rows, rowsErr := strconv.ParseUint(q.Get("rows"), 10, 16)
	if colsErr != nil || rowsErr != nil || cols == 0 || rows == 0 {
		return Resize{Cols: 80, Rows: 24}
	}
	return Resize{Cols: uint16(cols), Rows: uint16(rows)}
}

func sendOutput(ctx context.Context, conn *websocket.Conn, tty *os.File) {
	buf := make([]byte, StreamChunk)
	for {
		n, err := tty.Read(buf)
		if err != nil {
			conn.Close(websocket.StatusNormalClosure, "the session ended")
			return
		}
		if conn.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
			return
		}
	}
}

func receiveInput(ctx context.Context, conn *websocket.Conn, tty *os.File) {
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if kind == websocket.MessageText {
			var size Resize
			if json.Unmarshal(data, &size) == nil && size.Cols > 0 && size.Rows > 0 {
				pty.Setsize(tty, size.winsize())
			}
			continue
		}
		if _, err := tty.Write(data); err != nil {
			return
		}
	}
}

func detach(attach *exec.Cmd, tty *os.File) {
	tty.Close()
	attach.Process.Kill()
	attach.Wait()
}
