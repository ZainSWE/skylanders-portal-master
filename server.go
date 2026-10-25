//go:build !js

package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFiles embed.FS

// Backend is implemented by the real app (Windows) and by a demo for testing.
type Backend interface {
	State() APIState
	Update(patch map[string]json.RawMessage)
	SetStartup(on bool) error
	SetPortalType(key, typ string)
	UpdatePortal(key string, patch map[string]json.RawMessage) // change one portal's lights
	PlayPCM(name string, pcm []byte) error                     // interleaved stereo 16-bit LE at AudioRate
	PlayTest(variant int) error
	StopAudio()
	PlayFX(ev SoundEvent) // a portal sound, wherever the sound mode says
	Open(what string)
	Quit()
	DataDir() string // where the app keeps its files
}

type Server struct {
	backend Backend
	token   string // for the window on this PC
	addr    string

	rmu       sync.Mutex
	remoteLn  net.Listener // the phone remote's listener, while it's on
	remoteKey string       // the phone remote's key (kept between runs so a bookmark keeps working)
	remoteErr string
	addrs     []string // this PC's home-network addresses, refreshed every few seconds
	addrsAt   time.Time
}

// serve runs an HTTP server with timeouts, so a slow or stuck client can't tie it up.
func serve(ln net.Listener, h http.Handler) {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute,
		WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10}
	srv.Serve(ln)
}

// sameSecret compares secrets in constant time.
func sameSecret(got, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// RemotePort is where the phone remote listens on the home network.
const RemotePort = 47320

func newToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// StartServer listens on a random localhost port and serves the window. The phone remote is
// started and stopped as the setting changes.
func StartServer(b Backend) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{backend: b, token: newToken(), addr: ln.Addr().String()}
	go serve(ln, s.handler(false))
	go func() {
		for {
			s.syncRemote(b.State().Settings.Remote)
			time.Sleep(time.Second)
		}
	}()
	return s, nil
}

func (s *Server) URL() string { return "http://" + s.addr + "/?t=" + s.token }

func (s *Server) keyPath() string { return filepath.Join(s.backend.DataDir(), "phone-remote-key.txt") }

// key returns the phone remote's key, making one the first time.
func (s *Server) key() string {
	if s.remoteKey == "" {
		if b, err := os.ReadFile(s.keyPath()); err == nil && len(strings.TrimSpace(string(b))) == 32 {
			s.remoteKey = strings.TrimSpace(string(b))
		} else {
			s.remoteKey = newToken()
			os.WriteFile(s.keyPath(), []byte(s.remoteKey), 0o600)
		}
	}
	return s.remoteKey
}

// syncRemote opens or closes the phone remote's listener to match the setting.
func (s *Server) syncRemote(on bool) {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	switch {
	case on && s.remoteLn == nil:
		s.key()
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", RemotePort))
		if err != nil {
			s.remoteErr = fmt.Sprintf("Couldn't open port %d (%v). Another program may be using it.", RemotePort, err)
			return
		}
		s.remoteLn, s.remoteErr = ln, ""
		go serve(ln, s.handler(true))
	case !on && s.remoteLn != nil:
		s.remoteLn.Close()
		s.remoteLn = nil
	}
}

// resetRemoteKey makes a new key, so links shared before stop working.
func (s *Server) resetRemoteKey() {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	s.remoteKey = newToken()
	os.WriteFile(s.keyPath(), []byte(s.remoteKey), 0o600)
}

// homeAddresses lists this PC's addresses on the home network, the likeliest first.
func homeAddresses() []string {
	var out []string
	if c, err := net.Dial("udp", "192.0.2.1:9"); err == nil { // picks the outgoing address; sends nothing
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP.IsPrivate() {
			out = append(out, a.IP.String())
		}
		c.Close()
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() && !slices.Contains(out, n.IP.String()) {
			out = append(out, n.IP.String())
		}
	}
	return out
}

func (s *Server) remoteState() APIRemote {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	r := APIRemote{On: s.remoteLn != nil, Problem: s.remoteErr}
	if !r.On {
		return r
	}
	if time.Since(s.addrsAt) > 5*time.Second {
		s.addrs, s.addrsAt = homeAddresses(), time.Now()
	}
	addrs := s.addrs
	if len(addrs) == 0 {
		r.Problem = "This PC doesn't seem to be on a home network."
		return r
	}
	for i, ip := range addrs {
		u := fmt.Sprintf("http://%s:%d/?k=%s", ip, RemotePort, s.remoteKey)
		if i == 0 {
			r.URL = u
		} else {
			r.Others = append(r.Others, u)
		}
	}
	return r
}

// remoteHostOK accepts the PC's own address or name on the home network, but not other
// websites' names pointed at it (DNS rebinding).
func remoteHostOK(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsPrivate() || ip.IsLoopback()
	}
	return !strings.Contains(h, ".") || strings.HasSuffix(strings.ToLower(h), ".local")
}

// fromHomeNetwork reports whether a connection comes from this PC or the local network.
func fromHomeNetwork(remoteAddr string) bool {
	h, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

func (s *Server) handler(remote bool) http.Handler {
	static, _ := fs.Sub(webFiles, "web")
	files := http.FileServer(http.FS(static))
	mux := http.NewServeMux()
	mux.Handle("/", files)

	// Things a phone can't do: they act on the PC itself.
	pcOnly := map[string]bool{"/api/open": true, "/api/quit": true, "/api/startup": true, "/api/remote": true}
	api := func(path string, h func(w http.ResponseWriter, r *http.Request)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			ok := sameSecret(r.Header.Get("X-Token"), s.token)
			if remote {
				s.rmu.Lock()
				ok = sameSecret(r.Header.Get("X-Token"), s.remoteKey) && !pcOnly[path]
				s.rmu.Unlock()
			}
			if !ok {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodPost {
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			h(w, r)
		})
	}
	writeState := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		st := s.backend.State()
		st.IsRemote = remote
		if !remote {
			st.Remote = s.remoteState()
		}
		json.NewEncoder(w).Encode(st)
	}

	api("/api/remote", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("reset") != "" {
			s.resetRemoteKey()
		}
		writeState(w)
	})
	api("/api/state", func(w http.ResponseWriter, r *http.Request) { writeState(w) })
	api("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		var patch map[string]json.RawMessage
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&patch); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		lights, global := splitPatch(patch)
		var key string
		if raw, ok := patch["portal"]; ok && json.Unmarshal(raw, &key) == nil && key != "" && len(lights) > 0 {
			s.backend.UpdatePortal(key, lights)
			patch = global
		}
		if len(patch) > 0 {
			s.backend.Update(patch)
		}
		writeState(w)
	})
	api("/api/startup", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ On bool }
		json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body)
		if err := s.backend.SetStartup(body.On); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeState(w)
	})
	api("/api/portal", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Key, Type string }
		json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
		if body.Type != "classic" && body.Type != "trapteam" {
			http.Error(w, "unknown portal type", http.StatusBadRequest)
			return
		}
		s.backend.SetPortalType(body.Key, body.Type)
		writeState(w)
	})
	api("/api/audio/file", func(w http.ResponseWriter, r *http.Request) {
		pcm, err := io.ReadAll(io.LimitReader(r.Body, 64<<20)) // ~33 minutes of 8 kHz stereo
		if err != nil || len(pcm) < 4 {
			http.Error(w, "no audio received", http.StatusBadRequest)
			return
		}
		if err := s.backend.PlayPCM(r.URL.Query().Get("name"), pcm); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeState(w)
	})
	api("/api/audio/test", func(w http.ResponseWriter, r *http.Request) {
		v, _ := strconv.Atoi(r.URL.Query().Get("variant"))
		if err := s.backend.PlayTest(v); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeState(w)
	})
	api("/api/audio/fx", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		style, kind := q.Get("style"), q.Get("kind")
		if style != "trapteam" {
			style = "classic"
		}
		if !slices.Contains([]string{"place", "trap", "on", "off"}, kind) {
			kind = "place"
		}
		s.backend.PlayFX(SoundEvent{Style: style, Kind: kind, Force: q.Get("force") == "1"})
		writeState(w)
	})
	api("/api/audio/stop", func(w http.ResponseWriter, r *http.Request) {
		s.backend.StopAudio()
		writeState(w)
	})
	api("/api/open", func(w http.ResponseWriter, r *http.Request) {
		s.backend.Open(r.URL.Query().Get("what"))
		w.WriteHeader(http.StatusNoContent)
	})
	api("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		go s.backend.Quit()
	})

	// Only answer requests addressed to us (blocks DNS-rebinding tricks).
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if remote && (!remoteHostOK(r.Host) || !fromHomeNetwork(r.RemoteAddr)) ||
			!remote && r.Host != s.addr && r.Host != "localhost:"+strings.TrimPrefix(s.addr, "127.0.0.1:") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		mux.ServeHTTP(w, r)
	})
}
