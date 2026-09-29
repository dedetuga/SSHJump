// Verifies the ACAP auto-stops after the idle timeout even when a session is
// OPEN but unused (no keystrokes, no output) — the exact case the user hit.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

func main() {
	// in-process SSH server
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if c.User() == "u" && string(p) == "p" {
			return nil, nil
		}
		return nil, fmt.Errorf("no")
	}}
	cfg.AddHostKey(signer)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func(nc net.Conn) {
				conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(reqs)
				for nch := range chans {
					ch, chReqs, _ := nch.Accept()
					go func() {
						for r := range chReqs {
							r.Reply(r.Type == "pty-req" || r.Type == "shell", nil)
						}
					}()
					// one banner line, then stay completely silent
					ch.Write([]byte("ready\r\n"))
				}
			}(nc)
		}
	}()
	sshPort := ln.Addr().(*net.TCPAddr).Port

	// open a session on sshjump, then go quiet (no poll/input)
	body, _ := json.Marshal(map[string]interface{}{
		"host": "127.0.0.1", "port": sshPort, "user": "u",
		"authType": "password", "password": "p", "cols": 80, "rows": 24,
	})
	r, err := http.Post("http://127.0.0.1:2203/connect", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Println("FAIL: connect:", err)
		os.Exit(1)
	}
	r.Body.Close()
	fmt.Println("session opened; now idle (no I/O). idle timeout is 3s...")

	// wait past the timeout, then confirm the server process has exited
	time.Sleep(9 * time.Second)
	c, err := net.DialTimeout("tcp", "127.0.0.1:2203", time.Second)
	if err != nil {
		fmt.Println("PASS: server auto-stopped with an open-but-idle session")
		os.Exit(0)
	}
	c.Close()
	fmt.Println("FAIL: server still running after idle timeout")
	os.Exit(1)
}
