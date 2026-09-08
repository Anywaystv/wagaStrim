// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package listen

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Guard bounds HTTP work independently of TCP connections (including HTTP/2).
// Only the socket address is trusted; client-supplied forwarding headers are not.
func Guard(next http.Handler) http.Handler {
	limits := &requestLimits{
		clients:   make(map[string]*clientLimit),
		global:    rate.NewLimiter(100, 200),
		lastSweep: time.Now(),
	}
	active := make(chan struct{}, 32)

	return http.HandlerFunc(func(wri http.ResponseWriter, req *http.Request) {
		wri.Header().Set("Cache-Control", "no-store")
		wri.Header().Set("Referrer-Policy", "no-referrer")
		host, _, err := net.SplitHostPort(req.RemoteAddr)
		if err != nil {
			http.Error(wri, "bad address", http.StatusBadRequest)

			return
		}
		if limits.allow(host) {
			select {
			case active <- struct{}{}:
				defer func() { <-active }()
				next.ServeHTTP(wri, req)

				return
			default:
			}
		}
		wri.Header().Set("Retry-After", "2")
		http.Error(wri, "too many requests", http.StatusTooManyRequests)
	})
}

type clientLimit struct {
	limiter *rate.Limiter
	seen    time.Time
}

type requestLimits struct {
	mu        sync.Mutex
	clients   map[string]*clientLimit
	global    *rate.Limiter
	lastSweep time.Time
}

func (l *requestLimits) allow(host string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.lastSweep) >= time.Minute {
		for key, value := range l.clients {
			if now.Sub(value.seen) >= time.Minute {
				delete(l.clients, key)
			}
		}
		l.lastSweep = now
	}
	entry := l.clients[host]
	if entry == nil && len(l.clients) < 1024 {
		entry = &clientLimit{limiter: rate.NewLimiter(10, 20)}
		l.clients[host] = entry
	}
	if entry == nil {
		return false
	}
	entry.seen = now

	return entry.limiter.Allow() && l.global.Allow()
}
