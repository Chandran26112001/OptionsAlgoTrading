package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// Explicit opt-in fixtures. These never share a ledger with live-data paper trading.
type Demo struct{}

func (d Demo) Contracts(_ context.Context, key string) ([]Contract, error) {
	out := []Contract{}
	base := 25000.0
	name := "NIFTY"
	lot := 75
	exchange := "NSE"
	if strings.Contains(key, "Bank") {
		base = 55000
		name = "BANKNIFTY"
		lot = 30
	}
	if strings.Contains(key, "SENSEX") {
		base, name, lot, exchange = 81000, "SENSEX", 20, "BSE"
	}
	for week := 1; week <= 3; week++ {
		expiry := time.Now().In(ist).AddDate(0, 0, 7*week).Format("2006-01-02")
		for n := -12; n <= 12; n++ {
			strike := base + float64(n)*50
			for _, typ := range []string{"CE", "PE"} {
				out = append(out, Contract{Key: fmt.Sprintf("%s_FO|DEMO_%s_%s_%.0f_%s", exchange, name, expiry, strike, typ), Symbol: fmt.Sprintf("%s %.0f %s", name, strike, typ), Type: typ, Underlying: key, Exchange: exchange, Expiry: expiry, Strike: strike, Lot: lot, Tick: 5})
			}
		}
	}
	return out, nil
}
func demoPrice(strike float64, typ string, base float64) float64 {
	spot := base + 42 + math.Sin(float64(time.Now().Unix())/40)*25
	intrinsic := spot - strike
	if typ == "PE" {
		intrinsic = -intrinsic
	}
	return math.Round((math.Max(0, intrinsic)+125*math.Exp(-math.Abs(spot-strike)/500))*20) / 20
}
func (d Demo) Chain(ctx context.Context, key, expiry string) ([]ChainRow, error) {
	cs, _ := d.Contracts(ctx, key)
	base := 25000.0
	if strings.Contains(key, "Bank") {
		base = 55000
	}
	if strings.Contains(key, "SENSEX") {
		base = 81000
	}
	out := []ChainRow{}
	for i := 0; i < len(cs); i += 2 {
		c := cs[i]
		if c.Expiry != expiry {
			continue
		}
		r := ChainRow{Strike: c.Strike, Spot: base + 42 + math.Sin(float64(time.Now().Unix())/40)*25}
		for j := 0; j < 2; j++ {
			c = cs[i+j]
			p := demoPrice(c.Strike, c.Type, base)
			delta := 1 / (1 + math.Exp((c.Strike-r.Spot)/180))
			if j == 1 {
				delta -= 1
			}
			leg := Leg{Key: c.Key, Market: MarketData{LTP: p, Bid: p - .5, Ask: p + .5, OI: float64(450000 + i*32000 + j*41000), PrevOI: 360000, Volume: int64(150000 + i*8100), Close: p * .96}, Greeks: Greeks{delta, .0017, -14.6, 23.4, 13.8}}
			if j == 0 {
				r.Call = leg
			} else {
				r.Put = leg
			}
		}
		out = append(out, r)
	}
	return out, nil
}
func (d Demo) Quotes(ctx context.Context, keys []string) (map[string]Quote, error) {
	out := map[string]Quote{}
	for _, under := range []string{"NSE_INDEX|Nifty 50", "NSE_INDEX|Nifty Bank", "BSE_INDEX|SENSEX"} {
		cs, _ := d.Contracts(ctx, under)
		base := 25000.0
		if strings.Contains(under, "Bank") {
			base = 55000
		}
		if strings.Contains(under, "SENSEX") {
			base = 81000
		}
		for _, c := range cs {
			for _, key := range keys {
				if c.Key == key {
					p := demoPrice(c.Strike, c.Type, base)
					out[key] = Quote{key, p, p - .5, p + .5, 100000, 100000, time.Now()}
				}
			}
		}
	}
	return out, nil
}
func (Demo) Status(context.Context, string) (string, error) { return "NORMAL_OPEN", nil }
func (Demo) Search(context.Context, string) ([]Contract, error) {
	return []Contract{{Key: "NSE_INDEX|Nifty 50", Symbol: "NIFTY", Name: "Nifty 50"}, {Key: "NSE_INDEX|Nifty Bank", Symbol: "BANKNIFTY", Name: "Nifty Bank"}}, nil
}
func (Demo) Candles(_ context.Context, key string) ([]Candle, error) {
	base := 25000.0
	if strings.Contains(key, "Bank") {
		base = 55000
	}
	if strings.Contains(key, "SENSEX") {
		base = 81000
	}
	if strings.Contains(key, "DEMO") {
		base = 160
	}
	out := []Candle{}
	end := time.Now().Truncate(time.Minute).Unix()
	prev := base - 60
	for i := 0; i < 180; i++ {
		close := base - 60 + float64(i)*.55 + math.Sin(float64(i)/9)*18 + math.Sin(float64(i)*1.7)*4
		out = append(out, Candle{end - int64(179-i)*60, prev, math.Max(prev, close) + 3, math.Min(prev, close) - 4, close})
		prev = close
	}
	return out, nil
}
