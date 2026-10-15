package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type niftyVolumeTestProvider struct {
	candles map[string][]Candle
}

func (p niftyVolumeTestProvider) Contracts(context.Context, string) ([]Contract, error) { return nil, nil }
func (p niftyVolumeTestProvider) Chain(context.Context, string, string) ([]ChainRow, error) { return nil, nil }
func (p niftyVolumeTestProvider) Quotes(context.Context, []string) (map[string]Quote, error) { return nil, nil }
func (p niftyVolumeTestProvider) Status(context.Context, string) (string, error) { return "", nil }
func (p niftyVolumeTestProvider) Search(context.Context, string) ([]Contract, error) { return nil, nil }
func (p niftyVolumeTestProvider) Candles(_ context.Context, key string) ([]Candle, error) {
	return append([]Candle(nil), p.candles[key]...), nil
}

func TestAggregateNiftyConstituentVolume(t *testing.T) {
	keys := make([]string, 50)
	p := niftyVolumeTestProvider{candles: map[string][]Candle{}}
	expected := 0.0
	for i := range keys {
		keys[i] = fmt.Sprintf("NSE_EQ|TEST%02d", i)
		volume := float64(i + 1)
		expected += volume
		p.candles[keys[i]] = []Candle{{Time: 60, Volume: volume}, {Time: 120, Volume: volume}}
	}
	index := []Candle{{Time: 60, Open: 100, High: 102, Low: 99, Close: 101}, {Time: 120, Open: 101, High: 104, Low: 100, Close: 103}}
	got, err := aggregateConstituentVolume(context.Background(), p, keys, index)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Volume != expected || got[1].Volume != expected || got[0].MissingVolume {
		t.Fatalf("unexpected aggregated volume: %+v", got)
	}
	tech := technicals(got, 2, 2, 1, .02, .2)
	if tech.VWAP == nil || *tech.VWAP <= 0 {
		t.Fatalf("aggregated volume did not produce VWAP: %+v", tech)
	}
	p.candles[keys[0]] = p.candles[keys[0]][1:]
	got, err = aggregateConstituentVolume(context.Background(), p, keys, index)
	if err != nil || got[0].Volume != expected-1 || got[1].Volume != expected {
		t.Fatalf("missing zero-trade minute was not safely aggregated: %+v %v", got, err)
	}
	p.candles[keys[1]][0].MissingVolume = true
	if _, err := aggregateConstituentVolume(context.Background(), p, keys, index); err == nil {
		t.Fatal("null constituent volume produced VWAP inputs")
	}
}

func TestNiftyConstituentsRequireCurrentOfficialFifty(t *testing.T) {
	var csv strings.Builder
	csv.WriteString("Company Name,ISIN Code\n")
	for i := 0; i < 50; i++ {
		csv.WriteString(fmt.Sprintf("Company %d,INE%03dA01024\n", i, i))
	}
	source := newNiftyVolumeSource()
	source.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(csv.String())), Header: make(http.Header)}, nil
	})
	keys, err := source.constituents(context.Background())
	if err != nil || len(keys) != 50 || keys[0] == "" {
		t.Fatalf("official constituent list was not accepted: %v %#v", err, keys)
	}
	if _, err := source.constituents(context.Background()); err != nil {
		t.Fatal("cached constituent list failed", err)
	}
}

func TestCachedNiftyConstituentVolumesWaitForNextCompletedMinute(t *testing.T) {
	source := &NiftyVolumeSource{volumeByTime: map[int64]float64{60: 100, 120: 200}, volumeLast: 120}
	index := []Candle{{Time: 60}, {Time: 120}}
	got, ok := source.cachedVolumes(index)
	if !ok || got[0].Volume != 100 || got[1].Volume != 200 || got[0].MissingVolume {
		t.Fatalf("cached volumes not applied: %#v, %t", got, ok)
	}
	if _, ok := source.cachedVolumes(append(index, Candle{Time: 180})); ok {
		t.Fatal("cached volumes were reused after a new completed minute")
	}
}
