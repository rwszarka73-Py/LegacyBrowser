LEGACY BROWSER FOR LINUX 1.0
============================

Opens old HP iLO 2 / iLO 3 web interfaces (and other devices that only speak
TLS 1.0 or 1.1) in your normal browser - Firefox, Chrome, anything - and
launches the iLO Java remote console without a browser plugin.

Everything is in one program file. Nothing on your system is changed: no
browser settings, no system TLS settings, no root needed.


QUICK START
-----------
1. Make it executable and run it:
     chmod +x legacybrowser
     ./legacybrowser
2. The control panel opens in your browser.
3. Type the iLO's address under Quick connect and click "Open web interface",
   or click "Add server" to save it.
4. Click "Console" (or "Remote console") for the server's screen.

Leave the terminal window open while you use it (or use the desktop launcher,
below). Quit from the panel's Quit button or Ctrl+C in the terminal.


REQUIREMENTS
------------
- 64-bit Linux with a desktop (X11, or Wayland with XWayland - standard on
  Ubuntu, Fedora, Mint, Debian, etc.). Builds for x86-64 and ARM64.
- For the remote console only: Java 8 to 14. If you don't have one, Legacy
  Browser downloads Java 11 automatically the first time you open a console
  (about 45 MB, from adoptium.net, stored in ~/.local/share/legacybrowser).
  Newer Java versions are skipped on purpose: the iLO consoles are Java
  applets and don't work on Java 15+.
  To use a specific Java instead:  export LEGACYBROWSER_JAVA=/path/to/bin/java


WEB INTERFACE
-------------
Each device gets its own local address like http://127.0.0.1:41235/ that
Legacy Browser connects to the device with the old TLS it needs. Bookmarks to
that address only work while Legacy Browser is running, and the port number
changes each time it starts - open devices from the control panel instead.


REMOTE CONSOLE
--------------
iLO 2 (G5/G6 servers): the Java Remote Console.
iLO 3 (G7 servers):    the Java Integrated Remote Console.

Legacy Browser logs in to the iLO with your user name and password, downloads
the console program from the iLO itself, and runs it in its own window. The
console connects directly to the iLO (iLO 2: port 23, iLO 3: port 17990).

The iLO type is detected automatically. If detection fails, edit the server
and choose iLO 2 or iLO 3.

Notes:
- The remote console needs an iLO license that includes it (iLO Advanced on
  most models; iLO 3 standard allows only text/pre-OS screens).
- The iLO 3 console needs reasonably current iLO 3 firmware (1.50 or later
  for the login it uses; 1.94 is the latest).
- Each console uses an iLO session. If the iLO says no sessions are available,
  log out of the web interface or wait a few minutes.


COMMAND LINE
------------
  legacybrowser                        Control panel
  legacybrowser web 192.168.1.50       Open just that device's web interface
  legacybrowser console 192.168.1.50 Administrator
                                       Launch the console (asks for password)
  -nobrowser                           Print addresses instead of opening a browser
  -port 8470                           Use a fixed port for the control panel


DESKTOP LAUNCHER (optional)
---------------------------
  mkdir -p ~/.local/bin ~/.local/share/applications ~/.local/share/icons
  cp legacybrowser ~/.local/bin/
  cp legacybrowser.png ~/.local/share/icons/
  cp legacybrowser.desktop ~/.local/share/applications/
Legacy Browser then appears in your applications menu.


FILES
-----
~/.config/legacybrowser/servers.json      Saved servers (permissions 600).
                                          Passwords are only stored if you
                                          check "Remember password", in plain
                                          text, readable only by your user.
~/.local/share/legacybrowser/             Activity log, console logs, Java 11
~/.cache/legacybrowser/                   Downloaded console programs


TROUBLESHOOTING
---------------
The Activity box in the control panel shows each step. More detail is in
~/.local/share/legacybrowser/legacybrowser.log, and each console's Java output
is in ~/.local/share/legacybrowser/console-<address>.log.

"TLS handshake ... failed"   Check the address. Legacy Browser already tries
                             TLS 1.2, 1.1 and 1.0 with old ciphers.
"login failed"               Check user name/password. iLO 2 adds a delay
                             after failed logins - wait a minute.
Console window doesn't open  See the console-<address>.log file.


SECURITY
--------
TLS 1.0/1.1 and these iLO firmwares are outdated. Use Legacy Browser only for
your own devices on your own network. The control panel only listens on
127.0.0.1 and requires the private link it opens with.


UNINSTALL
---------
Delete the legacybrowser file, and optionally:
  rm -r ~/.config/legacybrowser ~/.local/share/legacybrowser ~/.cache/legacybrowser
  rm ~/.local/share/applications/legacybrowser.desktop ~/.local/share/icons/legacybrowser.png
