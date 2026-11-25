import java.applet.Applet;
import java.applet.AppletContext;
import java.applet.AppletStub;
import java.applet.AudioClip;
import java.awt.BorderLayout;
import java.awt.Desktop;
import java.awt.Dimension;
import java.awt.EventQueue;
import java.awt.Frame;
import java.awt.Image;
import java.awt.Toolkit;
import java.awt.Window;
import java.awt.event.WindowAdapter;
import java.awt.event.WindowEvent;
import java.io.FileInputStream;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.Reader;
import java.net.CookieHandler;
import java.net.CookieManager;
import java.net.CookiePolicy;
import java.net.HttpCookie;
import java.net.Socket;
import java.net.URI;
import java.net.URL;
import java.net.URLClassLoader;
import java.security.SecureRandom;
import java.security.Security;
import java.security.cert.X509Certificate;
import java.util.Collections;
import java.util.Enumeration;
import java.util.HashMap;
import java.util.Iterator;
import java.util.Map;
import java.util.Properties;
import javax.net.ssl.HostnameVerifier;
import javax.net.ssl.HttpsURLConnection;
import javax.net.ssl.SSLContext;
import javax.net.ssl.SSLEngine;
import javax.net.ssl.TrustManager;
import javax.net.ssl.X509ExtendedTrustManager;
import javax.swing.JFrame;
import javax.swing.Timer;
import javax.swing.WindowConstants;

/**
 * Runs an iLO console applet (served by the iLO itself) outside a browser.
 *
 * Usage: java -cp applethost.jar LegacyAppletHost launch.properties
 *
 * Properties:
 *   jar=/path/to/downloaded.jar     class=com.example.Applet
 *   codebase=https://ilo/           documentbase=https://ilo/page.html
 *   title=...   width=1060  height=850
 *   proxy.host=127.0.0.1  proxy.port=NNNN   (HTTPS from the applet goes through the legacy TLS relay)
 *   cookie.N=name=value
 *   param.NAME=value
 */
public class LegacyAppletHost {

    public static void main(String[] args) throws Exception {
        if (args.length < 1) {
            System.err.println("usage: LegacyAppletHost launch.properties");
            System.exit(2);
        }
        final Properties p = new Properties();
        try (Reader r = new InputStreamReader(new FileInputStream(args[0]), "UTF-8")) {
            p.load(r);
        }

        relaxTls();

        String proxyHost = p.getProperty("proxy.host");
        String proxyPort = p.getProperty("proxy.port");
        if (proxyHost != null && proxyPort != null) {
            System.setProperty("https.proxyHost", proxyHost);
            System.setProperty("https.proxyPort", proxyPort);
            System.setProperty("http.nonProxyHosts", "");
        }

        final URL codebase = new URL(p.getProperty("codebase"));
        final URL documentbase = new URL(p.getProperty("documentbase", p.getProperty("codebase")));

        CookieManager cm = new CookieManager(null, CookiePolicy.ACCEPT_ALL);
        CookieHandler.setDefault(cm);
        for (String key : p.stringPropertyNames()) {
            if (!key.startsWith("cookie.")) continue;
            String nv = p.getProperty(key);
            int eq = nv.indexOf('=');
            if (eq <= 0) continue;
            HttpCookie c = new HttpCookie(nv.substring(0, eq), nv.substring(eq + 1));
            c.setPath("/");
            c.setVersion(0);
            c.setDomain(codebase.getHost());
            cm.getCookieStore().add(new URI(codebase.getProtocol(), codebase.getHost(), "/", null), c);
        }

        final Map<String, String> params = new HashMap<String, String>();
        for (String key : p.stringPropertyNames()) {
            if (key.startsWith("param.")) params.put(key.substring(6), p.getProperty(key));
        }

        String cls = p.getProperty("class");
        if (cls.endsWith(".class")) cls = cls.substring(0, cls.length() - 6);
        URL jarUrl = new java.io.File(p.getProperty("jar")).toURI().toURL();
        final URLClassLoader loader = new URLClassLoader(new URL[]{jarUrl}, LegacyAppletHost.class.getClassLoader());
        Thread.currentThread().setContextClassLoader(loader);

        final Applet applet = (Applet) loader.loadClass(cls).getDeclaredConstructor().newInstance();
        final int width = Integer.parseInt(p.getProperty("width", "1060"));
        final int height = Integer.parseInt(p.getProperty("height", "850"));
        final boolean tiny = width <= 10 || height <= 10;   // applet opens its own window (iLO 3)
        final String title = p.getProperty("title", "iLO Remote Console");

        EventQueue.invokeAndWait(new Runnable() {
            public void run() {
                Thread.currentThread().setContextClassLoader(loader);
                final JFrame frame = new JFrame(title);
                frame.setDefaultCloseOperation(WindowConstants.DO_NOTHING_ON_CLOSE);
                frame.getContentPane().setLayout(new BorderLayout());
                frame.getContentPane().add(applet, BorderLayout.CENTER);
                applet.setStub(new Stub(applet, frame, codebase, documentbase, params));
                if (tiny) {
                    frame.setUndecorated(true);
                    frame.setSize(1, 1);
                    frame.setLocation(-50, -50);
                    frame.setType(Window.Type.UTILITY);
                } else {
                    applet.setPreferredSize(new Dimension(width, height));
                    frame.pack();
                    frame.setLocationRelativeTo(null);
                }
                frame.addWindowListener(new WindowAdapter() {
                    @Override
                    public void windowClosing(WindowEvent e) {
                        shutdown(applet);
                    }
                });
                frame.setVisible(true);
                applet.init();
                applet.start();
                frame.validate();

                if (tiny) {
                    // Exit once the applet's own window(s) have been opened and then all closed
                    final boolean[] seen = {false};
                    Timer t = new Timer(1500, null);
                    t.addActionListener(e -> {
                        int visible = 0;
                        for (Window w : Window.getWindows()) {
                            if (w != frame && w.isShowing()) visible++;
                        }
                        if (visible > 0) seen[0] = true;
                        else if (seen[0]) shutdown(applet);
                    });
                    t.start();
                }
            }
        });
    }

    private static void shutdown(Applet applet) {
        try { applet.stop(); } catch (Throwable ignored) { }
        try { applet.destroy(); } catch (Throwable ignored) { }
        System.exit(0);
    }

    /** iLO 2/3 only speak TLS 1.0/1.1 with old ciphers and self-signed certificates. */
    private static void relaxTls() {
        Security.setProperty("jdk.tls.disabledAlgorithms", "");
        Security.setProperty("jdk.certpath.disabledAlgorithms", "");
        System.setProperty("jdk.tls.client.protocols", "TLSv1,TLSv1.1,TLSv1.2");
        System.setProperty("https.protocols", "TLSv1,TLSv1.1,TLSv1.2");
        try {
            TrustManager[] trustAll = new TrustManager[]{new X509ExtendedTrustManager() {
                public void checkClientTrusted(X509Certificate[] c, String a) { }
                public void checkServerTrusted(X509Certificate[] c, String a) { }
                public void checkClientTrusted(X509Certificate[] c, String a, Socket s) { }
                public void checkServerTrusted(X509Certificate[] c, String a, Socket s) { }
                public void checkClientTrusted(X509Certificate[] c, String a, SSLEngine e) { }
                public void checkServerTrusted(X509Certificate[] c, String a, SSLEngine e) { }
                public X509Certificate[] getAcceptedIssuers() { return new X509Certificate[0]; }
            }};
            SSLContext ctx = SSLContext.getInstance("TLS");
            ctx.init(null, trustAll, new SecureRandom());
            SSLContext.setDefault(ctx);
            HttpsURLConnection.setDefaultSSLSocketFactory(ctx.getSocketFactory());
            HttpsURLConnection.setDefaultHostnameVerifier(new HostnameVerifier() {
                public boolean verify(String h, javax.net.ssl.SSLSession s) { return true; }
            });
        } catch (Exception e) {
            System.err.println("TLS setup: " + e);
        }
    }

    static final class Stub implements AppletStub, AppletContext {
        private final Applet applet;
        private final Frame frame;
        private final URL codebase, documentbase;
        private final Map<String, String> params;

        Stub(Applet applet, Frame frame, URL codebase, URL documentbase, Map<String, String> params) {
            this.applet = applet;
            this.frame = frame;
            this.codebase = codebase;
            this.documentbase = documentbase;
            this.params = params;
        }

        // AppletStub
        public boolean isActive() { return true; }
        public URL getDocumentBase() { return documentbase; }
        public URL getCodeBase() { return codebase; }
        public String getParameter(String name) {
            String v = params.get(name);
            if (v == null) {
                for (Map.Entry<String, String> e : params.entrySet()) {
                    if (e.getKey().equalsIgnoreCase(name)) return e.getValue();
                }
            }
            return v;
        }
        public AppletContext getAppletContext() { return this; }
        public void appletResize(int w, int h) {
            if (frame.isUndecorated()) return;
            applet.setPreferredSize(new Dimension(w, h));
            frame.pack();
        }

        // AppletContext
        public AudioClip getAudioClip(URL url) { return Applet.newAudioClip(url); }
        public Image getImage(URL url) { return Toolkit.getDefaultToolkit().createImage(url); }
        public Applet getApplet(String name) { return null; }
        public Enumeration<Applet> getApplets() { return Collections.enumeration(Collections.singletonList(applet)); }
        public void showDocument(URL url) { showDocument(url, "_self"); }
        public void showDocument(URL url, String target) {
            System.out.println("showDocument: " + url + " (" + target + ")");
            if (url != null && url.getProtocol().startsWith("http")) {
                try { Desktop.getDesktop().browse(url.toURI()); } catch (Throwable ignored) { }
            }
        }
        public void showStatus(String status) { System.out.println("status: " + status); }
        public void setStream(String key, InputStream stream) { }
        public InputStream getStream(String key) { return null; }
        public Iterator<String> getStreamKeys() { return Collections.<String>emptyList().iterator(); }
    }
}
