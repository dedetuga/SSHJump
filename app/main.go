// sshjump — ACAP web terminal that bridges a browser xterm.js session to an
// SSH connection on the local network. Intended as a small jump host so an
// operator can reach LAN devices (e.g. re-adopt a switch over SSH) through the
// camera's web interface. Auto-stops the ACAP after a period of inactivity.
//
// Transport: plain HTTP (long-poll for output, POST for input). The AXIS OS
// Apache reverse proxy strips WebSocket upgrade headers, so a WebSocket cannot
// survive the proxy hop; an HTTP long-poll transport works through any proxy.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ---- configuration (overridable via environment) ---------------------------

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var (
	listenAddr  = env("SSHJUMP_LISTEN", "127.0.0.1:2201")
	idleTimeout = parseDuration(env("SSHJUMP_IDLE_TIMEOUT", "30m"))
	appName     = env("SSHJUMP_APP", "sshjump")
)

func parseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 30 * time.Minute
	}
	return d
}

// ---- activity tracking (drives the idle shutdown) --------------------------

type activity struct {
	mu   sync.Mutex
	last time.Time
	n    int // active sessions
}

func (a *activity) touch() { a.mu.Lock(); a.last = time.Now(); a.mu.Unlock() }
func (a *activity) open()  { a.mu.Lock(); a.n++; a.last = time.Now(); a.mu.Unlock() }
func (a *activity) close() {
	a.mu.Lock()
	if a.n > 0 {
		a.n--
	}
	a.last = time.Now()
	a.mu.Unlock()
}
func (a *activity) idleFor() (time.Duration, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Since(a.last), a.n
}

var act = &activity{last: time.Now()}

// ---- connect request -------------------------------------------------------

type connectMsg struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	AuthType   string `json:"authType"`
	Password   string `json:"password"`
	Key        string `json:"key"`
	Passphrase string `json:"passphrase"`
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
}

// buildClientConfig assembles the SSH client config. The host key fingerprint
// seen during the handshake is written to *fp (trust-on-use; reported to the
// operator for manual verification).
func buildClientConfig(cm connectMsg, fp *string) (*ssh.ClientConfig, error) {
	var auths []ssh.AuthMethod
	switch cm.AuthType {
	case "key":
		var signer ssh.Signer
		var e error
		if cm.Passphrase != "" {
			signer, e = ssh.ParsePrivateKeyWithPassphrase([]byte(cm.Key), []byte(cm.Passphrase))
		} else {
			signer, e = ssh.ParsePrivateKey([]byte(cm.Key))
		}
		if e != nil {
			return nil, e
		}
		auths = append(auths, ssh.PublicKeys(signer))
	default:
		pw := cm.Password
		auths = append(auths,
			ssh.Password(pw),
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				ans := make([]string, len(qs))
				for i := range qs {
					ans[i] = pw
				}
				return ans, nil
			}),
		)
	}
	cfg := &ssh.ClientConfig{
		User: cm.User,
		Auth: auths,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if fp != nil {
				*fp = key.Type() + " " + ssh.FingerprintSHA256(key)
			}
			return nil
		},
		Timeout: 12 * time.Second,
		HostKeyAlgorithms: []string{
			ssh.KeyAlgoED25519,
			ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSA,
			ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
		},
	}
	// Include legacy ciphers often required by older network gear.
	cfg.Ciphers = append(cfg.Ciphers,
		"aes128-gcm@openssh.com", "aes256-gcm@openssh.com",
		"aes128-ctr", "aes192-ctr", "aes256-ctr",
	)
	return cfg, nil
}

// ---- session ---------------------------------------------------------------

type session struct {
	id        string
	client    *ssh.Client
	sess      *ssh.Session
	stdin     io.WriteCloser
	out       chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

func (s *session) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		if s.sess != nil {
			_ = s.sess.Close()
		}
		if s.client != nil {
			_ = s.client.Close()
		}
		sessions.del(s.id)
		act.close()
	})
}

func (s *session) pump(r io.Reader) {
	buf := make([]byte, 8192)
	for {
		n, e := r.Read(buf)
		if n > 0 {
			act.touch()
			b := make([]byte, n)
			copy(b, buf[:n])
			select {
			case s.out <- b:
			case <-s.closed:
				return
			}
		}
		if e != nil {
			return
		}
	}
}

type sessionRegistry struct {
	mu sync.Mutex
	m  map[string]*session
}

func (r *sessionRegistry) get(id string) *session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[id]
}
func (r *sessionRegistry) put(s *session) {
	r.mu.Lock()
	r.m[s.id] = s
	r.mu.Unlock()
}
func (r *sessionRegistry) del(id string) {
	r.mu.Lock()
	delete(r.m, id)
	r.mu.Unlock()
}

var sessions = &sessionRegistry{m: make(map[string]*session)}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- HTTP handlers ---------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// POST /connect  -> {id, fingerprint} | {error}
func connectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var cm connectMsg
	if err := json.NewDecoder(r.Body).Decode(&cm); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	if cm.Host == "" || cm.User == "" {
		writeJSON(w, 400, map[string]string{"error": "host and username are required"})
		return
	}
	if cm.Port == 0 {
		cm.Port = 22
	}
	if cm.Cols == 0 {
		cm.Cols = 80
	}
	if cm.Rows == 0 {
		cm.Rows = 24
	}

	var fp string
	cfg, err := buildClientConfig(cm, &fp)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid private key: " + err.Error()})
		return
	}

	addr := net.JoinHostPort(cm.Host, strconv.Itoa(cm.Port))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		// Append what actually arrived at the daemon (lengths only, never the
		// content) so a failed login reveals whether a field was truncated or
		// clobbered (e.g. by browser autofill) before it reached us.
		diag := err.Error()
		if cm.AuthType != "key" {
			diag += " [received: user " + strconv.Quote(cm.User) +
				" (" + strconv.Itoa(len(cm.User)) + " chars), password " +
				strconv.Itoa(len(cm.Password)) + " chars]"
		}
		writeJSON(w, 502, map[string]string{"error": "SSH connection failed: " + diag})
		return
	}
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		writeJSON(w, 502, map[string]string{"error": "session failed: " + err.Error()})
		return
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty("xterm-256color", cm.Rows, cm.Cols, modes); err != nil {
		sess.Close()
		client.Close()
		writeJSON(w, 502, map[string]string{"error": "PTY failed: " + err.Error()})
		return
	}
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	stderr, _ := sess.StderrPipe()
	if err := sess.Shell(); err != nil {
		sess.Close()
		client.Close()
		writeJSON(w, 502, map[string]string{"error": "shell failed: " + err.Error()})
		return
	}

	s := &session{
		id:     newID(),
		client: client,
		sess:   sess,
		stdin:  stdin,
		out:    make(chan []byte, 256),
		closed: make(chan struct{}),
	}
	sessions.put(s)
	act.open()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { s.pump(stdout); wg.Done() }()
	go func() { s.pump(stderr); wg.Done() }()
	go func() { wg.Wait(); s.close() }() // shell exited -> tear down

	writeJSON(w, 200, map[string]string{"id": s.id, "fingerprint": fp})
}

// GET /poll?id=..  long-poll for terminal output (application/octet-stream)
func pollHandler(w http.ResponseWriter, r *http.Request) {
	s := sessions.get(r.URL.Query().Get("id"))
	if s == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// NOTE: polling does NOT count as activity. The long-poll is transport
	// keep-alive; counting it would reset the idle timer every ~20s and the
	// ACAP would never auto-stop while a browser tab is open. Only real
	// terminal I/O (keystrokes via /input and shell output via pump) counts.

	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()

	var chunks [][]byte
	select {
	case b := <-s.out:
		chunks = append(chunks, b)
	case <-s.closed:
		// flush anything still buffered, then signal closed
		for {
			select {
			case b := <-s.out:
				chunks = append(chunks, b)
				continue
			default:
			}
			break
		}
		w.Header().Set("X-Session-Closed", "1")
		w.Header().Set("Content-Type", "application/octet-stream")
		for _, b := range chunks {
			w.Write(b)
		}
		return
	case <-timer.C:
		w.WriteHeader(http.StatusNoContent) // 204: nothing yet, client re-polls
		return
	}
	// drain whatever else is immediately available to batch it
	for {
		select {
		case b := <-s.out:
			chunks = append(chunks, b)
			continue
		default:
		}
		break
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	for _, b := range chunks {
		w.Write(b)
	}
}

// POST /input?id=..  body = raw stdin bytes
func inputHandler(w http.ResponseWriter, r *http.Request) {
	s := sessions.get(r.URL.Query().Get("id"))
	if s == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	act.touch()
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if len(body) > 0 {
		_, _ = s.stdin.Write(body)
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /resize?id=..&cols=..&rows=..
func resizeHandler(w http.ResponseWriter, r *http.Request) {
	s := sessions.get(r.URL.Query().Get("id"))
	if s == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// resize is not a user "activity" for idle purposes (it can fire on load
	// and on window resize without any real interaction).
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	if cols > 0 && rows > 0 {
		_ = s.sess.WindowChange(rows, cols)
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /close?id=..
func closeHandler(w http.ResponseWriter, r *http.Request) {
	if s := sessions.get(r.URL.Query().Get("id")); s != nil {
		s.close()
	}
	w.WriteHeader(http.StatusNoContent)
}

func healthHandler(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	idle, n := act.idleFor()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":          true,
		"sessions":    n,
		"idleSeconds": int(idle.Seconds()),
		"idleTimeout": idleTimeout.String(),
	})
}

// ---- idle watchdog ---------------------------------------------------------

func stopSelf() {
	log.Printf("idle for %s — stopping %s", idleTimeout, appName)
	_ = exec.Command("sh", "-c",
		"curl -s --max-time 5 'http://127.0.0.1/axis-cgi/applications/control.cgi?action=stop&package="+appName+"' >/dev/null 2>&1").Run()
	os.Exit(0)
}

func watchdog() {
	interval := 30 * time.Second
	if idleTimeout < interval {
		interval = idleTimeout
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for range tick.C {
		idle, _ := act.idleFor()
		// Idle = no real terminal I/O for the whole timeout. An open-but-unused
		// session does NOT keep the ACAP alive; stopSelf() tears everything down.
		if idle >= idleTimeout {
			stopSelf()
			return
		}
	}
}

// ---- routing ---------------------------------------------------------------
//
// The ACAP reverse proxy may or may not strip the configured apiPath prefix
// before forwarding, so we route by suffix to be robust to both behaviours.
func route(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/connect"):
		connectHandler(w, r)
	case strings.HasSuffix(p, "/poll"):
		pollHandler(w, r)
	case strings.HasSuffix(p, "/input"):
		inputHandler(w, r)
	case strings.HasSuffix(p, "/resize"):
		resizeHandler(w, r)
	case strings.HasSuffix(p, "/close"):
		closeHandler(w, r)
	case strings.HasSuffix(p, "/health"):
		healthHandler(w)
	default:
		http.NotFound(w, r)
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[sshjump] ")
	log.Printf("listening on %s, idle timeout %s (HTTP long-poll transport)", listenAddr, idleTimeout)

	go watchdog()

	srv := &http.Server{Addr: listenAddr, Handler: http.HandlerFunc(route)}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
