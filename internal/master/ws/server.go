package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"

	"github.com/Rashid2G/on-demand-vms/internal/master/auth"
	"github.com/Rashid2G/on-demand-vms/internal/store/db"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
)

const (
	MaxMessageBytes = 1048576 // 1 MiB
	HeartbeatMS     = 20000   // 20s
	CloseRevoked    = 4401    // Credential revoked
)

type HandlerFunc func(ctx context.Context, conn *ClientConn, req *api.Req) (interface{}, *api.Error)

// Server manages WebSocket client connections and protocol dispatching.
type Server struct {
	keyMgr    *auth.KeyManager
	sessMgr   *auth.SessionManager
	publicURL string

	mu       sync.RWMutex
	handlers map[string]HandlerFunc
	conns    map[*ClientConn]struct{}
}

type ClientConn struct {
	wsConn    *websocket.Conn
	server    *Server
	user      *db.User
	authType  string
	keyID     string
	sessionID string
	sendCh    chan []byte
	closed    bool
	mu        sync.Mutex
}

func (c *ClientConn) User() *db.User {
	return c.user
}

func (c *ClientConn) KeyID() string {
	return c.keyID
}

func NewServer(publicURL string, keyMgr *auth.KeyManager, sessMgr *auth.SessionManager) *Server {
	s := &Server{
		publicURL: strings.TrimRight(publicURL, "/"),
		keyMgr:    keyMgr,
		sessMgr:   sessMgr,
		handlers:  make(map[string]HandlerFunc),
		conns:     make(map[*ClientConn]struct{}),
	}

	// Register default ping and capabilities handlers
	s.Register("system.ping", func(ctx context.Context, c *ClientConn, req *api.Req) (interface{}, *api.Error) {
		return map[string]string{"pong": "true"}, nil
	})
	s.Register("system.info", func(ctx context.Context, c *ClientConn, req *api.Req) (interface{}, *api.Error) {
		return map[string]string{"version": "2.0.0", "user": c.user.Username, "role": c.user.Role}, nil
	})

	return s
}

func (s *Server) Register(op string, h HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[op] = h
}

// ServeHTTP handles the /api/v1/ws handshake, authentication, and connection loop.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Authenticate via Bearer token or session cookie
	var user *db.User
	var keyID, sessionID, authType string

	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		clientIP := r.RemoteAddr
		u, keyRecord, err := s.keyMgr.AuthenticateToken(r.Context(), token, clientIP)
		if err != nil {
			http.Error(w, fmt.Sprintf("unauthorized: %v", err), http.StatusUnauthorized)
			return
		}
		user = u
		keyID = keyRecord.ID
		authType = "api_key"
	} else {
		// Session cookie auth
		cookie, err := r.Cookie("mf_session")
		if err != nil {
			http.Error(w, "missing authentication credentials", http.StatusUnauthorized)
			return
		}

		// CSWSH Protection: Origin header MUST match publicURL (SR-AUTH-05)
		origin := r.Header.Get("Origin")
		if s.publicURL != "" && origin != s.publicURL {
			http.Error(w, "cross-origin websocket connection forbidden", http.StatusForbidden)
			return
		}

		u, sessRecord, err := s.sessMgr.AuthenticateSession(r.Context(), cookie.Value)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid session: %v", err), http.StatusUnauthorized)
			return
		}
		user = u
		sessionID = sessRecord.ID
		authType = "cookie"
	}

	// Upgrade connection with compute.v1 subprotocol
	opts := &websocket.AcceptOptions{
		Subprotocols:   []string{api.Subprotocol},
		OriginPatterns: []string{"*"}, // Handled explicitly above
	}
	wsConn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}

	if wsConn.Subprotocol() != api.Subprotocol {
		_ = wsConn.Close(websocket.StatusPolicyViolation, "subprotocol required: "+api.Subprotocol)
		return
	}

	client := &ClientConn{
		wsConn:    wsConn,
		server:    s,
		user:      user,
		authType:  authType,
		keyID:     keyID,
		sessionID: sessionID,
		sendCh:    make(chan []byte, 128),
	}

	s.mu.Lock()
	s.conns[client] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.conns, client)
		s.mu.Unlock()
		client.Close()
	}()

	// Send HelloMsg
	hello := api.HelloMsg{
		Type:          api.TypeHello,
		ServerVersion: "2.0.0",
		Session: api.SessionInfo{
			UserID:   user.ID,
			Username: user.Username,
			Role:     user.Role,
			Auth:     authType,
			KeyID:    keyID,
		},
		Limits: api.ServerLimits{
			MaxMessageBytes: MaxMessageBytes,
			MaxInflight:     64,
		},
		HeartbeatMS: HeartbeatMS,
	}
	helloBytes, _ := json.Marshal(hello)
	if err := wsConn.Write(r.Context(), websocket.MessageText, helloBytes); err != nil {
		return
	}

	// Launch write pump
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go client.writePump(ctx)

	// Read pump
	wsConn.SetReadLimit(MaxMessageBytes)
	for {
		msgType, data, err := wsConn.Read(ctx)
		if err != nil {
			break
		}

		if msgType == websocket.MessageText {
			go s.handleTextMessage(ctx, client, data)
		} else if msgType == websocket.MessageBinary {
			// Binary multiplexed stream chunk
			go s.handleBinaryMessage(ctx, client, data)
		}
	}
}

func (s *Server) handleTextMessage(ctx context.Context, client *ClientConn, data []byte) {
	var req api.Req
	if err := json.Unmarshal(data, &req); err != nil {
		client.SendRes(&api.Res{
			Type:  api.TypeRes,
			OK:    false,
			Error: api.NewError(api.ErrCodeBadRequest, "malformed JSON request", ""),
		})
		return
	}

	s.mu.RLock()
	handler, ok := s.handlers[req.Op]
	s.mu.RUnlock()

	if !ok {
		client.SendRes(&api.Res{
			Type:    api.TypeRes,
			ID:      req.ID,
			OK:      false,
			Error:   api.NewError(api.ErrCodeBadRequest, fmt.Sprintf("unsupported operation: %s", req.Op), req.Trace),
			TraceID: req.Trace,
		})
		return
	}

	result, errRes := handler(ctx, client, &req)
	res := &api.Res{
		Type:    api.TypeRes,
		ID:      req.ID,
		TraceID: req.Trace,
	}
	if errRes != nil {
		res.OK = false
		res.Error = errRes
	} else {
		res.OK = true
		if result != nil {
			rawResult, _ := json.Marshal(result)
			res.Result = rawResult
		}
	}

	client.SendRes(res)
}

func (s *Server) handleBinaryMessage(ctx context.Context, client *ClientConn, data []byte) {
	// Binary frame decoding: | sid(4) | channel(1) | payload |
	_, err := DecodeBinaryFrame(data)
	if err != nil {
		return
	}
	// Binary stream handling
}

func (c *ClientConn) SendRes(res *api.Res) {
	data, err := json.Marshal(res)
	if err != nil {
		return
	}
	c.SendRaw(data)
}

func (c *ClientConn) SendRaw(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.sendCh <- data:
	default:
		// Queue full, drop or close
	}
}

func (c *ClientConn) writePump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-c.sendCh:
			if !ok {
				return
			}
			if err := c.wsConn.Write(ctx, websocket.MessageText, msg); err != nil {
				return
			}
		}
	}
}

func (c *ClientConn) CloseWithReason(code websocket.StatusCode, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	_ = c.wsConn.Close(code, reason)
	close(c.sendCh)
}

func (c *ClientConn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	_ = c.wsConn.Close(websocket.StatusNormalClosure, "")
	close(c.sendCh)
}

// RevokeCredential disconnects any active connections using keyID or sessionID within <= 5s.
func (s *Server) RevokeCredential(credentialID string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for conn := range s.conns {
		if conn.keyID == credentialID || conn.sessionID == credentialID {
			bye := api.ByeMsg{
				Type:   api.TypeBye,
				Code:   CloseRevoked,
				Reason: "credential revoked",
			}
			byeBytes, _ := json.Marshal(bye)
			_ = conn.wsConn.Write(context.Background(), websocket.MessageText, byeBytes)
			conn.CloseWithReason(websocket.StatusCode(CloseRevoked), "credential revoked")
		}
	}
}
