package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ctxKey string

const userCtxKey ctxKey = "user"

func userFromCtx(r *http.Request) *User {
	v := r.Context().Value(userCtxKey)
	if v == nil {
		return nil
	}
	if u, ok := v.(*User); ok {
		return u
	}
	return nil
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var token string
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token = strings.TrimPrefix(h, "Bearer ")
		}
		if token == "" {
			if c, err := r.Cookie("access_token"); err == nil {
				token = c.Value
			}
		}
		if token == "" {
			writeError(w, http.StatusUnauthorized, "Authentication required")
			return
		}
		claims, err := s.signer.parseAccess(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "Invalid or expired token")
			return
		}
		user, err := s.store.GetUserByID(claims.Sub)
		if err != nil {
			if errors.Is(err, errNotFound) {
				writeError(w, http.StatusUnauthorized, "User not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		if !user.IsActive {
			writeError(w, http.StatusForbidden, "Account disabled")
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireAdminForMutation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			user := userFromCtx(r)
			if user == nil || user.Role != "admin" {
				writeError(w, http.StatusForbidden, "Admin role required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireVersionHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := r.Header.Get("X-API-Version")
		if v == "" {
			writeError(w, http.StatusBadRequest, "API version header required")
			return
		}
		if v != "1" {
			writeError(w, http.StatusBadRequest, "Unsupported API version")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie("csrf_token")
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusForbidden, "CSRF token required")
			return
		}
		header := r.Header.Get("X-CSRF-Token")
		if header == "" || header != cookie.Value {
			writeError(w, http.StatusForbidden, "Invalid CSRF token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type rateLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{hits: make(map[string][]time.Time), limit: limit, window: window}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	hits := rl.hits[key]
	i := 0
	for ; i < len(hits); i++ {
		if hits[i].After(cutoff) {
			break
		}
	}
	hits = hits[i:]
	if len(hits) >= rl.limit {
		rl.hits[key] = hits
		return false
	}
	hits = append(hits, now)
	rl.hits[key] = hits
	return true
}

func clientIP(r *http.Request) string {
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		if i := strings.Index(x, ","); i >= 0 {
			return strings.TrimSpace(x[:i])
		}
		return strings.TrimSpace(x)
	}
	if x := r.Header.Get("X-Real-IP"); x != "" {
		return x
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host
}

func (s *Server) authRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authLimiter.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "Too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) apiRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := clientIP(r)
		if u := userFromCtx(r); u != nil {
			key = "u:" + u.ID
		}
		if !s.apiLimiter.allow(key) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "Too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(s int) {
	sw.status = s
	sw.ResponseWriter.WriteHeader(s)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if sw.status == 0 {
		sw.status = http.StatusOK
	}
	return sw.ResponseWriter.Write(b)
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start))
	})
}

func chainMiddleware(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
