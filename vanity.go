// Command vanity is a web server that serves go-import meta redirects for
// vanity domains. See "go help importpath" for details.
//
// Vanity reads TXT records for the requested domain to determine the redirect
// target. For example, if you wish to use example.org as the base of your
// import path, create an A record that points to a vanity server:
//
// 	example.org.	A	108.59.82.123
//
// Then add a TXT record for each repository that you wish to map:
//
//	example.org.	TXT	"go-import example.org/foo git https://github.com/example/foo"
//	example.org.	TXT	"go-import example.org/bar hg https://code.google.com/p/bar"
//
// (The author runs a public vanity instance at 108.59.82.123 that you may use
// for your own redirects. It comes with no SLA, so use at your own risk.)
//
// Written by Andrew Gerrand <adg@golang.org>
//
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"html/template"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/storage"
	"golang.org/x/build/autocertcache"
	"golang.org/x/crypto/acme/autocert"

	"github.com/nf/vanity/internal/dns"
)

var (
	httpAddr      = flag.String("http", "", "HTTP listen address")
	httpsAddr     = flag.String("https", "", "HTTPs listen address (enables letsencrypt)")
	resolverAddr  = flag.String("resolver", "8.8.8.8:53", "DNS resolver address")
	refreshPeriod = flag.Duration("refresh", 15*time.Minute, "refresh period")
	anusEnabled   = flag.Bool("anus", false, "enable anus.io web root")
)

func main() {
	flag.Parse()

	var anusHandler http.Handler
	if *anusEnabled {
		t := newHTTPTracker()
		http.Handle("/anusz", t)
		anusHandler = t.Wrap(http.HandlerFunc(anus))
	}

	s := NewServer(*resolverAddr, *refreshPeriod)
	var rootHandler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if *anusEnabled && r.URL.Path == "/" && r.FormValue("go-get") != "1" {
			anusHandler.ServeHTTP(w, r)
			return
		}
		s.ServeHTTP(w, r)
	})

	if *httpsAddr != "" {
		if !metadata.OnGCE() {
			log.Fatal("Not on GCE. HTTPS only supported on GCE using letsencrypt. Exiting.")
		}
		cli, err := storage.NewClient(context.Background())
		if err != nil {
			log.Fatal(err)
		}
		bucket, err := metadata.InstanceAttributeValue("vanity-letsencrypt-bucket")
		if err != nil {
			log.Fatal(err)
		}
		m := &autocert.Manager{
			Cache:  autocertcache.NewGoogleCloudStorageCache(cli, bucket), // TODO
			Prompt: autocert.AcceptTOS,
		}
		rootHandler = m.HTTPHandler(rootHandler)
		srv := &http.Server{
			Addr:      *httpsAddr,
			TLSConfig: &tls.Config{GetCertificate: m.GetCertificate},
		}
		go func() {
			log.Println("Starting HTTPS server on", *httpsAddr)
			log.Fatal(srv.ListenAndServeTLS("", ""))
		}()
	}

	if *httpAddr != "" {
		go func() {
			log.Println("Starting HTTP server on", *httpAddr)
			log.Fatal(http.ListenAndServe(*httpAddr, nil))
		}()
	}

	http.Handle("/", rootHandler)
	select {}
}

type Server struct {
	resolver string
	refresh  time.Duration
	dns      *dns.Client

	mu    sync.RWMutex
	hosts map[string]*Host
}

func NewServer(resolver string, refresh time.Duration) *Server {
	return &Server{
		resolver: resolver,
		refresh:  refresh,
		dns:      &dns.Client{Net: "tcp", SingleInflight: true},
		hosts:    map[string]*Host{},
	}
}

type Host struct {
	imports []*Import
	expiry  time.Time
}

type Import struct {
	Prefix, VCS, URL string
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", "github.com/nf/vanity")

	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	if r.FormValue("go-get") != "1" {
		http.Redirect(w, r, "http://godoc.org/"+host+r.URL.Path, http.StatusFound)
		return
	}
	h := s.match(host)
	if h == nil {
		var err error
		h, err = s.lookup(host)
		if err != nil {
			log.Printf("lookup %q: %v", host, err)
			http.NotFound(w, r)
			return
		}
	}
	if err := metaTmpl.Execute(w, h.imports); err != nil {
		log.Println("writing response:", err)
	}
}

var metaTmpl = template.Must(template.New("meta").Parse(`
{{range .}}<meta name="go-import" content="{{.Prefix}} {{.VCS}} {{.URL}}">{{end}}
`))

func (s *Server) match(host string) *Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if h := s.hosts[host]; h != nil && h.expiry.After(time.Now()) {
		return h
	}
	return nil
}

func (s *Server) lookup(name string) (*Host, error) {
	m := &dns.Msg{}
	m.SetQuestion(name+".", dns.TypeTXT)
	r, _, err := s.dns.Exchange(m, s.resolver)
	if err != nil {
		return nil, err
	}
	h := &Host{expiry: time.Now().Add(s.refresh)}
	for _, a := range r.Answer {
		t, ok := a.(*dns.TXT)
		if !ok {
			continue
		}
		for _, s := range t.Txt {
			if i := parseImport(s); i != nil {
				h.imports = append(h.imports, i)
			}
		}
	}
	if len(h.imports) == 0 {
		return nil, errors.New("no go-import TXT records found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts[name] = h
	return h, nil
}

func parseImport(s string) *Import {
	const p = "go-import "
	if !strings.HasPrefix(s, p) {
		return nil
	}
	f := strings.Fields(s[len(p):])
	if len(f) != 3 {
		return nil
	}
	return &Import{f[0], f[1], f[2]}
}

func anus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	const delay = 5 * time.Millisecond
	t, done := time.NewTimer(delay), r.Context().Done()
	defer t.Stop()

	for d := delay; ; d += delay {
		if _, err := w.Write([]byte("\U0001F4A9")); err != nil {
			return
		}
		w.(http.Flusher).Flush()

		select {
		case <-done:
			return
		case <-t.C:
		}
		t.Reset(d)
	}
}
