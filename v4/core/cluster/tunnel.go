package cluster

import (
	"bufio"
	"encoding/json"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/tunnelproto"
)

// tunnelHandshake is the first line on a reverse tunnel.
type tunnelHandshake struct {
	Version int    `json:"v"`
	Mux     bool   `json:"mux"`
	NodeID  string `json:"node_id"`
	Token   string `json:"token"`
	Target  int    `json:"target"`
}

// AllowTunnel authorises one node to expose one RPC port through the
// coordinator's reverse tunnel. It is called only after the planner assigns
// the port and before the agent is asked to open the tunnel.
func (c *Coordinator) AllowTunnel(nodeID string, port int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tunnelAllowed == nil {
		c.tunnelAllowed = map[string]map[int]bool{}
	}
	if c.tunnelAllowed[nodeID] == nil {
		c.tunnelAllowed[nodeID] = map[int]bool{}
	}
	c.tunnelAllowed[nodeID][port] = true
}

// TunnelPort returns the reverse-tunnel listen port.
func (c *Coordinator) TunnelPort() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tunnelPort
}

func (c *Coordinator) startTunnelServer() error {
	port := c.Info().Port + 2
	if port <= 2 {
		return errs.New(errs.Network, "coordinator has no bound port for tunnels")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(c.cfg.Cluster.Bind, strconv.Itoa(port)))
	if err != nil {
		return errs.Wrap(errs.Network, err, "cannot listen for reverse tunnels on %d", port)
	}
	c.mu.Lock()
	c.tunnelLn = ln
	c.tunnelPort = port
	c.mu.Unlock()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go c.handleTunnel(conn)
		}
	}()
	return nil
}

func (c *Coordinator) handleTunnel(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	var hs tunnelHandshake
	if err := json.Unmarshal(line, &hs); err != nil || hs.Version != 1 || !hs.Mux {
		writeTunnelErr(conn, "invalid handshake")
		return
	}
	if !TokenEqual(hs.Token, c.cfg.Cluster.Token) {
		writeTunnelErr(conn, "unauthorized")
		return
	}
	c.mu.Lock()
	_, member := c.members[hs.NodeID]
	allowed := c.tunnelAllowed[hs.NodeID][hs.Target]
	c.mu.Unlock()
	if !member {
		writeTunnelErr(conn, "unknown node")
		return
	}
	if !allowed {
		writeTunnelErr(conn, "target port is not assigned to this node")
		return
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		writeTunnelErr(conn, "cannot allocate local port")
		return
	}
	defer ln.Close()
	localPort := ln.Addr().(*net.TCPAddr).Port
	resp, _ := json.Marshal(map[string]any{"ok": true, "port": localPort})
	if _, err := conn.Write(append(resp, '\n')); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})

	m := &serverMux{conn: conn, br: br, streams: map[uint32]net.Conn{}, done: make(chan struct{})}
	go m.readLoop()
	for {
		local, err := ln.Accept()
		if err != nil {
			m.closeAll()
			return
		}
		m.add(local)
	}
}

type serverMux struct {
	conn    net.Conn
	br      *bufio.Reader
	writeMu sync.Mutex
	mu      sync.Mutex
	streams map[uint32]net.Conn
	next    uint32
	done    chan struct{}
	once    sync.Once
}

func (m *serverMux) add(local net.Conn) {
	id := atomic.AddUint32(&m.next, 1)
	m.mu.Lock()
	m.streams[id] = local
	m.mu.Unlock()
	if err := tunnelproto.WriteFrame(m.conn, &m.writeMu, tunnelproto.Frame{Type: tunnelproto.Open, Stream: id}); err != nil {
		m.drop(id)
		return
	}
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := local.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				if werr := tunnelproto.WriteFrame(m.conn, &m.writeMu, tunnelproto.Frame{
					Type: tunnelproto.Data, Stream: id, Data: chunk,
				}); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		_ = tunnelproto.WriteFrame(m.conn, &m.writeMu, tunnelproto.Frame{Type: tunnelproto.Close, Stream: id})
		m.drop(id)
	}()
}

func (m *serverMux) readLoop() {
	defer m.closeAll()
	for {
		f, err := tunnelproto.ReadFrame(m.br)
		if err != nil {
			return
		}
		m.mu.Lock()
		local := m.streams[f.Stream]
		m.mu.Unlock()
		if local == nil {
			continue
		}
		switch f.Type {
		case tunnelproto.Data:
			if _, err := local.Write(f.Data); err != nil {
				m.drop(f.Stream)
			}
		case tunnelproto.Close:
			m.drop(f.Stream)
		}
	}
}

func (m *serverMux) drop(id uint32) {
	m.mu.Lock()
	local := m.streams[id]
	delete(m.streams, id)
	m.mu.Unlock()
	if local != nil {
		local.Close()
	}
}

func (m *serverMux) closeAll() {
	m.once.Do(func() {
		close(m.done)
		m.conn.Close()
		m.mu.Lock()
		for id, local := range m.streams {
			local.Close()
			delete(m.streams, id)
		}
		m.mu.Unlock()
	})
}

func writeTunnelErr(conn net.Conn, msg string) {
	b, _ := json.Marshal(map[string]any{"ok": false, "error": msg})
	_, _ = conn.Write(append(b, '\n'))
}
