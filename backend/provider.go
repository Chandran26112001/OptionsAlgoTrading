package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Contract struct {
	Key        string  `json:"instrument_key"`
	Symbol     string  `json:"trading_symbol"`
	Name       string  `json:"name"`
	Type       string  `json:"instrument_type"`
	Underlying string  `json:"underlying_key"`
	Exchange   string  `json:"exchange"`
	Expiry     string  `json:"expiry"`
	Strike     float64 `json:"strike_price"`
	Lot        int     `json:"lot_size"`
	Tick       float64 `json:"tick_size"`
}
type MarketData struct {
	LTP    float64 `json:"ltp"`
	Bid    float64 `json:"bid_price"`
	Ask    float64 `json:"ask_price"`
	OI     float64 `json:"oi"`
	PrevOI float64 `json:"prev_oi"`
	Volume int64   `json:"volume"`
	Close  float64 `json:"close_price"`
}
type Greeks struct {
	Delta float64 `json:"delta"`
	Gamma float64 `json:"gamma"`
	Theta float64 `json:"theta"`
	Vega  float64 `json:"vega"`
	IV    float64 `json:"iv"`
}
type Leg struct {
	Key    string     `json:"instrument_key"`
	Market MarketData `json:"market_data"`
	Greeks Greeks     `json:"option_greeks"`
}
type ChainRow struct {
	Strike float64 `json:"strike_price"`
	Spot   float64 `json:"underlying_spot_price"`
	Call   Leg     `json:"call_options"`
	Put    Leg     `json:"put_options"`
}
type Quote struct {
	Key    string    `json:"key"`
	LTP    float64   `json:"ltp"`
	Bid    float64   `json:"bid"`
	Ask    float64   `json:"ask"`
	BidQty int       `json:"bidQty"`
	AskQty int       `json:"askQty"`
	At     time.Time `json:"at"`
}
type Candle struct {
	Time  int64   `json:"time"`
	Open  float64 `json:"open"`
	High  float64 `json:"high"`
	Low   float64 `json:"low"`
	Close float64 `json:"close"`
}
type Provider interface {
	Contracts(context.Context, string) ([]Contract, error)
	Chain(context.Context, string, string) ([]ChainRow, error)
	Quotes(context.Context, []string) (map[string]Quote, error)
	Status(context.Context, string) (string, error)
	Candles(context.Context, string) ([]Candle, error)
	Search(context.Context, string) ([]Contract, error)
}
type cacheEntry struct {
	data  json.RawMessage
	until time.Time
}
type Upstox struct {
	token        string
	client       *http.Client
	mu           sync.Mutex
	cache        map[string]cacheEntry
	blockedUntil time.Time
}

func newUpstox(token string) *Upstox {
	return &Upstox{token: token, client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("provider redirects disabled") }}, cache: map[string]cacheEntry{}}
}

// This is the ONLY broker transport. Neither a method nor a host is caller-configurable.
func allowedPath(p string) bool {
	switch p {
	case "/v2/option/contract", "/v2/option/chain", "/v2/market-quote/quotes", "/v2/instruments/search", "/v2/market/status/NSE", "/v2/market/status/BSE":
		return true
	}
	if strings.HasPrefix(p, "/v3/historical-candle/intraday/") && strings.HasSuffix(p, "/minutes/1") {
		middle := strings.TrimSuffix(strings.TrimPrefix(p, "/v3/historical-candle/intraday/"), "/minutes/1")
		return validKey(middle)
	}
	return false
}
func validKey(k string) bool {
	if strings.ContainsAny(k, "/\\?#\r\n") || len(k) > 160 {
		return false
	}
	for _, pre := range []string{"NSE_INDEX|", "BSE_INDEX|", "NSE_EQ|", "BSE_EQ|", "NSE_FO|", "BSE_FO|"} {
		if strings.HasPrefix(k, pre) && len(k) > len(pre) {
			return true
		}
	}
	return false
}
func (u *Upstox) get(ctx context.Context, p string, q url.Values, ttl time.Duration, out any) error {
	if !allowedPath(p) {
		return errors.New("broker endpoint is not on the read-only allowlist")
	}
	if u.token == "" {
		return errors.New("Add UPSTOX_ACCESS_TOKEN to .env and restart the server to connect live data")
	}
	key := p + "?" + q.Encode()
	// Serialise/cache requests across browser tabs and workers; limits duplicate upstream polling.
	u.mu.Lock()
	defer u.mu.Unlock()
	if c, ok := u.cache[key]; ok && time.Now().Before(c.until) {
		return json.Unmarshal(c.data, out)
	}
	if time.Now().Before(u.blockedUntil) {
		return errors.New("Upstox rate limit: cooling down for 30 seconds")
	}
	target := url.URL{Scheme: "https", Host: "api.upstox.com", Path: p, RawQuery: q.Encode()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+u.token)
	req.Header.Set("Accept", "application/json")
	res, err := u.client.Do(req)
	if err != nil {
		return errors.New("Upstox is unreachable; check your connection")
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return errors.New("Upstox rejected the token or data permission; update .env and restart")
	}
	if res.StatusCode == 429 {
		u.blockedUntil = time.Now().Add(30 * time.Second)
		return errors.New("Upstox rate limit: cooling down for 30 seconds")
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("Upstox returned HTTP %d for %s", res.StatusCode, p)
	}
	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&envelope); err != nil {
		return errors.New("invalid Upstox response")
	}
	if envelope.Status != "success" || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("Upstox returned incomplete market data")
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("unexpected Upstox data format: %w", err)
	}
	if ttl > 0 {
		if len(u.cache) > 300 {
			u.cache = map[string]cacheEntry{}
		}
		u.cache[key] = cacheEntry{envelope.Data, time.Now().Add(ttl)}
	}
	return nil
}
func (u *Upstox) Contracts(ctx context.Context, key string) ([]Contract, error) {
	var out []Contract
	err := u.get(ctx, "/v2/option/contract", url.Values{"instrument_key": {key}}, 5*time.Minute, &out)
	return cleanContracts(out), err
}
func cleanContracts(in []Contract) []Contract {
	out := []Contract{}
	today := time.Now().In(ist).Format("2006-01-02")
	for _, c := range in {
		if len(c.Expiry) >= 10 {
			c.Expiry = c.Expiry[:10]
		}
		if (c.Type == "CE" || c.Type == "PE") && (strings.HasPrefix(c.Key, "NSE_FO|") || strings.HasPrefix(c.Key, "BSE_FO|")) && c.Lot > 0 && c.Expiry >= today {
			out = append(out, c)
		}
	}
	return out
}
func (u *Upstox) Chain(ctx context.Context, key, expiry string) ([]ChainRow, error) {
	var out []ChainRow
	err := u.get(ctx, "/v2/option/chain", url.Values{"instrument_key": {key}, "expiry_date": {expiry}}, 3*time.Second, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Strike < out[j].Strike })
	return out, err
}
func (u *Upstox) Status(ctx context.Context, exchange string) (string, error) {
	var out struct {
		Status string `json:"status"`
	}
	err := u.get(ctx, "/v2/market/status/"+exchange, nil, 15*time.Second, &out)
	return out.Status, err
}
func (u *Upstox) Search(ctx context.Context, q string) ([]Contract, error) {
	var out []Contract
	err := u.get(ctx, "/v2/instruments/search", url.Values{"query": {q}, "exchanges": {"NSE,BSE"}, "segments": {"EQ,INDEX"}, "records": {"20"}}, time.Minute, &out)
	return out, err
}
func (u *Upstox) Quotes(ctx context.Context, keys []string) (map[string]Quote, error) {
	out := map[string]Quote{}
	if len(keys) == 0 {
		return out, nil
	}
	sort.Strings(keys)
	if len(keys) > 500 {
		return nil, errors.New("at most 500 active instruments supported")
	}
	type level struct {
		Price    float64 `json:"price"`
		Quantity int     `json:"quantity"`
	}
	var raw map[string]struct {
		Key       string          `json:"instrument_token"`
		Price     float64         `json:"last_price"`
		Timestamp json.RawMessage `json:"timestamp"`
		Depth     struct {
			Buy  []level `json:"buy"`
			Sell []level `json:"sell"`
		} `json:"depth"`
	}
	err := u.get(ctx, "/v2/market-quote/quotes", url.Values{"instrument_key": {strings.Join(keys, ",")}}, 0, &raw)
	if err != nil {
		return nil, err
	}
	for _, v := range raw {
		q := Quote{Key: v.Key, LTP: v.Price, At: parseTimestamp(v.Timestamp)}
		if len(v.Depth.Buy) > 0 {
			q.Bid = v.Depth.Buy[0].Price
			q.BidQty = v.Depth.Buy[0].Quantity
		}
		if len(v.Depth.Sell) > 0 {
			q.Ask = v.Depth.Sell[0].Price
			q.AskQty = v.Depth.Sell[0].Quantity
		}
		out[q.Key] = q
	}
	return out, nil
}
func parseTimestamp(raw json.RawMessage) time.Time {
	s := strings.Trim(string(raw), "\"")
	if f, err := strconv.ParseInt(s, 10, 64); err == nil {
		if f > 1e12 {
			return time.UnixMilli(f)
		}
		return time.Unix(f, 0)
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
func (u *Upstox) Candles(ctx context.Context, key string) ([]Candle, error) {
	var raw struct {
		Candles [][]json.RawMessage `json:"candles"`
	}
	err := u.get(ctx, "/v3/historical-candle/intraday/"+key+"/minutes/1", nil, 15*time.Second, &raw)
	if err != nil {
		return nil, err
	}
	out := []Candle{}
	seen := map[int64]bool{}
	for _, r := range raw.Candles {
		if len(r) < 5 {
			continue
		}
		t := parseTimestamp(r[0])
		var o, h, l, c float64
		_ = json.Unmarshal(r[1], &o)
		_ = json.Unmarshal(r[2], &h)
		_ = json.Unmarshal(r[3], &l)
		_ = json.Unmarshal(r[4], &c)
		if t.IsZero() || seen[t.Unix()] || c <= 0 {
			continue
		}
		seen[t.Unix()] = true
		out = append(out, Candle{t.Unix(), o, h, l, c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out, nil
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func money(v float64) int64 { return int64(math.Round(v * 100)) }
