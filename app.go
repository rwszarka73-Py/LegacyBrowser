package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------

func xdg(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}

func configDir() string { return mkdir(filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "legacybrowser")) }
func dataDir() string   { return mkdir(filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "legacybrowser")) }
func cacheDir() string  { return mkdir(filepath.Join(xdg("XDG_CACHE_HOME", ".cache"), "legacybrowser")) }
func configPath() string {
	return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "legacybrowser", "servers.json")
}

func mkdir(p string) string {
	_ = os.MkdirAll(p, 0o700)
	return p
}

// ---------------------------------------------------------------------------
// Log: console + file + in-memory tail for the control panel
// ---------------------------------------------------------------------------

var (
	logMu   sync.Mutex
	logTail []string
	logFile *os.File
)

func logInit() {
	f, err := os.OpenFile(filepath.Join(dataDir(), "legacybrowser.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		logFile = f
	}
}

func logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	log.Print(line)
	stamp := time.Now().Format("15:04:05") + "  " + line
	logMu.Lock()
	logTail = append(logTail, stamp)
	if len(logTail) > 300 {
		logTail = logTail[len(logTail)-300:]
	}
	if logFile != nil {
		fmt.Fprintln(logFile, time.Now().Format("2006-01-02 ")+stamp)
	}
	logMu.Unlock()
}

func logLines() []string {
	logMu.Lock()
	defer logMu.Unlock()
	return append([]string{}, logTail...)
}

// ---------------------------------------------------------------------------
// Saved servers
// ---------------------------------------------------------------------------

type Server struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Type     string `json:"type"` // auto / ilo2 / ilo3 / web
	Username string `json:"username"`
	Password string `json:"password,omitempty"` // only if "remember" was checked
}

type Config struct {
	Servers []Server `json:"servers"`
}

func loadConfig() *Config {
	c := &Config{}
	if b, err := os.ReadFile(configPath()); err == nil {
		_ = json.Unmarshal(b, c)
	}
	return c
}

func (c *Config) save() error {
	configDir()
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(configPath(), b, 0o600)
}

// ---------------------------------------------------------------------------
// App
// ---------------------------------------------------------------------------

type App struct {
	mu          sync.Mutex
	cfg         *Config
	relays      map[string]*webRelay // target -> relay
	connect     *connectRelay
	openBrowser bool
	quit        chan struct{}
	panelURL    string
}

func newApp(openBrowserAuto bool) *App {
	return &App{cfg: loadConfig(), relays: map[string]*webRelay{}, openBrowser: openBrowserAuto, quit: make(chan struct{})}
}

func (a *App) open(url string) {
	if a.openBrowser {
		openBrowser(url)
	}
}

// webURL returns the local address of a device's web interface, starting its relay if needed.
func (a *App) webURL(host string) (string, error) {
	target := hostPort(host)
	a.mu.Lock()
	defer a.mu.Unlock()
	if r, ok := a.relays[target]; ok {
		return r.local + "/", nil
	}
	if _, err := dialLegacyCheck(target); err != nil {
		return "", err
	}
	r, err := startWebRelay(target)
	if err != nil {
		return "", err
	}
	a.relays[target] = r
	return r.local + "/", nil
}

func dialLegacyCheck(target string) (string, error) {
	c, err := dialLegacy(target)
	if err != nil {
		return "", err
	}
	c.Close()
	return "", nil
}

func (a *App) shutdown() {
	logf("%s closed", appName)
	if logFile != nil {
		logFile.Close()
	}
}

func newID() string {
	return strings.ReplaceAll(fmt.Sprintf("%x", time.Now().UnixNano()), "-", "")
}
