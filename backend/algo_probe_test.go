package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// Opt-in read-only integration check. Never prints credentials or account data.
func TestAlgoLiveData(t *testing.T) {
	if os.Getenv("OPTIONDESK_VERIFY_LIVE") != "1" {
		t.Skip("explicit live verification only")
	}
	if err := os.Chdir(".."); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir("backend")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	p := newUpstox(cfg.Token)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	for _, name := range []string{"India VIX"} {
		rows, err := p.Search(ctx, name)
		if err != nil {
			t.Logf("Search %s: %v", name, err)
			continue
		}
		for _, row := range rows {
			t.Logf("Search %s: %s %s", name, row.Key, row.Name)
		}
		t.Logf("Search %s: %d results", name, len(rows))
	}
	for _, key := range []string{"GLOBAL_INDEX|DOW FUTURES", "NSE_INDEX|Nifty 50", "NSE_INDEX|India VIX"} {
		qs, e := p.Quotes(ctx, []string{key})
		t.Logf("Quote %s: %+v, error %v", key, qs[key], e)
		cs, e := p.Candles(ctx, key)
		t.Logf("Candles %s: %d, error %v", key, len(cs), e)
		if len(cs) > 0 {
			t.Logf("Latest %+v", cs[len(cs)-1])
		}
	}
	candles, err := p.Candles(ctx, "NSE_INDEX|India VIX")
	if err != nil {
		t.Logf("VIX candles: %v", err)
	} else if len(candles) > 0 {
		t.Logf("VIX candles: %d, latest %s", len(candles), time.Unix(candles[len(candles)-1].Time, 0).In(ist))
	}
	contracts, err := p.Contracts(ctx, "NSE_INDEX|Nifty 50")
	if err != nil {
		t.Fatal(err)
	}
	if len(contracts) == 0 {
		t.Fatal("no contracts")
	}
	rows, err := p.Chain(ctx, "NSE_INDEX|Nifty 50", contracts[0].Expiry)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("NIFTY chain: %d strikes", len(rows))
	candles, err = p.Candles(ctx, contracts[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Option candles: %d", len(candles))
	if len(candles) > 0 {
		t.Logf("Latest option candle volume: %v", candles[len(candles)-1].Volume)
	}
}
