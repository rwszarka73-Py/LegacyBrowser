// Legacy Browser for Linux
//
// Opens old HP iLO 2 / iLO 3 web interfaces (and other TLS 1.0/1.1 devices) in any
// modern browser, and launches the iLO Java remote console without a browser plugin.
//
// Everything runs from this one binary. A Java runtime for the console is downloaded
// automatically the first time it's needed (or set LEGACYBROWSER_JAVA).
//
//go:debug tlsrsakex=1
//go:debug tls3des=1
//go:debug rsa1024min=0
//go:debug tls10server=1
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

const appName = "Legacy Browser"
const version = "1.0"

func usage() {
	fmt.Fprintf(os.Stderr, `%s %s for Linux

Usage:
  legacybrowser                       Open the control panel in your browser
  legacybrowser web <host>            Open a device's web interface
  legacybrowser console <host> <user> Launch the iLO remote console (asks for password)

Options:
  -nobrowser   Don't open a browser automatically (print the address instead)
  -port N      Control panel port (default: any free port)

Settings: %s
`, appName, version, configPath())
}

func main() {
	noBrowser := flag.Bool("nobrowser", false, "don't open a browser automatically")
	port := flag.Int("port", 0, "control panel port")
	flag.Usage = usage
	flag.Parse()

	log.SetFlags(log.Ltime)
	logInit()

	app := newApp(!*noBrowser)

	args := flag.Args()
	switch {
	case len(args) == 0:
		url, err := app.startPanel(*port)
		if err != nil {
			log.Fatalf("Can't start control panel: %v", err)
		}
		fmt.Printf("\n%s is running.\nControl panel: %s\nLeave this window open. Press Ctrl+C to quit.\n\n", appName, url)
		app.open(url)
	case args[0] == "web" && len(args) >= 2:
		u, err := app.webURL(args[1])
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("\n%s web interface: %s\nLeave this window open. Press Ctrl+C to quit.\n\n", args[1], u)
		app.open(u)
	case args[0] == "console" && len(args) >= 3:
		pw := readPassword(fmt.Sprintf("Password for %s@%s: ", args[2], args[1]))
		if err := app.launchConsole(args[1], args[2], pw, "auto"); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Console launched. Leave this window open while you use it. Press Ctrl+C to quit.")
	default:
		usage()
		os.Exit(2)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
	case <-app.quit:
	}
	app.shutdown()
}

// readPassword reads a line from the terminal without echo.
func readPassword(prompt string) string {
	fmt.Print(prompt)
	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	_ = off.Run()
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	on := exec.Command("stty", "echo")
	on.Stdin = os.Stdin
	_ = on.Run()
	fmt.Println()
	return strings.TrimRight(line, "\r\n")
}

// openBrowser opens a URL in the desktop's default browser.
func openBrowser(url string) {
	for _, c := range [][]string{{"xdg-open", url}, {"gio", "open", url}, {"sensible-browser", url}, {"firefox", url}} {
		if _, err := exec.LookPath(c[0]); err == nil {
			cmd := exec.Command(c[0], c[1:]...)
			if cmd.Start() == nil {
				go cmd.Wait()
				return
			}
		}
	}
	fmt.Println("Open this address in your browser:", url)
}
