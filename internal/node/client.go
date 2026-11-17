package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

type Client struct {
	HTTP *http.Client
	Base string
}

type StatusError struct {
	Status  int
	Message string
}

func (e StatusError) Error() string { return fmt.Sprintf("%d: %s", e.Status, e.Message) }

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
