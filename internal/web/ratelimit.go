package web

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// rateLimiter allows each client a number of requests per fixed window, counted in memory.
// Counters reset when the app restarts, which is fine for stopping password guessing and
// runaway scripts.
type rateLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu        sync.Mutex
	clients   map[string]*clientWindow
	lastSweep time.Time
}

type clientWindow struct {
	start time.Time
	count int
}

func newRateLimiter(limit int, window time.Duration, now func() time.Time) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, now: now, clients: map[string]*clientWindow{}}
}

// count records one request from client and returns how many it made in the current window.
func (l *rateLimiter) count(client string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= l.window {
		for key, c := range l.clients {
			if now.Sub(c.start) >= l.window {
				delete(l.clients, key)
			}
		}
		l.lastSweep = now
	}
	c, ok := l.clients[client]
	if !ok || now.Sub(c.start) >= l.window {
		c = &clientWindow{start: now}
		l.clients[client] = c
	}
	c.count++
	return c.count
}

func (l *rateLimiter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		n := l.count(ip)
		if n <= l.limit {
			next.ServeHTTP(w, r)
			return
		}
		if n == l.limit+1 { // log once per window, not once per blocked request
			slog.Warn("rate limited", "ip", ip, "path", r.URL.Path, "limit", l.limit, "window", l.window)
		}
		w.Header().Set("Retry-After", strconv.Itoa(int(l.window.Seconds())))
		http.Error(w, "Muitas requisições. Aguarde um minuto.", http.StatusTooManyRequests)
	})
}

// clientIP is the address the request came from (the app has no proxy in front of it).
// IPv6 clients are grouped by /64, the block one home or phone usually gets, so rotating
// addresses inside it does not reset the limit.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if addr.Is6() {
		prefix, err := addr.Prefix(64)
		if err == nil {
			return prefix.String()
		}
	}
	return addr.String()
}
