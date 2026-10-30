// Command noroshi runs health checks and serves a status page.
//
//	noroshi              run the server (configured through NOROSHI_* variables)
//	noroshi validate     check a config file and exit
//	noroshi healthcheck  exit 0 if the local instance is ready (for container health checks)
//	noroshi version      print the version
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "time/tzdata" // page time zones work in scratch images

	"github.com/dor1a/noroshi/internal/auth"
	"github.com/dor1a/noroshi/internal/config"
	"github.com/dor1a/noroshi/internal/metrics"
	"github.com/dor1a/noroshi/internal/notify"
	"github.com/dor1a/noroshi/internal/probe"
	"github.com/dor1a/noroshi/internal/prober"
	"github.com/dor1a/noroshi/internal/store"
	"github.com/dor1a/noroshi/internal/web"
)

var version = "dev"

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "validate":
		err = validate(os.Args[2:])
	case "healthcheck":
		err = healthcheck()
	case "version", "-version", "--version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q (use serve, validate, healthcheck, or version)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "noroshi:", err)
		os.Exit(1)
	}
}

func validate(args []string) error {
	path := os.Getenv("NOROSHI_CONFIG")
	if len(args) > 0 {
		path = args[0]
	}
	if path == "" {
		return errors.New("usage: noroshi validate <config.yaml>")
	}
	f, err := config.LoadFile(path)
	if err != nil {
		return err
	}
	pages, comps, checks := len(f.Pages), 0, 0
	for _, p := range f.Pages {
		for _, g := range p.Groups {
			comps += len(g.Components)
			for _, c := range g.Components {
				checks += len(c.Checks)
			}
		}
	}
	fmt.Printf("%s: ok (%d pages, %d components, %d checks)\n", path, pages, comps, checks)
	return nil
}

// healthcheck asks the local ops listener whether the database is reachable.
func healthcheck() error {
	addr := os.Getenv("NOROSHI_OPS_ADDR")
	if addr == "" {
		addr = ":8082"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/readyz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readyz returned %d", resp.StatusCode)
	}
	return nil
}

func serve() error {
	env, err := config.LoadEnv()
	if err != nil {
		return err
	}
	log := newLogger(env.LogLevel)
	probe.UserAgent = "noroshi/" + version
	notify.UserAgent = "noroshi/" + version

	file, err := config.LoadFile(env.ConfigPath)
	if err != nil {
		return fmt.Errorf("config %s: %w", env.ConfigPath, err)
	}
	var targets []notify.Target
	for _, n := range file.Notifiers {
		u, err := n.URL()
		if err != nil {
			return err
		}
		targets = append(targets, notify.Target{Key: n.Key, Type: n.Type, URL: u, Locale: n.Locale})
	}
	apiTokens := map[string]string{}
	for _, t := range file.APITokens {
		v, err := t.Token()
		if err != nil {
			return err
		}
		apiTokens[t.Key] = v
	}
	// Pages often share one logo, so each source is read or fetched once.
	type loaded struct {
		logo *web.Logo
		err  error
	}
	logos := map[string]loaded{}
	logo := func(src string) (*web.Logo, error) {
		l, ok := logos[src]
		if !ok {
			l.logo, l.err = loadLogo(src)
			logos[src] = l
		}
		return l.logo, l.err
	}
	pageNotify := map[string][]string{}
	pageMeta := map[string]web.PageMeta{}
	for _, p := range file.Pages {
		pageNotify[p.Key] = p.Notify
		meta := web.PageMeta{Private: p.Private()}
		if p.Logo != "" {
			if meta.Logo, err = logo(p.Logo); err != nil {
				// A missing logo should not take the status page down with it.
				log.Warn("page logo unavailable, using the default", "page", p.Key, "err", err)
				meta.Logo = web.DefaultLogo()
			}
		}
		pageMeta[p.Key] = meta
	}
	admin := web.AdminMeta{Title: file.Admin.Title, Gravatar: file.Admin.Gravatar}
	if file.Admin.Logo != "" {
		if admin.Logo, err = logo(file.Admin.Logo); err != nil {
			log.Warn("admin logo unavailable, using the default", "err", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, env.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := st.SyncConfig(ctx, file); err != nil {
		return fmt.Errorf("sync config: %w", err)
	}

	m := metrics.New(version)
	for _, p := range file.Pages {
		// Private pages are only served on the admin listener.
		base := env.BaseURL
		if p.Private() {
			base = env.AdminURL
		}
		url := ""
		if base != "" {
			url = base + "/p/" + p.Key
		}
		title := p.Title.Get(env.DefaultLocale)
		if title == "" {
			title = p.Key
		}
		m.PageInfo(p.Key, title, url)
	}
	pr := prober.New(st, m, log, prober.Settings{Locale: env.DefaultLocale, Retention: env.ResultRetention})
	a := auth.New(env, log)
	srv, err := web.New(st, a, m, pr, env, log)
	if err != nil {
		return err
	}
	srv.SetNotifiers(targets, pageNotify)
	srv.SetPages(pageMeta)
	srv.SetAdmin(admin)
	srv.SetFooter(file.Footer.ShowPoweredBy())
	srv.SetAPITokens(apiTokens)
	if env.DevNoAuth {
		log.Warn("NOROSHI_DEV_NO_AUTH is set: the admin pages require no sign-in")
	}

	servers := []*http.Server{
		{Addr: env.PublicAddr, Handler: srv.PublicHandler(), ReadHeaderTimeout: 10 * time.Second},
		{Addr: env.AdminAddr, Handler: srv.AdminHandler(), ReadHeaderTimeout: 10 * time.Second},
		{Addr: env.OpsAddr, Handler: srv.OpsHandler(), ReadHeaderTimeout: 10 * time.Second},
	}
	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func(s *http.Server) {
			log.Info("listening", "addr", s.Addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("listen %s: %w", s.Addr, err)
			}
		}(s)
	}

	proberDone := make(chan struct{})
	go func() {
		pr.Run(ctx)
		close(proberDone)
	}()

	log.Info("noroshi started", "version", version, "pages", len(file.Pages))
	select {
	case <-ctx.Done():
	case err = <-errc:
		stop()
	}

	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, s := range servers {
		s.Shutdown(sctx) //nolint:errcheck
	}
	srv.Drain(sctx)
	select {
	case <-proberDone:
	case <-sctx.Done():
	}
	return err
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}

var logoTypes = map[string]string{
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".webp": "image/webp", ".gif": "image/gif", ".ico": "image/x-icon",
}

const maxLogo = 1 << 20

// loadLogo reads a page or admin logo once at startup, from a file or an
// http(s) URL.
func loadLogo(src string) (*web.Logo, error) {
	var b []byte
	var err error
	if config.IsURL(src) {
		b, err = fetchLogo(src)
	} else {
		b, err = readLogo(src)
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	return &web.Logo{
		Data:        b,
		ContentType: logoTypes[config.LogoExt(src)],
		Version:     hex.EncodeToString(sum[:])[:10],
	}, nil
}

func readLogo(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("logo: %w", err)
	}
	if info.Size() > maxLogo {
		return nil, fmt.Errorf("logo %s is larger than 1 MiB", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("logo: %w", err)
	}
	return b, nil
}

func fetchLogo(src string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, fmt.Errorf("logo: %w", err)
	}
	req.Header.Set("User-Agent", notify.UserAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("logo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("logo %s: HTTP %d", src, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxLogo+1))
	if err != nil {
		return nil, fmt.Errorf("logo: %w", err)
	}
	if len(b) > maxLogo {
		return nil, fmt.Errorf("logo %s is larger than 1 MiB", src)
	}
	return b, nil
}
