package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/netinfo"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/tunnelproto"
)

func netinfoBest() (string, bool) {
	if a, ok := netinfo.BestAdvertise(); ok {
		return a.IP, true
	}
	return "", false
}

type tunnel struct {
	target  int
	local   int
	conn    net.Conn
	br      *bufio.Reader
	writeMu sync.Mutex
	mu      sync.Mutex
	streams map[uint32]net.Conn
	done    chan struct{}
	once    sync.Once
}

func (t *tunnel) close() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		close(t.done)
		t.conn.Close()
		t.mu.Lock()
		for id, c := range t.streams {
			c.Close()
			delete(t.streams, id)
		}
		t.mu.Unlock()
	})
}

func (a *Agent) openTunnel(ctx context.Context, args map[string]string, data map[string]string) error {
	target, _ := strconv.Atoi(args["target_port"])
	if target == 0 {
		return errs.New(errs.Usage, "tunnel.open requires target_port")
	}
	a.mu.Lock()
	if t, ok := a.tunnels[target]; ok {
		data["endpoint"] = "127.0.0.1:" + strconv.Itoa(t.local)
		data["port"] = strconv.Itoa(t.local)
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	host := hostOf(a.client.BaseURL)
	port := a.clientPort() + 2
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		return errs.Wrap(errs.Network, err, "cannot open reverse tunnel to %s:%d", host, port).
			WithHint("the coordinator must expose its API and tunnel port on the same host")
	}
	hs := map[string]any{
		"v": 1, "mux": true, "node_id": a.NodeID(), "token": a.cfg.Cluster.Token, "target": target,
	}
	raw, _ := json.Marshal(hs)
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		conn.Close()
		return errs.Wrap(errs.Network, err, "cannot send tunnel handshake")
	}
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	br := bufio.NewReader(conn)
	line, err := br.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return errs.Wrap(errs.Network, err, "tunnel handshake failed")
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Port  int    `json:"port"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil || !resp.OK || resp.Port == 0 {
		conn.Close()
		msg := resp.Error
		if msg == "" {
			msg = "invalid tunnel response"
		}
		return errs.New(errs.Network, "coordinator refused tunnel: %s", msg)
	}
	_ = conn.SetReadDeadline(time.Time{})
	t := &tunnel{
		target: target, local: resp.Port, conn: conn, br: br,
		streams: map[uint32]net.Conn{}, done: make(chan struct{}),
	}
	a.mu.Lock()
	a.tunnels[target] = t
	a.mu.Unlock()
	go t.readLoop()
	go func() {
		<-t.done
		a.mu.Lock()
		if cur := a.tunnels[target]; cur == t {
			delete(a.tunnels, target)
		}
		a.mu.Unlock()
	}()
	data["endpoint"] = "127.0.0.1:" + strconv.Itoa(resp.Port)
	data["port"] = strconv.Itoa(resp.Port)
	return nil
}

func (t *tunnel) readLoop() {
	defer t.close()
	for {
		f, err := tunnelproto.ReadFrame(t.br)
		if err != nil {
			return
		}
		switch f.Type {
		case tunnelproto.Open:
			t.openStream(f.Stream)
		case tunnelproto.Data:
			t.mu.Lock()
			up := t.streams[f.Stream]
			t.mu.Unlock()
			if up != nil {
				if _, err := up.Write(f.Data); err != nil {
					t.dropStream(f.Stream)
				}
			}
		case tunnelproto.Close:
			t.dropStream(f.Stream)
		}
	}
}

func (t *tunnel) openStream(id uint32) {
	up, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(t.target)), 3*time.Second)
	if err != nil {
		_ = tunnelproto.WriteFrame(t.conn, &t.writeMu, tunnelproto.Frame{Type: tunnelproto.Close, Stream: id})
		return
	}
	t.mu.Lock()
	t.streams[id] = up
	t.mu.Unlock()
	go t.pumpUpstream(id, up)
}

func (t *tunnel) pumpUpstream(id uint32, up net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := up.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			if werr := tunnelproto.WriteFrame(t.conn, &t.writeMu, tunnelproto.Frame{
				Type: tunnelproto.Data, Stream: id, Data: chunk,
			}); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	_ = tunnelproto.WriteFrame(t.conn, &t.writeMu, tunnelproto.Frame{Type: tunnelproto.Close, Stream: id})
	t.dropStream(id)
}

func (t *tunnel) dropStream(id uint32) {
	t.mu.Lock()
	up := t.streams[id]
	delete(t.streams, id)
	t.mu.Unlock()
	if up != nil {
		up.Close()
	}
}

func (a *Agent) closeTunnelArg(args map[string]string) {
	target, _ := strconv.Atoi(args["target_port"])
	a.mu.Lock()
	t, ok := a.tunnels[target]
	if ok {
		delete(a.tunnels, target)
	}
	a.mu.Unlock()
	if ok {
		t.close()
	}
}

func (a *Agent) clientPort() int {
	u := a.client.BaseURL
	u = strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	if _, p, err := net.SplitHostPort(u); err == nil {
		n, _ := strconv.Atoi(p)
		return n
	}
	return 47800
}
