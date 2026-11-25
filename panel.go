package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed panel.html
var panelHTML string

//go:embed favicon.png
var faviconPNG []byte

func randToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// startPanel serves the control panel on 127.0.0.1. Access requires a random token
// (given in the first URL, then kept in a same-site cookie) so other sites or local
// users can't drive it.
func (a *App) startPanel(port int) (string, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", err
	}
	token := randToken()
	base := "http://" + ln.Addr().String()

	authed := func(r *http.Request) bool {
		ck, err := r.Cookie("lbtoken")
		return err == nil && ck.Value == token
	}
	api := func(h func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !authed(r) || r.Header.Get("X-LB") != "1" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			res, err := h(w, r)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			json.NewEncoder(w).Encode(res)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(faviconPNG)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("t") == token {
			http.SetCookie(w, &http.Cookie{Name: "lbtoken", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if !authed(r) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "Open the control panel using the address shown in the Legacy Browser terminal window.")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, panelHTML)
	})

	type serverView struct {
		Server
		HasPassword bool `json:"hasPassword"`
	}
	mux.HandleFunc("/api/state", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		list := []serverView{}
		for _, s := range a.cfg.Servers {
			v := serverView{Server: s, HasPassword: s.Password != ""}
			v.Password = ""
			list = append(list, v)
		}
		return map[string]any{"servers": list, "log": logLines(), "version": version, "config": configPath(), "data": dataDir()}, nil
	}))

	mux.HandleFunc("/api/save", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		var in struct {
			Server
			Remember      bool `json:"remember"`
			ClearPassword bool `json:"clearPassword"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		in.Host = strings.TrimSpace(in.Host)
		if in.Host == "" {
			return nil, fmt.Errorf("enter the iLO's address")
		}
		if in.Name == "" {
			in.Name = in.Host
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		found := false
		for i, s := range a.cfg.Servers {
			if s.ID == in.ID && in.ID != "" {
				pw := s.Password
				if in.ClearPassword {
					pw = ""
				}
				if in.Remember && in.Password != "" {
					pw = in.Password
				}
				a.cfg.Servers[i] = Server{ID: s.ID, Name: in.Name, Host: in.Host, Type: in.Type, Username: in.Username, Password: pw}
				found = true
			}
		}
		if !found {
			s := Server{ID: newID(), Name: in.Name, Host: in.Host, Type: in.Type, Username: in.Username}
			if in.Remember {
				s.Password = in.Password
			}
			a.cfg.Servers = append(a.cfg.Servers, s)
		}
		return map[string]bool{"ok": true}, a.cfg.save()
	}))

	mux.HandleFunc("/api/delete", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		id := r.URL.Query().Get("id")
		a.mu.Lock()
		defer a.mu.Unlock()
		out := a.cfg.Servers[:0]
		for _, s := range a.cfg.Servers {
			if s.ID != id {
				out = append(out, s)
			}
		}
		a.cfg.Servers = out
		return map[string]bool{"ok": true}, a.cfg.save()
	}))

	mux.HandleFunc("/api/web", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		host := r.URL.Query().Get("host")
		u, err := a.webURL(host)
		if err != nil {
			logf("%s: %v", host, err)
			return nil, err
		}
		return map[string]string{"url": u}, nil
	}))

	mux.HandleFunc("/api/console", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		var in struct {
			ID, Host, Type, Username, Password string
			Remember                           bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		a.mu.Lock()
		for i, s := range a.cfg.Servers {
			if s.ID == in.ID {
				in.Host, in.Type = s.Host, s.Type
				if in.Username == "" {
					in.Username = s.Username
				}
				if in.Password == "" {
					in.Password = s.Password
				} else if in.Remember {
					a.cfg.Servers[i].Password = in.Password
					a.cfg.Servers[i].Username = in.Username
					_ = a.cfg.save()
				}
			}
		}
		a.mu.Unlock()
		if in.Host == "" || in.Username == "" {
			return nil, fmt.Errorf("enter the address, user name and password")
		}
		go func() {
			logf("%s: starting remote console...", in.Host)
			if err := a.launchConsole(in.Host, in.Username, in.Password, in.Type); err != nil {
				logf("%s: console failed: %v", in.Host, err)
			}
		}()
		return map[string]bool{"ok": true}, nil
	}))

	mux.HandleFunc("/api/detect", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		k, err := detectILO(hostPort(r.URL.Query().Get("host")))
		if err != nil {
			return nil, err
		}
		return map[string]string{"type": k}, nil
	}))

	mux.HandleFunc("/api/quit", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		go func() {
			time.Sleep(300 * time.Millisecond)
			close(a.quit)
		}()
		return map[string]bool{"ok": true}, nil
	}))

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 30 * time.Second}
	go srv.Serve(ln)
	a.panelURL = base + "/?t=" + token
	return a.panelURL, nil
}
