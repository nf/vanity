package main

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"
)

func newHTTPTracker() *httpTracker {
	return &httpTracker{reqs: make(map[*httpRequest]struct{})}
}

type httpTracker struct {
	mu   sync.RWMutex
	reqs map[*httpRequest]struct{}
}

func (t *httpTracker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t.mu.RLock()
	reqs := make([]*httpRequest, 0, len(t.reqs))
	for req := range t.reqs {
		reqs = append(reqs, req)
	}
	t.mu.RUnlock()

	sort.Slice(reqs, func(i, j int) bool {
		return reqs[i].start.Before(reqs[j].start)
	})

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	tw := tabwriter.NewWriter(w, 0, 2, 1, ' ', 0)
	now := time.Now()
	for _, req := range reqs {
		fmt.Fprintf(tw, "%v\t%d bytes\t%v\t%q\n", now.Sub(req.start), atomic.LoadUint64(&req.bytesWritten), req.http.RemoteAddr, req.http.Header.Get("User-agent"))
	}
	tw.Flush()
}

func (t *httpTracker) Wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := &httpRequest{
			http:  r,
			start: time.Now(),
		}
		t.mu.Lock()
		t.reqs[req] = struct{}{}
		t.mu.Unlock()
		defer func() {
			t.mu.Lock()
			delete(t.reqs, req)
			t.mu.Unlock()
		}()
		w = &byteCountingResponseWriter{w, &req.bytesWritten}
		h.ServeHTTP(w, r)
	})
}

type httpRequest struct {
	http         *http.Request
	start        time.Time
	bytesWritten uint64 // Accessed atomically.
}

type byteCountingResponseWriter struct {
	http.ResponseWriter
	bytesWritten *uint64
}

func (w *byteCountingResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	atomic.AddUint64(w.bytesWritten, uint64(n))
	return n, err
}

func (w *byteCountingResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
