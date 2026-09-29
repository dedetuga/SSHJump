// Integration test for the HTTP long-poll transport: starts an in-process SSH
// server, then drives the running sshjump server (127.0.0.1:2201) exactly as
// the browser does (POST /connect, GET /poll, POST /input) and checks the
// full browser<->HTTP<->SSH bridge.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func fail(msg string) { fmt.Println("FAIL:", msg); os.Exit(1) }

func startSSH() int {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "u" && string(pass) == "p" {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail("listen ssh: " + err.Error())
	}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go handleSSH(nc, cfg)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func handleSSH(nc net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, chReqs, _ := nch.Accept()
		go func() {
			for r := range chReqs {
				switch r.Type {
				case "pty-req", "shell", "window-change":
					r.Reply(true, nil)
				default:
					r.Reply(false, nil)
				}
			}
		}()
		go func() {
			ch.Write([]byte("TESTOK\r\n"))
			buf := make([]byte, 256)
			for {
				n, e := ch.Read(buf)
				if n > 0 {
					ch.Write(buf[:n]) // echo
				}
				if e != nil {
					break
				}
			}
			ch.Close()
		}()
	}
}

const base = "http://127.0.0.1:2201/"

func main() {
	sshPort := startSSH()

	// POST /connect
	connect, _ := json.Marshal(map[string]interface{}{
		"host": "127.0.0.1", "port": sshPort, "user": "u",
		"authType": "password", "password": "p", "cols": 80, "rows": 24,
	})
	resp, err := http.Post(base+"connect", "application/json", bytes.NewReader(connect))
	if err != nil {
		fail("connect: " + err.Error())
	}
	var cr struct {
		ID          string `json:"id"`
		Fingerprint string `json:"fingerprint"`
		Error       string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	if cr.Error != "" || cr.ID == "" {
		fail("connect returned: " + cr.Error)
	}
	fmt.Println("fingerprint reported:", cr.Fingerprint != "")
	fmt.Println("session id:          ", cr.ID != "")

	gotBanner, gotEcho, sentInput := false, false, false
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		r, err := http.Get(base + "poll?id=" + cr.ID)
		if err != nil {
			fail("poll: " + err.Error())
		}
		if r.Header.Get("X-Session-Closed") == "1" {
			r.Body.Close()
			break
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		s := string(body)
		if strings.Contains(s, "TESTOK") {
			gotBanner = true
			if !sentInput {
				sentInput = true
				http.Post(base+"input?id="+cr.ID, "application/octet-stream",
					strings.NewReader("PINGME"))
			}
		}
		if strings.Contains(s, "PINGME") {
			gotEcho = true
		}
		if gotBanner && gotEcho {
			break
		}
	}

	fmt.Println("stdout banner:       ", gotBanner)
	fmt.Println("stdin echo:          ", gotEcho)

	// health
	if r, err := http.Get(base + "health"); err == nil {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		fmt.Println("health:              ", strings.TrimSpace(string(b)))
	}

	if gotBanner && gotEcho && cr.Fingerprint != "" {
		fmt.Println("PASS")
		os.Exit(0)
	}
	fail("missing expected signals")
}
