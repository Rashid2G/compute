package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"

	"github.com/Rashid2G/compute/pkg/api"
)

var (
	ErrClientClosed = errors.New("client connection closed")
	ErrTimeout      = errors.New("request timed out waiting for server response")
)

type Client struct {
	conn       *websocket.Conn
	token      string
	wsURL      string
	hello      api.HelloMsg
	reqCounter uint64

	mu      sync.Mutex
	pending map[string]chan *api.Res
	closed  bool
	closeCh chan struct{}
}

// Dial connects to the Compute WebSocket API at wsURL using the provided Bearer token.
func Dial(ctx context.Context, wsURL, token string) (*Client, error) {
	header := make(http.Header)
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}

	opts := &websocket.DialOptions{
		Subprotocols: []string{api.Subprotocol},
		HTTPHeader:   header,
	}

	conn, _, err := websocket.Dial(ctx, wsURL, opts)
	if err != nil {
		return nil, fmt.Errorf("websocket dial failed: %w", err)
	}

	c := &Client{
		conn:    conn,
		token:   token,
		wsURL:   wsURL,
		pending: make(map[string]chan *api.Res),
		closeCh: make(chan struct{}),
	}

	// 1. Read Hello message
	msgType, data, err := conn.Read(ctx)
	if err != nil {
		conn.Close(websocket.StatusNormalClosure, "")
		return nil, fmt.Errorf("failed to read initial hello message: %w", err)
	}

	if msgType == websocket.MessageText {
		_ = json.Unmarshal(data, &c.hello)
	}

	// 2. Start background read pump
	go c.readPump()

	return c, nil
}

// Hello returns the initial server greeting information.
func (c *Client) Hello() api.HelloMsg {
	return c.hello
}

// Call executes an RPC operation over WebSocket and populates result.
func (c *Client) Call(ctx context.Context, op string, params interface{}, result interface{}) error {
	reqID := fmt.Sprintf("req-%d", atomic.AddUint64(&c.reqCounter, 1))

	var rawParams json.RawMessage
	if params != nil {
		pBytes, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("failed to marshal params: %w", err)
		}
		rawParams = pBytes
	}

	req := api.Req{
		Type:   api.TypeReq,
		ID:     reqID,
		Op:     op,
		Params: rawParams,
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	resCh := make(chan *api.Res, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClientClosed
	}
	c.pending[reqID] = resCh
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, reqID)
		c.mu.Unlock()
	}()

	if err := c.conn.Write(ctx, websocket.MessageText, reqBytes); err != nil {
		return fmt.Errorf("failed to write request: %w", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closeCh:
		return ErrClientClosed
	case res := <-resCh:
		if !res.OK {
			if res.Error != nil {
				return res.Error
			}
			return errors.New("request failed with unknown error")
		}

		if result != nil && len(res.Result) > 0 {
			if err := json.Unmarshal(res.Result, result); err != nil {
				return fmt.Errorf("failed to unmarshal result: %w", err)
			}
		}
		return nil
	}
}

// Ping sends a system.ping RPC.
func (c *Client) Ping(ctx context.Context) error {
	var res map[string]string
	return c.Call(ctx, "system.ping", nil, &res)
}

func (c *Client) readPump() {
	defer c.Close()

	for {
		msgType, data, err := c.conn.Read(context.Background())
		if err != nil {
			return
		}

		if msgType == websocket.MessageText {
			var base struct {
				Type string `json:"t"`
				ID   string `json:"id"`
			}
			if err := json.Unmarshal(data, &base); err != nil {
				continue
			}

			if base.Type == api.TypeRes {
				var res api.Res
				if err := json.Unmarshal(data, &res); err == nil {
					c.mu.Lock()
					ch, ok := c.pending[res.ID]
					c.mu.Unlock()
					if ok {
						select {
						case ch <- &res:
						default:
						}
					}
				}
			}
		}
	}
}

// Close gracefully closes the WebSocket connection.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.closeCh)
	c.mu.Unlock()

	return c.conn.Close(websocket.StatusNormalClosure, "client closed")
}
