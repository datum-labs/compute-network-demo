package mesh

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	rttWindow     = 15
	outcomeWindow = 20
	// Enough history that a page polling every two seconds never misses an
	// exchange, with room to spare.
	exchangeWindow = 50
)

// Peer is an instance to exchange traffic with.
type Peer struct {
	Name string
	Addr netip.Addr
}

type peerState struct {
	peer        Peer
	rtts        []time.Duration
	outcomes    []bool
	attempts    int64
	messages    int64
	lastSuccess time.Time
	wire        *atomic.Int64
}

// Pinger sends a small request to every peer on an interval and records how
// long the round trip took. Requests reuse connections so the measurement
// reflects network latency rather than connection setup.
type Pinger struct {
	Port     int
	Interval time.Duration
	Timeout  time.Duration

	client *http.Client

	mu        sync.Mutex
	peers     map[string]*peerState
	exchanges []Exchange
	// wire counts bytes on the connections to each peer address, keyed by
	// host:port, so byte totals reflect what actually crossed the network.
	wire sync.Map
}

// NewPinger returns a Pinger that targets peers on port.
func NewPinger(port int, interval, timeout time.Duration) *Pinger {
	p := &Pinger{Port: port, Interval: interval, Timeout: timeout, peers: map[string]*peerState{}}
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &countingConn{Conn: conn, n: p.counter(addr)}, nil
		},
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
	}
	p.client = &http.Client{Transport: transport, Timeout: timeout}
	return p
}

func (p *Pinger) counter(addr string) *atomic.Int64 {
	v, _ := p.wire.LoadOrStore(addr, new(atomic.Int64))
	return v.(*atomic.Int64)
}

// SetPeers replaces the peer set, keeping history for peers that remain.
func (p *Pinger) SetPeers(peers []Peer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := make(map[string]*peerState, len(peers))
	for _, peer := range peers {
		if st, ok := p.peers[peer.Name]; ok && st.peer.Addr == peer.Addr {
			next[peer.Name] = st
			continue
		}
		next[peer.Name] = &peerState{peer: peer, wire: p.counter(p.hostPort(peer.Addr))}
	}
	p.peers = next
}

func (p *Pinger) hostPort(a netip.Addr) string {
	return net.JoinHostPort(a.String(), strconv.Itoa(p.Port))
}

// Run pings all peers every Interval until ctx is done.
func (p *Pinger) Run(ctx context.Context) {
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for {
		p.round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Pinger) round(ctx context.Context) {
	p.mu.Lock()
	targets := make([]Peer, 0, len(p.peers))
	for _, st := range p.peers {
		targets = append(targets, st.peer)
	}
	p.mu.Unlock()

	var wg sync.WaitGroup
	for _, peer := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rtt, ok := p.ping(ctx, peer)
			p.record(peer, rtt, ok, time.Now())
		}()
	}
	wg.Wait()
}

func (p *Pinger) ping(ctx context.Context, peer Peer) (time.Duration, bool) {
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+p.hostPort(peer.Addr)+"/mesh/ping", nil)
	if err != nil {
		return 0, false
	}
	start := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	rtt := time.Since(start)
	return rtt, resp.StatusCode == http.StatusOK
}

func (p *Pinger) record(peer Peer, rtt time.Duration, ok bool, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, exists := p.peers[peer.Name]
	if !exists || st.peer.Addr != peer.Addr {
		return
	}
	st.attempts++
	st.outcomes = appendWindow(st.outcomes, ok, outcomeWindow)
	p.exchanges = appendWindow(p.exchanges, Exchange{
		At:    now,
		To:    peer.Name,
		RTTMs: float64(rtt.Microseconds()) / 1000,
		OK:    ok,
	}, exchangeWindow)
	if ok {
		// A request and its reply.
		st.messages += 2
		st.lastSuccess = now
		st.rtts = appendWindow(st.rtts, rtt, rttWindow)
	}
}

func appendWindow[T any](s []T, v T, max int) []T {
	s = append(s, v)
	if len(s) > max {
		s = s[len(s)-max:]
	}
	return s
}

// Stats returns the current measurements for every peer, sorted by name.
func (p *Pinger) Stats() []PeerStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PeerStats, 0, len(p.peers))
	for _, st := range p.peers {
		out = append(out, st.stats())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (st *peerState) stats() PeerStats {
	s := PeerStats{
		Name:        st.peer.Name,
		Address:     st.peer.Addr.String(),
		Attempts:    st.attempts,
		Messages:    st.messages,
		Bytes:       st.wire.Load(),
		LastSuccess: st.lastSuccess,
		RTTMs:       medianMs(st.rtts),
	}
	if len(st.outcomes) > 0 {
		good := 0
		for _, o := range st.outcomes {
			if o {
				good++
			}
		}
		s.SuccessRate = float64(good) / float64(len(st.outcomes))
	}
	return s
}

func medianMs(d []time.Duration) float64 {
	if len(d) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), d...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	m := sorted[mid]
	if len(sorted)%2 == 0 {
		m = (sorted[mid-1] + sorted[mid]) / 2
	}
	return float64(m.Microseconds()) / 1000
}

type countingConn struct {
	net.Conn
	n *atomic.Int64
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.n.Add(int64(n))
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.n.Add(int64(n))
	return n, err
}

// Exchanges returns recent messages to peers, newest first.
func (p *Pinger) Exchanges(limit int) []Exchange {
	p.mu.Lock()
	defer p.mu.Unlock()
	if limit <= 0 || limit > len(p.exchanges) {
		limit = len(p.exchanges)
	}
	out := make([]Exchange, 0, limit)
	for i := len(p.exchanges) - 1; i >= len(p.exchanges)-limit; i-- {
		out = append(out, p.exchanges[i])
	}
	return out
}
