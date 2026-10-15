package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const niftyConstituentsURL = "https://www.niftyindices.com/IndexConstituent/ind_nifty50list.csv"

// NiftyVolumeSource obtains the current official NIFTY 50 membership. It does
// not access any broker endpoint and has no credentials.
type NiftyVolumeSource struct {
	client       *http.Client
	mu           sync.Mutex
	keys         []string
	until        time.Time
	volumeMu     sync.Mutex
	volumeByTime map[int64]float64
	volumeLast   int64
}

func newNiftyVolumeSource() *NiftyVolumeSource {
	return &NiftyVolumeSource{client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("NIFTY Indices redirects disabled")
	}}}
}

func (n *NiftyVolumeSource) constituents(ctx context.Context) ([]string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.keys) == 50 && time.Now().Before(n.until) {
		return append([]string(nil), n.keys...), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, niftyConstituentsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/csv")
	// Nifty Indices serves the public CSV to browser clients; identify this
	// server-side fetch without sending credentials or user-specific data.
	req.Header.Set("User-Agent", "Optiondesk/1.0 (+local NIFTY 50 VWAP)")
	res, err := n.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("official NIFTY constituent list is unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("official NIFTY constituent list returned HTTP %d", res.StatusCode)
	}
	rows, err := csv.NewReader(io.LimitReader(res.Body, 128<<10)).ReadAll()
	if err != nil {
		return nil, errors.New("official NIFTY constituent list is not valid CSV")
	}
	if len(rows) < 2 {
		return nil, errors.New("official NIFTY constituent list is empty")
	}
	isinColumn := -1
	for i, name := range rows[0] {
		if strings.TrimPrefix(strings.TrimSpace(name), "\ufeff") == "ISIN Code" {
			isinColumn = i
			break
		}
	}
	if isinColumn < 0 {
		return nil, errors.New("official NIFTY constituent list has no ISIN column")
	}
	seen := map[string]bool{}
	keys := make([]string, 0, 50)
	for _, row := range rows[1:] {
		if isinColumn >= len(row) {
			continue
		}
		isin := strings.ToUpper(strings.TrimSpace(row[isinColumn]))
		if !validNSEISIN(isin) || seen[isin] {
			return nil, errors.New("official NIFTY constituent list contains an invalid ISIN")
		}
		seen[isin] = true
		keys = append(keys, "NSE_EQ|"+isin)
	}
	if len(keys) != 50 {
		return nil, fmt.Errorf("official NIFTY constituent list contains %d stocks, expected 50", len(keys))
	}
	sort.Strings(keys)
	n.keys = keys
	n.until = time.Now().Add(6 * time.Hour)
	return append([]string(nil), keys...), nil
}

func validNSEISIN(v string) bool {
	if len(v) != 12 || !strings.HasPrefix(v, "INE") {
		return false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			if r < 'A' || r > 'Z' {
				return false
			}
		}
	}
	return true
}

// candles mirrors TradingView's documented NSE-index-volume definition: sum
// the volume of every component for each matching completed one-minute bar.
func (n *NiftyVolumeSource) candles(ctx context.Context, provider Provider, indexCandles []Candle) ([]Candle, error) {
	// A completed one-minute indicator cannot change until the next completed
	// minute. Reuse its verified component-volume totals between three-second UI
	// refreshes instead of making 50 redundant broker requests each time.
	n.volumeMu.Lock()
	defer n.volumeMu.Unlock()
	if cached, ok := n.cachedVolumes(indexCandles); ok {
		return cached, nil
	}
	keys, err := n.constituents(ctx)
	if err != nil {
		return nil, err
	}
	volumeCandles, err := aggregateConstituentVolume(ctx, provider, keys, indexCandles)
	if err != nil {
		return nil, err
	}
	n.volumeByTime = make(map[int64]float64, len(volumeCandles))
	for _, candle := range volumeCandles {
		n.volumeByTime[candle.Time] = candle.Volume
	}
	n.volumeLast = volumeCandles[len(volumeCandles)-1].Time
	return volumeCandles, nil
}

func (n *NiftyVolumeSource) cachedVolumes(indexCandles []Candle) ([]Candle, bool) {
	if len(indexCandles) == 0 || n.volumeLast != indexCandles[len(indexCandles)-1].Time || len(n.volumeByTime) == 0 {
		return nil, false
	}
	out := append([]Candle(nil), indexCandles...)
	for i := range out {
		volume, ok := n.volumeByTime[out[i].Time]
		if !ok {
			return nil, false
		}
		out[i].Volume = volume
		out[i].MissingVolume = false
	}
	return out, true
}

func aggregateConstituentVolume(ctx context.Context, provider Provider, keys []string, indexCandles []Candle) ([]Candle, error) {
	if len(keys) != 50 {
		return nil, errors.New("expected 50 NIFTY constituents")
	}
	byTime := make(map[int64]float64, len(indexCandles))
	for _, c := range indexCandles {
		byTime[c.Time] = 0
	}
	if len(byTime) == 0 {
		return nil, errors.New("no completed index candles")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, 10) // 50 candle calls remain below Upstox's standard 50/sec limit.
	errCh := make(chan error, len(keys))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, key := range keys {
		key := key
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			candles, err := provider.Candles(ctx, key)
			if err != nil {
				select { case errCh <- fmt.Errorf("%s: %w", key, err): default: }
				cancel()
				return
			}
			if len(candles) == 0 {
				select { case errCh <- fmt.Errorf("%s returned no candle history", key): default: }
				cancel()
				return
			}
			volumes := make(map[int64]Candle, len(candles))
			for _, candle := range candles {
				volumes[candle.Time] = candle
			}
			mu.Lock()
			defer mu.Unlock()
			for at := range byTime {
				candle, ok := volumes[at]
				// Upstox can omit a minute with no constituent trade. TradingView's
				// index-volume definition sums that component's volume as zero for
				// the minute; it is not a partial basket. A returned null/invalid
				// volume remains a hard data-quality failure.
				if !ok {
					continue
				}
				if candle.MissingVolume || !finite(candle.Volume) || candle.Volume < 0 {
					select { case errCh <- fmt.Errorf("%s has incomplete minute-volume history", key): default: }
					cancel()
					return
				}
				byTime[at] += candle.Volume
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return nil, err
	default:
	}
	out := append([]Candle(nil), indexCandles...)
	for i := range out {
		out[i].Volume = byTime[out[i].Time]
		out[i].MissingVolume = false
	}
	return out, nil
}
