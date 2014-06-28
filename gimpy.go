// Command gimpy is a web server that serves go-import meta redirect headers.
// See "go help importpath" for details.
//
// TODO(adg): Add more documentation.
package main

import (
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/miekg/dns"
)

var (
	httpAddr     = flag.String("http", ":80", "HTTP listen address")
	resolverAddr = flag.String("resolver", "8.8.8.8:53", "DNS resolver address")
)

func main() {
	flag.Parse()
	http.Handle("/", NewServer(*resolverAddr))
	log.Fatal(http.ListenAndServe(*httpAddr, nil))
}

type Server struct {
	dns      *dns.Client
	resolver string

	mu    sync.RWMutex
	hosts map[string]*Host
}

func NewServer(resolver string) *Server {
	return &Server{
		dns: &dns.Client{
			Net:            "tcp",
			SingleInflight: true,
		},
		resolver: resolver,
		hosts:    map[string]*Host{},
	}
}

type Host struct {
	name    string
	imports []*Import
	expiry  time.Time
}

type Import struct {
	Prefix, VCS, URL string
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.Host)
	if r.FormValue("go-get") != "1" {
		if r.URL.Path == "/" {
			// TODO(adg): redirect to gimpy documentation
			http.NotFound(w, r)
			return
		}
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

func (s *Server) lookup(host string) (*Host, error) {
	m := &dns.Msg{}
	m.SetQuestion(host+".", dns.TypeTXT)
	r, _, err := s.dns.Exchange(m, s.resolver)
	if err != nil {
		return nil, err
	}
	h := &Host{name: host, expiry: time.Now().Add(time.Hour)}
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
	s.hosts[host] = h
	return h, nil
}

func parseImport(s string) *Import {
	const p = "go-import "
	if !strings.HasPrefix(s, p) {
		return nil
	}
	s = s[len(p):]
	f := strings.Fields(s)
	if len(f) != 3 {
		return nil
	}
	return &Import{f[0], f[1], f[2]}
}
