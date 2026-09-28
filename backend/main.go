package main

import (
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed web
var web embed.FS

type App struct {
	cfg      Config
	engine   *Engine
	provider Provider
	csrf     string
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	var p Provider = newUpstox(cfg.Token)
	if cfg.Mode == "demo" {
		p = Demo{}
	}
	e, cleanup, err := newEngine(cfg, p)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()
	a := App{cfg: cfg, engine: e, provider: p}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		e.Refresh(ctx)
		ticker := time.NewTicker(cfg.Poll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.Refresh(ctx)
			}
		}
	}()
	server := &http.Server{Addr: "127.0.0.1:" + cfg.Port, Handler: a.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
	}()
	log.Printf("Optiondesk running at http://%s · %s data · broker access GET only", server.Addr, cfg.Mode)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Print(err)
	}
}
func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"mode": a.cfg.Mode, "connected": a.cfg.Token != "" || a.cfg.Mode == "demo", "pollSeconds": int(a.cfg.Poll.Seconds()), "marginPerLot": float64(a.cfg.Margin) / 100, "feePerOrder": float64(a.cfg.Fee) / 100})
	})
	mux.HandleFunc("GET /api/account", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, a.engine.Snapshot()) })
	mux.HandleFunc("GET /api/contracts", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("underlying")
		if !validKey(key) {
			fail(w, 400, "invalid underlying")
			return
		}
		cs, err := a.provider.Contracts(r.Context(), key)
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		a.engine.register(cs)
		exp := map[string]bool{}
		for _, c := range cs {
			exp[c.Expiry] = true
		}
		dates := []string{}
		for d := range exp {
			dates = append(dates, d)
		}
		sort.Strings(dates)
		respond(w, 200, map[string]any{"contracts": cs, "expiries": dates})
	})
	mux.HandleFunc("GET /api/chain", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("underlying")
		expiry := r.URL.Query().Get("expiry")
		if !validKey(key) {
			fail(w, 400, "invalid underlying")
			return
		}
		if _, err := time.Parse("2006-01-02", expiry); err != nil {
			fail(w, 400, "invalid expiry")
			return
		}
		rows, err := a.provider.Chain(r.Context(), key, expiry)
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		ex := "NSE"
		if strings.HasPrefix(key, "BSE") {
			ex = "BSE"
		}
		status, err := a.provider.Status(r.Context(), ex)
		statusError := ""
		if err != nil {
			status = "UNKNOWN"
			statusError = err.Error()
		}
		respond(w, 200, map[string]any{"rows": rows, "status": status, "statusError": statusError, "receivedAt": time.Now()})
	})
	mux.HandleFunc("GET /api/candles", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if !validKey(key) {
			fail(w, 400, "invalid instrument")
			return
		}
		cs, err := a.provider.Candles(r.Context(), key)
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		respond(w, 200, cs)
	})
	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if len(q) < 2 || len(q) > 60 {
			fail(w, 400, "search must be 2..60 characters")
			return
		}
		cs, err := a.provider.Search(r.Context(), q)
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		respond(w, 200, cs)
	})
	mux.HandleFunc("POST /api/orders", func(w http.ResponseWriter, r *http.Request) {
		var req OrderRequest
		if err := decode(w, r, &req); err != nil {
			fail(w, 400, err.Error())
			return
		}
		o, err := a.engine.Submit(r.Context(), req)
		if err != nil {
			fail(w, 422, err.Error())
			return
		}
		respond(w, 200, o)
	})
	mux.HandleFunc("POST /api/orders/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		o, err := a.engine.Cancel(r.PathValue("id"))
		if err != nil {
			fail(w, 422, err.Error())
			return
		}
		respond(w, 200, o)
	})
	mux.HandleFunc("GET /api/export", func(w http.ResponseWriter, r *http.Request) {
		a.engine.mu.Lock()
		orders := append([]Order{}, a.engine.state.Orders...)
		a.engine.mu.Unlock()
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=optiondesk-trades.csv")
		c := csv.NewWriter(w)
		defer c.Flush()
		_ = c.Write([]string{"ID", "Symbol", "Expiry", "Side", "Type", "Quantity", "Limit INR", "Fill INR", "Fee INR", "Status", "Created IST"})
		for _, o := range orders {
			_ = c.Write([]string{csvSafe(o.ID), csvSafe(o.Contract.Symbol), o.Contract.Expiry, o.Side, o.Kind, strconv.Itoa(o.Qty), fmt.Sprintf("%.2f", float64(o.Limit)/100), fmt.Sprintf("%.2f", float64(o.Fill)/100), fmt.Sprintf("%.2f", float64(o.Fee)/100), o.Status, o.Created.In(ist).Format(time.RFC3339)})
		}
	})
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "endpoint not found") })
	files, _ := fs.Sub(web, "web")
	mux.Handle("GET /", http.FileServer(http.FS(files)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "localhost" && host != "127.0.0.1" && host != "[::1]" {
			fail(w, 403, "local access only")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || (u.Host != r.Host && u.Host != "127.0.0.1:5173" && u.Host != "localhost:5173") {
				fail(w, 403, "cross-origin requests are blocked")
				return
			}
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if r.Header.Get("X-Optiondesk-Request") != "paper" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				fail(w, 403, "expected a local paper-trading request")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func csvSafe(s string) string {
	if len(s) > 0 && strings.ContainsAny(s[:1], "=+-@\t\r") {
		return "'" + s
	}
	return s
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	respond(w, status, map[string]string{"error": msg})
}
