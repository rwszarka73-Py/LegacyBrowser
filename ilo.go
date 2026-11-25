package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const ieUserAgent = "Mozilla/5.0 (Windows NT 10.0; WOW64; Trident/7.0; rv:11.0) like Gecko"

// consoleSpec describes how to run the iLO's own console applet.
type consoleSpec struct {
	Kind     string // ilo2 / ilo3
	Target   string // host:port
	JarPath  string // iLO path of the applet jar, e.g. /rc175p08.jar
	Class    string
	DocPath  string
	Params   map[string]string
	Cookies  []string // name=value
	Width    int
	Height   int
}

func httpsURL(target, path string) string {
	return "https://" + hostHeader(target) + path
}

// get performs a GET with explicit cookies and returns body, response.
func iloGet(c *http.Client, target, path string, cookies []string) (string, *http.Response, error) {
	req, _ := http.NewRequest("GET", httpsURL(target, path), nil)
	req.Header.Set("User-Agent", ieUserAgent)
	req.Header.Set("Referer", httpsURL(target, "/"))
	if len(cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(cookies, "; "))
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(b), resp, err
}

// detectILO returns "ilo2", "ilo3" or an error.
func detectILO(target string) (string, error) {
	c := legacyClient()
	paths := []string{"/", "/login.htm", "/html/login.html", "/index.html"}
	for _, p := range paths {
		body, resp, err := iloGet(c, target, p, nil)
		if err != nil {
			return "", err
		}
		if loc := resp.Header.Get("Location"); loc != "" && p == "/" {
			// follow one redirect within the device
			if i := strings.Index(loc, "://"); i >= 0 {
				if j := strings.Index(loc[i+3:], "/"); j >= 0 {
					loc = loc[i+3+j:]
				} else {
					loc = "/"
				}
			}
			if b2, _, err := iloGet(c, target, loc, nil); err == nil {
				body += b2
			}
		}
		lb := strings.ToLower(body)
		switch {
		case strings.Contains(lb, "lights-out 2") || strings.Contains(lb, "ilo 2"):
			return "ilo2", nil
		case strings.Contains(lb, "lights-out 3") || strings.Contains(lb, "ilo 3"):
			return "ilo3", nil
		}
	}
	// iLO 3 has a JSON API; iLO 2 does not
	req, _ := http.NewRequest("POST", httpsURL(target, "/json/login_session"), strings.NewReader(`{"method":"login","user_login":"","password":""}`))
	req.Header.Set("Content-Type", "application/json")
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
		if resp.StatusCode != 404 {
			return "ilo3", nil
		}
	}
	return "", errors.New("couldn't tell whether this is an iLO 2 or iLO 3 - choose the type manually")
}

// ---------------------------------------------------------------------------
// iLO 2: login.htm -> index.htm (login cookie) -> drc2fram.htm (applet parameters)
// ---------------------------------------------------------------------------

var (
	reSessKey   = regexp.MustCompile(`var\s+sessionkey\s*=\s*"([^"]+)"`)
	reSessIdx   = regexp.MustCompile(`var\s+sessionindex\s*=\s*"([^"]+)"`)
	reInfoQ     = regexp.MustCompile(`(?i)\binfo([0-9a-z]{1,2})\s*=\s*"([^"]*)"\s*;`)
	reInfoU     = regexp.MustCompile(`(?i)\binfo([0-9a-z]{1,2})\s*=\s*([^";\s]+)\s*;`)
	reCabBase   = regexp.MustCompile(`(?i)<PARAM\s+NAME\s*=\s*["']?CABBASE["']?\s+VALUE\s*=\s*["']?([^"'>\s]+)`)
	reArchive   = regexp.MustCompile(`(?i)ARCHIVE\s*=\s*\\?["']?([\w./-]+\.jar)`)
	reAnyJar    = regexp.MustCompile(`([\w./-]+\.jar)`)
	reCode      = regexp.MustCompile(`(?i)\bCODE\s*=\s*\\?["']?([\w.]+?)(?:\.class)?\\?["'\s>]`)
	reAppletDim = regexp.MustCompile(`(?i)<APPLET[^>]*?\bWIDTH\s*=\s*\\?["']?(\d+)[^>]*?\bHEIGHT\s*=\s*\\?["']?(\d+)`)
)

func ilo2Console(target, user, pass string) (*consoleSpec, error) {
	c := legacyClient()

	body, _, err := iloGet(c, target, "/login.htm", []string{"hp-iLO-Login="})
	if err != nil {
		return nil, err
	}
	mk, mi := reSessKey.FindStringSubmatch(body), reSessIdx.FindStringSubmatch(body)
	if mk == nil || mi == nil {
		return nil, errors.New("iLO 2 login page didn't contain a session key (is this an iLO 2?)")
	}
	login := fmt.Sprintf("hp-iLO-Login=%s:%s:%s:%s", mi[1],
		base64.StdEncoding.EncodeToString([]byte(user)),
		base64.StdEncoding.EncodeToString([]byte(pass)), mk[1])

	body, resp, err := iloGet(c, target, "/index.htm", []string{login})
	if err != nil {
		return nil, err
	}
	var cookies []string
	for _, ck := range resp.Cookies() {
		if ck.Value != "" {
			cookies = append(cookies, ck.Name+"="+ck.Value)
		}
	}
	hasSession := false
	for _, ck := range cookies {
		if strings.HasPrefix(ck, "hp-iLO-Session=") {
			hasSession = true
		}
	}
	if !hasSession {
		if strings.Contains(body, "Login Delay") {
			return nil, errors.New("iLO 2 refused the login (login delay after failed attempts - wait a minute and try again)")
		}
		return nil, errors.New("iLO 2 login failed - check the user name and password")
	}
	logf("%s: logged in to iLO 2", target)

	page, _, err := iloGet(c, target, "/drc2fram.htm?restart=1", cookies)
	if err != nil {
		return nil, err
	}
	params := map[string]string{}
	for _, m := range reInfoQ.FindAllStringSubmatch(page, -1) {
		params["INFO"+strings.ToUpper(m[1])] = m[2]
	}
	for _, m := range reInfoU.FindAllStringSubmatch(page, -1) {
		k := "INFO" + strings.ToUpper(m[1])
		if _, ok := params[k]; !ok {
			params[k] = m[2]
		}
	}
	if m := reCabBase.FindStringSubmatch(page); m != nil {
		params["CABBASE"] = m[1]
	}
	if params["INFO0"] == "" {
		saveDebug("ilo2-drc2fram.htm", page)
		return nil, errors.New("iLO 2 console page didn't have the expected parameters (saved a copy to the log folder)")
	}

	spec := &consoleSpec{Kind: "ilo2", Target: target, DocPath: "/drc2fram.htm", Params: params,
		Cookies: cookies, Class: "com.hp.ilo2.remcons.remcons", Width: 1060, Height: 850}
	if m := reArchive.FindStringSubmatch(page); m != nil {
		spec.JarPath = m[1]
	} else if m := reAnyJar.FindStringSubmatch(page); m != nil {
		spec.JarPath = m[1]
	} else {
		saveDebug("ilo2-drc2fram.htm", page)
		return nil, errors.New("couldn't find the console applet in the iLO 2 page (saved a copy to the log folder)")
	}
	if m := reCode.FindStringSubmatch(page); m != nil && strings.Contains(m[1], ".") {
		spec.Class = m[1]
	}
	if m := reAppletDim.FindStringSubmatch(page); m != nil {
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		if w >= 400 && h >= 300 {
			spec.Width, spec.Height = w, h
		}
	}
	if !strings.HasPrefix(spec.JarPath, "/") {
		spec.JarPath = "/" + spec.JarPath
	}
	return spec, nil
}

// ---------------------------------------------------------------------------
// iLO 3: /json/login_session -> session key -> Java IRC applet (intgapp3_*.jar)
// ---------------------------------------------------------------------------

var (
	reIntgJar = regexp.MustCompile(`(intgapp[\w]*\.jar)`)
	reInfo0   = regexp.MustCompile(`INFO0["']?[^0-9A-Fa-f]{0,30}([0-9A-Fa-f]{32})`)
	reInfo1   = regexp.MustCompile(`INFO1["']?[^0-9]{0,30}(\d{2,5})`)
)

func ilo3Console(target, user, pass string) (*consoleSpec, error) {
	c := legacyClient()
	payload, _ := json.Marshal(map[string]string{"method": "login", "user_login": user, "password": pass})
	req, _ := http.NewRequest("POST", httpsURL(target, "/json/login_session"), strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ieUserAgent)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	var res map[string]any
	_ = json.Unmarshal(b, &res)
	key, _ := res["session_key"].(string)
	if key == "" {
		msg, _ := res["message"].(string)
		if msg == "" {
			msg = strings.TrimSpace(string(b))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		if resp.StatusCode == 404 {
			return nil, errors.New("this iLO doesn't have the iLO 3 JSON login (update the iLO 3 firmware, or choose iLO 2)")
		}
		return nil, fmt.Errorf("iLO 3 login failed (%s) - check the user name and password", msg)
	}
	logf("%s: logged in to iLO 3", target)
	cookies := []string{"sessionKey=" + key}

	spec := &consoleSpec{Kind: "ilo3", Target: target, DocPath: "/html/java_irc.html",
		Class: "com.hp.ilo2.intgapp.intgapp", Cookies: cookies, Width: 1, Height: 1,
		Params: map[string]string{
			"RCINFO1":    key,
			"RCINFOLANG": "en",
			"INFO0":      "7AC3BDEBC9AC64E85734454B53BB73CE",
			"INFO1":      "17988",
			"INFO2":      "composite",
		}}

	var pages string
	for _, p := range []string{"/html/java_irc.html", "/html/jnlp_template.html"} {
		if body, r, err := iloGet(c, target, p, cookies); err == nil && r.StatusCode == 200 {
			pages += body
		}
	}
	if m := reIntgJar.FindStringSubmatch(pages); m != nil {
		spec.JarPath = "/html/" + m[1]
	} else {
		spec.JarPath = "/html/intgapp3_231.jar" // iLO 3 1.94
		logf("%s: console jar name not found in iLO pages, trying %s", target, spec.JarPath)
	}
	if m := reInfo0.FindStringSubmatch(pages); m != nil {
		spec.Params["INFO0"] = m[1]
	}
	if m := reInfo1.FindStringSubmatch(pages); m != nil {
		spec.Params["INFO1"] = m[1]
	}
	return spec, nil
}

// downloadJar fetches the applet jar from the device into the cache.
func downloadJar(spec *consoleSpec) (string, error) {
	dir := filepath.Join(cacheDir(), "jars")
	_ = os.MkdirAll(dir, 0o700)
	safeHost := strings.NewReplacer(":", "_", "/", "_", "[", "", "]", "").Replace(spec.Target)
	dst := filepath.Join(dir, safeHost+"_"+filepath.Base(spec.JarPath))
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 1000 {
		return dst, nil
	}
	c := legacyClient()
	req, _ := http.NewRequest("GET", httpsURL(spec.Target, spec.JarPath), nil)
	req.Header.Set("User-Agent", ieUserAgent)
	req.Header.Set("Cookie", strings.Join(spec.Cookies, "; "))
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("downloading %s: HTTP %d", spec.JarPath, resp.StatusCode)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, resp.Body)
	f.Close()
	if err != nil || n < 1000 {
		os.Remove(tmp)
		return "", fmt.Errorf("downloading %s failed (%d bytes): %v", spec.JarPath, n, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	logf("%s: downloaded console %s (%d KB)", spec.Target, filepath.Base(spec.JarPath), n/1024)
	return dst, nil
}

func saveDebug(name, content string) {
	p := filepath.Join(dataDir(), name)
	_ = os.WriteFile(p, []byte(content), 0o600)
	logf("saved %s for troubleshooting", p)
}
