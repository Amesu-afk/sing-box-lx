package v2rayxhttp

import (
	"sync"
	"time"
)

// poolConnections owns sockets across slot replacements. A generation change
// rejects sockets from dials that finish after an explicit network reset.
type poolConnections struct {
	mu         sync.Mutex
	generation uint64
	active     map[*trackedPoolConn]struct{}
}

func (p *poolConnections) currentGeneration() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.generation
}

func (p *poolConnections) register(conn *trackedPoolConn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if conn.owner.generation != p.generation {
		return false
	}
	if p.active == nil {
		p.active = make(map[*trackedPoolConn]struct{})
	}
	p.active[conn] = struct{}{}
	return true
}

func (p *poolConnections) forget(conn *trackedPoolConn) {
	p.mu.Lock()
	delete(p.active, conn)
	p.mu.Unlock()
}

func (p *poolConnections) snapshot(reset bool) []*trackedPoolConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	connections := make([]*trackedPoolConn, 0, len(p.active))
	for conn := range p.active {
		connections = append(connections, conn)
	}
	if reset {
		p.generation++
		p.active = nil
	}
	return connections
}

// reapRetired preserves receiving streams and cold sibling handshakes. Only
// members removed from rotation, or the member that actually timed out, qualify.
func (p *poolConnections) reapRetired(failed *poolTransport) int {
	closed := 0
	for _, conn := range p.snapshot(false) {
		if conn.owner != failed && !conn.owner.retired.Load() {
			continue
		}
		lastRead := conn.lastRead.Load()
		if lastRead != 0 && time.Since(time.Unix(0, lastRead)) < handshakeTimeout {
			continue
		}
		if lastRead == 0 && conn.owner != failed && time.Since(conn.createdAt) < freshHandshakeTimeout {
			continue
		}
		_ = conn.Close()
		closed++
	}
	return closed
}
