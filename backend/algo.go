package main

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const vixKey = "NSE_INDEX|India VIX"
const dowKey = "GLOBAL_INDEX|DOW FUTURES"

type Comparison struct {
	Call       *float64 `json:"call"`
	Put        *float64 `json:"put"`
	Difference *float64 `json:"difference"`
	Leader     string   `json:"leader"`
	Count      int      `json:"count"`
	Expected   int      `json:"expected"`
	Low        float64  `json:"low"`
	High       float64  `json:"high"`
	Issue      string   `json:"issue"`
}
type Trend struct {
	Current    *float64  `json:"current"`
	Previous   *float64  `json:"previous"`
	Change     *float64  `json:"change"`
	Percent    *float64  `json:"percent"`
	At         time.Time `json:"at"`
	PreviousAt int64     `json:"previousAt"`
	Direction  string    `json:"direction"`
	Issue      string    `json:"issue"`
}
type AlgoDashboard struct {
	Config         AlgoConfig       `json:"config"`
	Mode           string           `json:"mode"`
	ReceivedAt     time.Time        `json:"receivedAt"`
	Underlying     string           `json:"underlying"`
	Option         string           `json:"option"`
	Expiry         string           `json:"expiry"`
	TargetKey      string           `json:"targetKey"`
	ATM            float64          `json:"atm"`
	OI             Comparison       `json:"oi"`
	IV             Comparison       `json:"iv"`
	VIX            Trend            `json:"vix"`
	Dow            Trend            `json:"dow"`
	Technical      Technicals       `json:"technical"`
	TechnicalChart []TechnicalPoint `json:"technicalChart"`
	TechnicalIssue string           `json:"technicalIssue"`
	ChainIssue     string           `json:"chainIssue"`
	MarketStatus   string           `json:"marketStatus"`
	FinalCall      string           `json:"finalCall"`
	FinalReason    string           `json:"finalReason"`
}

func compareChain(rows []ChainRow, wings int, iv bool) Comparison {
	out := Comparison{Expected: 2*wings + 1, Leader: "Unavailable"}
	if len(rows) == 0 {
		out.Issue = "Option chain unavailable"
		return out
	}
	rows = append([]ChainRow(nil), rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Strike < rows[j].Strike })
	spot := rows[0].Spot
	if !finite(spot) || spot <= 0 {
		out.Issue = "Underlying spot unavailable"
		return out
	}
	atm := 0
	for i, r := range rows {
		if !finite(r.Strike) || r.Strike <= 0 || (i > 0 && r.Strike == rows[i-1].Strike) {
			out.Issue = "Invalid or duplicate strikes"
			return out
		}
		if math.Abs(r.Strike-spot) < math.Abs(rows[atm].Strike-spot) {
			atm = i
		}
	}
	start, end := max(0, atm-wings), min(len(rows), atm+wings+1)
	out.Count = end - start
	out.Low = rows[start].Strike
	out.High = rows[end-1].Strike
	if out.Count != out.Expected {
		out.Issue = "Incomplete ATM range; totals withheld"
		return out
	}
	call, put := 0.0, 0.0
	for _, r := range rows[start:end] {
		c, p := r.Call.Market.OI, r.Put.Market.OI
		missing := r.Call.Market.MissingOI || r.Put.Market.MissingOI
		if iv {
			c, p = r.Call.Greeks.IV, r.Put.Greeks.IV
			missing = c <= 0 || p <= 0
		}
		if r.Call.Key == "" || r.Put.Key == "" || missing || !finite(c) || !finite(p) || c < 0 || p < 0 {
			out.Issue = "Missing CE / PE values in ATM range; comparison withheld"
			return out
		}
		call += c
		put += p
	}
	if iv {
		call /= float64(out.Count)
		put /= float64(out.Count)
	}
	diff := math.Abs(call - put)
	out.Call = &call
	out.Put = &put
	out.Difference = &diff
	out.Leader = "Equal"
	if call > put {
		out.Leader = "CE"
	}
	if put > call {
		out.Leader = "PE"
	}
	return out
}

func completedCandles(cs []Candle, now time.Time) ([]Candle, string) {
	out := []Candle{}
	day := now.In(ist).Format("2006-01-02")
	for _, c := range cs {
		t := time.Unix(c.Time, 0)
		if t.In(ist).Format("2006-01-02") != day || t.Add(time.Minute).After(now) {
			continue
		}
		if !finite(c.Open) || !finite(c.High) || !finite(c.Low) || !finite(c.Close) || !finite(c.Volume) || c.Low <= 0 || c.Open < c.Low || c.Open > c.High || c.Close < c.Low || c.Close > c.High || c.Volume < 0 {
			return nil, "Invalid candle values; indicators withheld"
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	for i := 1; i < len(out); i++ {
		if out[i].Time-out[i-1].Time != 60 {
			return nil, "Incomplete one-minute candle history; indicators withheld"
		}
	}
	if len(out) == 0 {
		return out, "No completed candles for this session"
	}
	return out, ""
}

func trendFrom(q Quote, cs []Candle, cfg AlgoConfig, now time.Time) Trend {
	out := Trend{Direction: "Unavailable", At: q.At}
	if q.LTP <= 0 || !finite(q.LTP) || q.At.IsZero() {
		out.Issue = "Current quote or provider timestamp unavailable"
		return out
	}
	out.Current = &q.LTP
	if now.Sub(q.At) > time.Duration(cfg.MaxAge)*time.Second || q.At.After(now.Add(5*time.Second)) {
		out.Issue = "Stale or invalid provider timestamp"
		return out
	}
	// Compare with the candle beginning exactly N minutes before the quote's minute.
	target := q.At.Truncate(time.Minute).Add(-time.Duration(cfg.Lookback) * time.Minute).Unix()
	for _, c := range cs {
		if c.Time != target || !finite(c.Close) || c.Close <= 0 {
			continue
		}
		previous := c.Close
		change := q.LTP - previous
		pct := change / previous * 100
		out.Previous = &previous
		out.PreviousAt = c.Time
		out.Change = &change
		out.Percent = &pct
		out.Direction = "Flat"
		if change > 0 {
			out.Direction = "Up"
		}
		if change < 0 {
			out.Direction = "Down"
		}
		return out
	}
	out.Issue = "Comparison candle unavailable; waiting for lookback history"
	return out
}

func (a *App) algoDashboard(w http.ResponseWriter, r *http.Request) {
	under, expiry, key := r.URL.Query().Get("underlying"), r.URL.Query().Get("expiry"), r.URL.Query().Get("option")
	if !validKey(under) {
		fail(w, 400, "invalid underlying")
		return
	}
	if _, err := time.Parse("2006-01-02", expiry); err != nil {
		fail(w, 400, "invalid expiry")
		return
	}
	cfg := a.cfg.Algo
	if cfg.EMAPeriod == 0 {
		var err error
		cfg, err = loadAlgoConfig()
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	contracts, err := a.provider.Contracts(r.Context(), under)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	verified := false
	for _, c := range contracts {
		if c.Key == key && c.Expiry == expiry && c.Underlying == under && (c.Type == "CE" || c.Type == "PE") {
			verified = true
			break
		}
	}
	if !verified {
		fail(w, 400, "select a verified option for this underlying and expiry")
		return
	}
	target := under
	if cfg.Target == "option" {
		target = key
	}
	out := AlgoDashboard{Config: cfg, Mode: a.cfg.Mode, Underlying: under, Option: key, Expiry: expiry, TargetKey: target, FinalCall: "WAIT", FinalReason: "Entry conditions are not configured. Display only; no automated orders."}
	// Independent reads only; no ledger registration, Refresh or Submit is invoked.
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	var rows []ChainRow
	var cs, vc, dc []Candle
	var qs map[string]Quote
	var chainErr, candleErr, vixErr, dowErr, quoteErr, statusErr error
	var wg sync.WaitGroup
	wg.Add(6)
	go func() { defer wg.Done(); rows, chainErr = a.provider.Chain(ctx, under, expiry) }()
	go func() { defer wg.Done(); cs, candleErr = a.provider.Candles(ctx, target) }()
	go func() { defer wg.Done(); vc, vixErr = a.provider.Candles(ctx, vixKey) }()
	go func() { defer wg.Done(); dc, dowErr = a.provider.Candles(ctx, dowKey) }()
	go func() { defer wg.Done(); qs, quoteErr = a.provider.Quotes(ctx, []string{vixKey, dowKey}) }()
	go func() {
		defer wg.Done()
		exchange := "NSE"
		if strings.HasPrefix(under, "BSE") {
			exchange = "BSE"
		}
		out.MarketStatus, statusErr = a.provider.Status(ctx, exchange)
	}()
	wg.Wait()
	now := time.Now()
	out.ReceivedAt = now
	out.OI = compareChain(rows, cfg.OIWings, false)
	out.IV = compareChain(rows, cfg.IVWings, true)
	if len(rows) > 0 {
		out.ATM = rows[0].Strike
		for _, row := range rows {
			if math.Abs(row.Strike-rows[0].Spot) < math.Abs(out.ATM-rows[0].Spot) {
				out.ATM = row.Strike
			}
		}
	}
	if chainErr != nil {
		out.OI = compareChain(nil, cfg.OIWings, false)
		out.IV = compareChain(nil, cfg.IVWings, true)
		out.ChainIssue = chainErr.Error()
	}
	if statusErr != nil {
		out.MarketStatus = "UNKNOWN"
		out.ChainIssue = statusErr.Error()
	}
	if candleErr != nil {
		out.TechnicalIssue = candleErr.Error()
	} else {
		clean, issue := completedCandles(cs, now)
		out.TechnicalIssue = issue
		out.Technical = technicals(clean, cfg.EMAPeriod, cfg.ATRPeriod, cfg.STMultiplier, cfg.SARStep, cfg.SARMax)
		out.TechnicalChart = technicalSeries(clean, cfg.EMAPeriod, cfg.ATRPeriod, cfg.STMultiplier, cfg.SARStep, cfg.SARMax)
		// NIFTY 50 is an index, so its Upstox candles can legitimately have no
		// volume. TradingView defines its index volume as the per-minute sum of
		// every current constituent's trading volume; use the same public method.
		if target == "NSE_INDEX|Nifty 50" && out.Technical.VWAP == nil && len(clean) > 0 {
			if a.cfg.Mode != "live" || a.niftyVolume == nil {
				out.Technical.VWAPIssue = "NIFTY 50 constituent volume is available in live mode only"
			} else if volumeCandles, err := a.niftyVolume.candles(ctx, a.provider, clean); err != nil {
				out.Technical.VWAPIssue = "NIFTY 50 constituent volume unavailable: " + err.Error()
			} else {
				out.Technical = technicals(volumeCandles, cfg.EMAPeriod, cfg.ATRPeriod, cfg.STMultiplier, cfg.SARStep, cfg.SARMax)
				out.TechnicalChart = technicalSeries(volumeCandles, cfg.EMAPeriod, cfg.ATRPeriod, cfg.STMultiplier, cfg.SARStep, cfg.SARMax)
				out.Technical.VWAPSource = "NIFTY 50 constituent volume · 50 NSE stocks"
			}
		}
		if len(clean) > 0 && now.Unix()-clean[len(clean)-1].Time > int64(cfg.MaxAge) {
			out.TechnicalIssue = "Stale candles — values are historical"
		}
	}
	out.VIX = trendFrom(qs[vixKey], vc, cfg, now)
	out.Dow = trendFrom(qs[dowKey], dc, cfg, now)
	if quoteErr != nil {
		out.VIX.Issue = quoteErr.Error()
		out.Dow.Issue = quoteErr.Error()
	}
	if vixErr != nil {
		out.VIX.Issue = vixErr.Error()
	}
	if dowErr != nil {
		out.Dow.Issue = dowErr.Error()
	}
	respond(w, 200, out)
}
