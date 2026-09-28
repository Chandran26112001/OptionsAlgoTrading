package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixtureProvider struct {
	Demo
	q      Quote
	status string
}

func (f *fixtureProvider) Quotes(_ context.Context, keys []string) (map[string]Quote, error) {
	out := map[string]Quote{}
	for _, k := range keys {
		q := f.q
		q.Key = k
		out[k] = q
	}
	return out, nil
}
func (f *fixtureProvider) Status(context.Context, string) (string, error) { return f.status, nil }
func fixture(t *testing.T) (*Engine, *fixtureProvider, Contract) {
	t.Helper()
	cfg := Config{Mode: "demo", DataDir: t.TempDir(), Capital: money(1000000), Margin: money(150000), Fee: money(20)}
	p := &fixtureProvider{q: Quote{LTP: 100, Bid: 99, Ask: 101, BidQty: 10000, AskQty: 10000, At: time.Now()}, status: "NORMAL_OPEN"}
	e, cleanup, err := newEngine(cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	c := Contract{Key: "NSE_FO|1234", Symbol: "TEST 25000 CE", Underlying: "NSE_INDEX|Nifty 50", Exchange: "NSE", Expiry: time.Now().Add(7 * 24 * time.Hour).Format("2006-01-02"), Type: "CE", Lot: 25, Tick: 5}
	e.register([]Contract{c})
	return e, p, c
}
func request(c Contract, id, side string, lots int) OrderRequest {
	return OrderRequest{ID: id, Key: c.Key, Underlying: c.Underlying, Side: side, Kind: "MARKET", Lots: lots}
}
func TestLongPartialCloseAndPersistence(t *testing.T) {
	e, p, c := fixture(t)
	ctx := context.Background()
	if _, err := e.Submit(ctx, request(c, "buy-order-1", "BUY", 2)); err != nil {
		t.Fatal(err)
	}
	p.q.Bid = 120
	p.q.Ask = 122
	p.q.LTP = 121
	if _, err := e.Submit(ctx, request(c, "sell-order-1", "SELL", 1)); err != nil {
		t.Fatal(err)
	}
	pos := e.state.Positions[c.Key]
	if pos.Qty != 25 || pos.Cost != money(2525) {
		t.Fatalf("wrong remaining basis: %+v", pos)
	}
	if e.state.Realized != money(475) || e.state.Fees != money(40) {
		t.Fatalf("realized=%d fees=%d", e.state.Realized, e.state.Fees)
	}
	if e.state.Cash != money(997910) {
		t.Fatalf("cash=%d", e.state.Cash)
	}
	raw, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Ledger
	if err = json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Cash != e.state.Cash || len(persisted.Orders) != 2 {
		t.Fatal("state not persisted")
	}
	snap := e.Snapshot()
	if snap["totalPnL"].(float64) != 935 {
		t.Fatalf("incorrect net P&L: %v", snap["totalPnL"])
	}
}
func TestShortCoverAndFlip(t *testing.T) {
	e, p, c := fixture(t)
	ctx := context.Background()
	if _, err := e.Submit(ctx, request(c, "short-order-1", "SELL", 1)); err != nil {
		t.Fatal(err)
	}
	if e.margin(e.state) != money(150000) {
		t.Fatal("short reserve missing")
	}
	p.q.Bid = 79
	p.q.Ask = 80
	p.q.LTP = 80
	if _, err := e.Submit(ctx, request(c, "cover-and-flip", "BUY", 2)); err != nil {
		t.Fatal(err)
	}
	pos := e.state.Positions[c.Key]
	if pos.Qty != 25 || pos.Cost != money(2000) || e.state.Realized != money(475) || e.margin(e.state) != 0 {
		t.Fatalf("invalid flip: %+v", e.state)
	}
}
func TestWeightedAverageAndExactClose(t *testing.T) {
	e, p, c := fixture(t)
	ctx := context.Background()
	_, _ = e.Submit(ctx, request(c, "first-buy", "BUY", 1))
	p.q.Ask = 103
	_, _ = e.Submit(ctx, request(c, "second-buy", "BUY", 1))
	p.q.Bid = 110
	p.q.Ask = 111
	if _, err := e.Submit(ctx, request(c, "full-close", "SELL", 2)); err != nil {
		t.Fatal(err)
	}
	if len(e.state.Positions) != 0 || e.state.Realized != money(400) {
		t.Fatalf("basis did not close correctly: %+v", e.state)
	}
}
func TestInsufficientFundsNoMutation(t *testing.T) {
	e, _, c := fixture(t)
	before := e.state.Cash
	_, err := e.Submit(context.Background(), request(c, "too-many-shorts", "SELL", 10))
	if err == nil || e.state.Cash != before || len(e.state.Orders) != 0 {
		t.Fatal("invalid short changed account")
	}
}
func TestStaleAndMissingDepthRejected(t *testing.T) {
	for _, mode := range []string{"stale", "future", "depth", "crossed", "closed", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			e, p, c := fixture(t)
			switch mode {
			case "stale":
				p.q.At = time.Now().Add(-time.Minute)
			case "future":
				p.q.At = time.Now().Add(time.Minute)
			case "depth":
				p.q.AskQty = 1
			case "crossed":
				p.q.Bid = 110
			case "closed":
				p.status = "CLOSED"
			case "expiry":
				c.Expiry = "2020-01-01"
				e.register([]Contract{c})
			}
			if _, err := e.Submit(context.Background(), request(c, "rejected-market", "BUY", 1)); err == nil {
				t.Fatal("unsafe fill accepted")
			}
			if len(e.state.Orders) > 0 {
				t.Fatal("rejected request mutated ledger")
			}
		})
	}
}
func TestLimitReservationCancelAndFill(t *testing.T) {
	e, p, c := fixture(t)
	ctx := context.Background()
	r := request(c, "limit-order-1", "BUY", 1)
	r.Kind = "LIMIT"
	r.Limit = 90
	o, err := e.Submit(ctx, r)
	if err != nil || o.Status != "PENDING" {
		t.Fatalf("%+v %v", o, err)
	}
	if e.reserved(e.state, "") != money(2270) {
		t.Fatal("limit reserve missing")
	}
	if _, err = e.Cancel(o.ID); err != nil {
		t.Fatal(err)
	}
	if e.reserved(e.state, "") != 0 {
		t.Fatal("cancel did not release reserve")
	}
	r.ID = "limit-order-2"
	_, err = e.Submit(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	p.q.Ask = 89
	p.q.Bid = 88
	p.q.LTP = 89
	e.Refresh(ctx)
	if e.state.Orders[1].Status != "FILLED" || e.state.Orders[1].Fill != money(89) {
		t.Fatal("limit did not fill at improved ask")
	}
}
func TestTickValidation(t *testing.T) {
	e, _, c := fixture(t)
	r := request(c, "bad-tick-price", "BUY", 1)
	r.Kind = "LIMIT"
	r.Limit = 90.03
	if _, err := e.Submit(context.Background(), r); err == nil {
		t.Fatal("off-tick limit accepted")
	}
}
func TestPendingSurvivesRestartAndFills(t *testing.T) {
	e, p, c := fixture(t)
	r := request(c, "persisted-limit", "BUY", 1)
	r.Kind = "LIMIT"
	r.Limit = 90
	_, err := e.Submit(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(e.path + ".lock"); err != nil {
		t.Fatal(err)
	}
	reloaded, cleanup, err := newEngine(e.cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	p.q.Bid = 88
	p.q.Ask = 89
	reloaded.Refresh(context.Background())
	if reloaded.state.Orders[0].Status != "FILLED" {
		t.Fatal("restored limit did not fill")
	}
}
func TestConcurrentIdempotency(t *testing.T) {
	e, _, c := fixture(t)
	r := request(c, "same-client-order", "BUY", 1)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Submit(context.Background(), r); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(e.state.Orders) != 1 || e.state.Positions[c.Key].Qty != 25 {
		t.Fatal("duplicate filled more than once")
	}
	r.Lots = 2
	if _, err := e.Submit(context.Background(), r); err == nil {
		t.Fatal("conflicting idempotency key accepted")
	}
}
func TestWriteFailureRollsBack(t *testing.T) {
	e, _, c := fixture(t)
	e.path = filepath.Join(t.TempDir(), "does-not-exist", "state.json")
	before := e.state.Cash
	if _, err := e.Submit(context.Background(), request(c, "failed-persistence", "BUY", 1)); err == nil {
		t.Fatal("write should fail")
	}
	if e.state.Cash != before || len(e.state.Orders) != 0 {
		t.Fatal("failed write changed in-memory balance")
	}
}
func TestLedgerLockAndCorruption(t *testing.T) {
	e, p, _ := fixture(t)
	if _, cleanup, err := newEngine(e.cfg, p); err == nil {
		cleanup()
		t.Fatal("second writer accepted")
	}
	_ = os.Remove(e.path + ".lock")
	_ = os.WriteFile(e.path, []byte("{broken"), 0600)
	if _, cleanup, err := newEngine(e.cfg, p); err == nil {
		cleanup()
		t.Fatal("corrupt ledger silently reset")
	}
}
func TestExpiredDayOrder(t *testing.T) {
	e, _, c := fixture(t)
	r := request(c, "day-limit-order", "BUY", 1)
	r.Kind = "LIMIT"
	r.Limit = 10
	_, err := e.Submit(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	e.cfg.Mode = "live"
	e.state.Orders[0].Created = time.Now().Add(-48 * time.Hour)
	e.Refresh(context.Background())
	if e.state.Orders[0].Status != "EXPIRED" {
		t.Fatal("DAY order persisted past its day")
	}
}
func TestStaleMarksAreDisclosed(t *testing.T) {
	e, p, c := fixture(t)
	_, _ = e.Submit(context.Background(), request(c, "mark-test-buy", "BUY", 1))
	p.q.At = time.Now().Add(-time.Minute)
	e.Refresh(context.Background())
	s := e.Snapshot()
	if s["marksFresh"].(bool) {
		t.Fatal("stale marks reported fresh")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestProviderOnlyReadOnlyEndpoints(t *testing.T) {
	u := newUpstox("test-secret")
	calls := 0
	u.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Host != "api.upstox.com" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Fatal("unexpected outbound request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"success","data":[]}`)), Header: make(http.Header)}, nil
	})
	var out []Contract
	if err := u.get(context.Background(), "/v2/option/contract", nil, 0, &out); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v2/order/place", "/v3/order/place", "/v2/logout", "/v2/user/kill-switch", "/v3/historical-candle/intraday/../../order/place/minutes/1"} {
		if err := u.get(context.Background(), path, nil, 0, &out); err == nil {
			t.Fatalf("allowed %s", path)
		}
	}
	if calls != 1 {
		t.Fatal("unsafe endpoint reached transport")
	}
}
func TestProviderResponseParsing(t *testing.T) {
	u := newUpstox("test")
	u.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"status":"success","data":{"NSE_FO:TEST":{"instrument_token":"NSE_FO|1234","last_price":101,"timestamp":"2026-09-28T10:00:00+05:30","depth":{"buy":[{"price":100,"quantity":75}],"sell":[{"price":102,"quantity":50}]}}}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	qs, err := u.Quotes(context.Background(), []string{"NSE_FO|1234"})
	if err != nil {
		t.Fatal(err)
	}
	q := qs["NSE_FO|1234"]
	if q.Bid != 100 || q.Ask != 102 || q.AskQty != 50 || q.At.IsZero() {
		t.Fatalf("bad quote: %+v", q)
	}
}
func TestHTTPRejectsCrossOriginAndBrokerWrites(t *testing.T) {
	e, p, _ := fixture(t)
	a := App{cfg: e.cfg, engine: e, provider: p}
	h := a.routes()
	for _, test := range []struct{ path, host, origin string }{{"/api/orders", "localhost:8080", "https://evil.example"}, {"/api/orders", "attacker.example", ""}, {"/api/orders", "localhost:8080", ""}} {
		r := httptest.NewRequest("POST", test.path, strings.NewReader(`{}`))
		r.Host = test.host
		r.Header.Set("Origin", test.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("unsafe request accepted: %d", w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/api/config", nil)
	r.Host = "localhost:8080"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "token") {
		t.Fatal("config unavailable or leaks token")
	}
}
func TestAllProviderEndpointsExistAsGETInAttachedSpec(t *testing.T) {
	b, err := os.ReadFile("../OpenAPI_Spec.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err = json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/v2/option/contract", "/v2/option/chain", "/v2/market-quote/quotes", "/v2/instruments/search", "/v2/market/status/{exchange}", "/v3/historical-candle/intraday/{instrumentKey}/{unit}/{interval}"} {
		if len(spec.Paths[p]["get"]) == 0 {
			t.Fatalf("endpoint missing in supplied spec: GET %s", p)
		}
	}
}
