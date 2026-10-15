package main

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAlgoComparisons(t *testing.T) {
	rows := []ChainRow{}
	for i := 0; i < 5; i++ {
		rows = append(rows, ChainRow{Strike: float64(100 + i*10), Spot: 121, Call: Leg{Key: "CE", Market: MarketData{OI: float64(i + 1)}, Greeks: Greeks{IV: 10 + float64(i)}}, Put: Leg{Key: "PE", Market: MarketData{OI: float64((i + 1) * 2)}, Greeks: Greeks{IV: 13 + float64(i)}}})
	}
	// ATM 120, range 110..130; includes ATM exactly once.
	oi, iv := compareChain(rows, 1, false), compareChain(rows, 1, true)
	if oi.Call == nil || *oi.Call != 9 || *oi.Put != 18 || oi.Leader != "PE" || oi.Count != 3 || oi.Low != 110 || oi.High != 130 {
		t.Fatalf("incorrect OI: %+v", oi)
	}
	if iv.Call == nil || *iv.Call != 12 || *iv.Put != 15 || *iv.Difference != 3 {
		t.Fatalf("incorrect IV: %+v", iv)
	}
	if got := compareChain(rows, 3, false); got.Call != nil || got.Issue == "" {
		t.Fatal("incomplete range accepted")
	}
	rows[2].Put.Greeks.IV = 0
	if got := compareChain(rows, 1, true); got.Call != nil {
		t.Fatal("missing IV accepted")
	}
	rows[2].Put.Market.MissingOI = true
	if got := compareChain(rows, 1, false); got.Call != nil {
		t.Fatal("missing OI accepted")
	}
}

func TestAlgoMissingOIAndCandleVolumeParsing(t *testing.T) {
	for _, body := range []string{`{"instrument_key":"CE"}`, `{"instrument_key":"CE","market_data":{}}`, `{"instrument_key":"CE","market_data":{"oi":null}}`} {
		var l Leg
		if err := json.Unmarshal([]byte(body), &l); err != nil {
			t.Fatal(err)
		}
		if !l.Market.MissingOI {
			t.Fatal("missing OI treated as zero")
		}
	}
	var l Leg
	_ = json.Unmarshal([]byte(`{"instrument_key":"CE","market_data":{"oi":0}}`), &l)
	if l.Market.MissingOI {
		t.Fatal("valid zero OI rejected")
	}
	u := newUpstox("test")
	u.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"success","data":{"candles":[["2026-09-30T10:01:00+05:30",10,12,9,11,25],["2026-09-30T10:00:00+05:30",10,12,9,11,null]]}}`))}, nil
	})
	cs, err := u.Candles(context.Background(), "NSE_EQ|TEST")
	if err != nil || len(cs) != 2 || !cs[0].MissingVolume || cs[1].Volume != 25 {
		t.Fatalf("candle volume parsing: %+v %v", cs, err)
	}
	if technicals(cs, 2, 2, 3, .02, .2).VWAP != nil {
		t.Fatal("partial volume history produced VWAP")
	}
}

func TestTechnicalsKnownValues(t *testing.T) {
	cs := []Candle{{Time: 60, Open: 10, High: 12, Low: 8, Close: 10, Volume: 10}, {Time: 120, Open: 12, High: 14, Low: 10, Close: 12, Volume: 30}, {Time: 180, Open: 14, High: 16, Low: 12, Close: 14, Volume: 10}, {Time: 240, Open: 16, High: 18, Low: 14, Close: 16, Volume: 50}}
	got := technicals(cs, 3, 2, 1, .02, .2)
	if got.VWAP == nil || math.Abs(*got.VWAP-14) > 1e-9 {
		t.Fatalf("VWAP: %v", got.VWAP)
	}
	if got.EMA == nil || *got.EMA != 14 {
		t.Fatalf("EMA: %v", got.EMA)
	}
	// TR=4 throughout; ATR=4. Supertrend starts at 16, stays 16 until crossed.
	if got.Supertrend == nil || *got.Supertrend != 16 {
		t.Fatalf("supertrend %+v", got.Supertrend)
	}
	if got.SAR == nil || math.Abs(*got.SAR-8.32) > 1e-9 {
		t.Fatalf("SAR %v", got.SAR)
	}
	for i := range cs {
		cs[i].Volume = 0
	}
	if technicals(cs, 3, 2, 1, .02, .2).VWAP != nil {
		t.Fatal("invented index VWAP")
	}
	got = technicals(cs[:1], 3, 2, 1, .02, .2)
	if got.EMA != nil || got.Supertrend != nil || got.SAR != nil {
		t.Fatal("insufficient history accepted")
	}
}

func TestCompletedCandleAndTrendFreshness(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 30, 0, ist)
	cs := []Candle{}
	for i := 6; i >= 0; i-- {
		cs = append(cs, Candle{Time: now.Truncate(time.Minute).Add(-time.Duration(i) * time.Minute).Unix(), Open: 100, High: 103, Low: 99, Close: 102})
	}
	clean, issue := completedCandles(cs, now)
	if issue != "" || len(clean) != 6 {
		t.Fatalf("unfinished candle used: %s %d", issue, len(clean))
	}
	cfg := AlgoConfig{Lookback: 5, MaxAge: 180}
	got := trendFrom(Quote{LTP: 101, At: now}, cs, cfg, now)
	if got.Direction != "Down" || got.Change == nil || *got.Change != -1 || got.PreviousAt != now.Truncate(time.Minute).Add(-5*time.Minute).Unix() {
		t.Fatalf("incorrect lookback %+v", got)
	}
	if trendFrom(Quote{LTP: 101, At: now.Add(-time.Hour)}, cs, cfg, now).Change != nil {
		t.Fatal("stale quote has trend")
	}
	if trendFrom(Quote{LTP: 101, At: now}, nil, cfg, now).Change != nil {
		t.Fatal("missing lookback fabricated")
	}
	cs = append(cs[:2], cs[3:]...)
	if _, issue = completedCandles(cs, now); issue == "" {
		t.Fatal("gap ignored")
	}
}

func TestAlgoReadOnlyAndAlwaysWait(t *testing.T) {
	cfg, err := loadAlgoConfig()
	if err != nil {
		t.Fatal(err)
	}
	e, _, _ := fixture(t)
	a := App{cfg: Config{Mode: "demo", Algo: cfg}, engine: e, provider: Demo{}}
	cs, _ := a.provider.Contracts(context.Background(), "NSE_INDEX|Nifty 50")
	before := e.Snapshot()
	r := httptest.NewRequest("GET", "/api/algo?underlying=NSE_INDEX%7CNifty%2050&expiry="+cs[0].Expiry+"&option="+cs[0].Key, nil)
	r.Host = "localhost:8080"
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var result AlgoDashboard
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.FinalCall != "WAIT" || result.FinalReason == "" || result.Mode != "demo" {
		t.Fatal("undefined rules produced actionable signal")
	}
	if !reflect.DeepEqual(before, e.Snapshot()) {
		t.Fatal("dashboard modified ledger")
	}
	if validKey(dowKey) || !validMarketKey(dowKey) || allowedPath("/v3/historical-candle/intraday/GLOBAL_INDEX|../../order/place/minutes/1") {
		t.Fatal("global display key weakened safety boundary")
	}
	a.cfg.Algo.Target = "option"
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.TargetKey != cs[0].Key {
		t.Fatal("option indicator target not applied")
	}
	r = httptest.NewRequest("GET", "/api/algo?underlying=NSE_INDEX%7CNifty%2050&expiry="+cs[0].Expiry+"&option=GLOBAL_INDEX%7CDOW%20FUTURES", nil)
	r.Host = "localhost:8080"
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("unverified global option accepted")
	}
}

func TestAlgoConfigValidation(t *testing.T) {
	for _, entry := range []struct{ k, v string }{{"ALGO_OI_STRIKES_EACH_SIDE", "0"}, {"ALGO_IV_DIFFERENCE_THRESHOLD", "NaN"}, {"ALGO_INDICATOR_TARGET", "invalid"}, {"ALGO_SAR_STEP", "0.9"}, {"ALGO_TREND_LOOKBACK_MINUTES", "2.5"}} {
		t.Run(entry.k, func(t *testing.T) {
			t.Setenv(entry.k, entry.v)
			if _, err := loadAlgoConfig(); err == nil {
				t.Fatal("invalid setting accepted")
			}
		})
	}
}

func TestAlgoRefreshCanBeThreeSeconds(t *testing.T) {
	t.Setenv("ALGO_REFRESH_SECONDS", "3")
	cfg, err := loadAlgoConfig()
	if err != nil || cfg.Refresh != 3 {
		t.Fatalf("three-second refresh rejected: %+v %v", cfg, err)
	}
}
