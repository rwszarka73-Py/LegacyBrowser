package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed applethost/applethost.jar
var appletHostJar []byte

var javaMu sync.Mutex

// The iLO consoles are Java applets. The applet API was removed in newer Java, and
// the iLO 2 console is known to hang on Java 15+, so we use Java 8-14 (prefer 11).
const javaMinOK, javaMaxOK = 8, 14

func bundledJavaDir() string { return filepath.Join(dataDir(), "jre11") }

// findJava returns a usable java binary, downloading Java 11 if needed.
func findJava() (string, error) {
	javaMu.Lock()
	defer javaMu.Unlock()

	if p := os.Getenv("LEGACYBROWSER_JAVA"); p != "" {
		return p, nil
	}
	bundled := filepath.Join(bundledJavaDir(), "bin", "java")
	if fi, err := os.Stat(bundled); err == nil && !fi.IsDir() {
		return bundled, nil
	}
	if p, err := exec.LookPath("java"); err == nil {
		if v := javaMajor(p); v >= javaMinOK && v <= javaMaxOK && javaHasGUI(p) {
			return p, nil
		}
	}
	// Common install locations of older JREs
	if matches, _ := filepath.Glob("/usr/lib/jvm/*/bin/java"); len(matches) > 0 {
		sort.Strings(matches)
		for _, p := range matches {
			if v := javaMajor(p); v >= javaMinOK && v <= javaMaxOK && javaHasGUI(p) {
				return p, nil
			}
		}
	}
	if err := downloadJava(); err != nil {
		return "", err
	}
	return bundled, nil
}

// javaHasGUI is false for "headless" Java builds (common on servers), which can't open windows.
func javaHasGUI(javaPath string) bool {
	real, err := filepath.EvalSymlinks(javaPath)
	if err != nil {
		return false
	}
	home := filepath.Dir(filepath.Dir(real))
	for _, pat := range []string{"lib/libawt_xawt.so", "lib/*/libawt_xawt.so", "jre/lib/*/libawt_xawt.so", "../lib/libawt_xawt.so"} {
		if m, _ := filepath.Glob(filepath.Join(home, pat)); len(m) > 0 {
			return true
		}
	}
	return false
}

var reJavaVer = regexp.MustCompile(`version "(\d+)(?:\.(\d+))?`)

func javaMajor(javaPath string) int {
	out, err := exec.Command(javaPath, "-version").CombinedOutput()
	if err != nil {
		return 0
	}
	m := reJavaVer.FindStringSubmatch(string(out))
	if m == nil {
		return 0
	}
	v, _ := strconv.Atoi(m[1])
	if v == 1 && m[2] != "" { // 1.8 -> 8
		v, _ = strconv.Atoi(m[2])
	}
	return v
}

func downloadJava() error {
	arch := map[string]string{"amd64": "x64", "arm64": "aarch64", "arm": "arm", "ppc64le": "ppc64le"}[runtime.GOARCH]
	if arch == "" {
		return fmt.Errorf("no automatic Java download for %s - install Java 11 and set LEGACYBROWSER_JAVA", runtime.GOARCH)
	}
	url := fmt.Sprintf("https://api.adoptium.net/v3/binary/latest/11/ga/linux/%s/jre/hotspot/normal/eclipse", arch)
	logf("Downloading Java 11 runtime for the console (one time, about 45 MB)...")
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("Java download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Java download failed: HTTP %d", resp.StatusCode)
	}
	tmp := bundledJavaDir() + ".part"
	os.RemoveAll(tmp)
	if err := extractTarGz(resp.Body, tmp); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("Java download failed: %w", err)
	}
	os.RemoveAll(bundledJavaDir())
	if err := os.Rename(tmp, bundledJavaDir()); err != nil {
		return err
	}
	logf("Java 11 installed in %s", bundledJavaDir())
	return nil
}

// extractTarGz extracts an archive, dropping its top-level folder.
func extractTarGz(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := h.Name
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[i+1:]
		} else {
			continue
		}
		if name == "" || strings.Contains(name, "..") {
			continue
		}
		p := filepath.Join(dst, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			if strings.HasPrefix(h.Linkname, "/") || strings.Contains(h.Linkname, "..") {
				continue
			}
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.Symlink(h.Linkname, p)
		}
	}
}

func appletHostPath() (string, error) {
	p := filepath.Join(dataDir(), "applethost.jar")
	if b, err := os.ReadFile(p); err == nil && bytes.Equal(b, appletHostJar) {
		return p, nil
	}
	return p, os.WriteFile(p, appletHostJar, 0o644)
}

// runConsole starts the Java applet host for a prepared console.
func (a *App) runConsole(spec *consoleSpec, jar string) error {
	javaBin, err := findJava()
	if err != nil {
		return err
	}
	host, err := appletHostPath()
	if err != nil {
		return err
	}
	if a.connect == nil {
		if a.connect, err = startConnectRelay(); err != nil {
			return err
		}
	}

	var b strings.Builder
	w := func(k, v string) { fmt.Fprintf(&b, "%s=%s\n", propEscape(k), propEscape(v)) }
	w("jar", jar)
	w("class", spec.Class)
	w("codebase", httpsURL(spec.Target, "/"))
	w("documentbase", httpsURL(spec.Target, spec.DocPath))
	w("title", fmt.Sprintf("%s - iLO %s Remote Console", hostHeader(spec.Target), strings.TrimPrefix(spec.Kind, "ilo")))
	w("width", strconv.Itoa(spec.Width))
	w("height", strconv.Itoa(spec.Height))
	w("proxy.host", "127.0.0.1")
	w("proxy.port", strconv.Itoa(a.connect.port()))
	for i, c := range spec.Cookies {
		w("cookie."+strconv.Itoa(i), c)
	}
	keys := make([]string, 0, len(spec.Params))
	for k := range spec.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w("param."+k, spec.Params[k])
	}

	safe := strings.NewReplacer(":", "_", "[", "", "]", "").Replace(spec.Target)
	props := filepath.Join(cacheDir(), "launch-"+safe+".properties")
	if err := os.WriteFile(props, []byte(b.String()), 0o600); err != nil {
		return err
	}
	logPath := filepath.Join(dataDir(), "console-"+safe+".log")
	lf, err := os.Create(logPath)
	if err != nil {
		return err
	}
	cmd := exec.Command(javaBin, "-cp", host, "LegacyAppletHost", props)
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		lf.Close()
		return err
	}
	logf("%s: console started (Java %s). Details: %s", spec.Target, javaBin, logPath)
	go func() {
		err := cmd.Wait()
		lf.Close()
		os.Remove(props) // contains the session key
		if err != nil {
			logf("%s: console exited: %v (see %s)", spec.Target, err, logPath)
		} else {
			logf("%s: console closed", spec.Target)
		}
	}()
	return nil
}

func propEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`, "=", `\=`, ":", `\:`, "#", `\#`, "!", `\!`)
	return r.Replace(s)
}

// launchConsole logs in, fetches the console and runs it.
func (a *App) launchConsole(host, user, pass, kind string) error {
	target := hostPort(host)
	if kind == "" || kind == "auto" {
		k, err := detectILO(target)
		if err != nil {
			return err
		}
		kind = k
		logf("%s: detected %s", target, strings.Replace(kind, "ilo", "iLO ", 1))
	}
	var spec *consoleSpec
	var err error
	switch kind {
	case "ilo2":
		spec, err = ilo2Console(target, user, pass)
	case "ilo3":
		spec, err = ilo3Console(target, user, pass)
	default:
		return errors.New("unknown iLO type " + kind)
	}
	if err != nil {
		return err
	}
	jar, err := downloadJar(spec)
	if err != nil {
		return err
	}
	return a.runConsole(spec, jar)
}
