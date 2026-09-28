package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Position struct {
	Contract Contract `json:"contract"`
	Qty      int      `json:"qty"`
	Cost     int64    `json:"costPaise"`
}
type Order struct {
	ID       string     `json:"id"`
	Contract Contract   `json:"contract"`
	Side     string     `json:"side"`
	Kind     string     `json:"kind"`
	Qty      int        `json:"qty"`
	Limit    int64      `json:"limitPaise"`
	Fill     int64      `json:"fillPaise"`
	Fee      int64      `json:"feePaise"`
	Status   string     `json:"status"`
	Reason   string     `json:"reason,omitempty"`
	Created  time.Time  `json:"created"`
	Filled   *time.Time `json:"filled,omitempty"`
}
type Ledger struct {
	Version   int                 `json:"version"`
	Mode      string              `json:"mode"`
	Initial   int64               `json:"initialPaise"`
	Cash      int64               `json:"cashPaise"`
	Realized  int64               `json:"realizedPaise"`
	Fees      int64               `json:"feesPaise"`
	Positions map[string]Position `json:"positions"`
	Orders    []Order             `json:"orders"`
}
type Engine struct {
	mu          sync.Mutex
	cfg         Config
	provider    Provider
	state       Ledger
	quotes      map[string]Quote
	contracts   map[string]Contract
	path        string
	feedError   string
	lastRefresh time.Time
}
type OrderRequest struct {
	ID         string  `json:"id"`
	Key        string  `json:"key"`
	Underlying string  `json:"underlying"`
	Side       string  `json:"side"`
	Kind       string  `json:"kind"`
	Lots       int     `json:"lots"`
	Limit      float64 `json:"limit"`
}

func newEngine(cfg Config, p Provider) (*Engine, func(), error) {
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(cfg.DataDir, "paper-"+cfg.Mode+".json")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, fmt.Errorf("ledger is locked by another server (after a crash, remove %s.lock only after checking no server is running): %w", path, err)
	}
	_, _ = fmt.Fprintln(lock, os.Getpid())
	_ = lock.Close()
	cleanup := func() { _ = os.Remove(path + ".lock") }
	e := &Engine{cfg: cfg, provider: p, path: path, quotes: map[string]Quote{}, contracts: map[string]Contract{}}
	e.state = Ledger{Version: 1, Mode: cfg.Mode, Initial: cfg.Capital, Cash: cfg.Capital, Positions: map[string]Position{}, Orders: []Order{}}
	if b, err := os.ReadFile(path); err == nil {
		if err = json.Unmarshal(b, &e.state); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("ledger is corrupt; refusing to reset: %w", err)
		}
		if e.state.Version != 1 || e.state.Mode != cfg.Mode || e.state.Positions == nil || e.state.Initial <= 0 {
			cleanup()
			return nil, nil, errors.New("unsupported or invalid ledger")
		}
	} else if !os.IsNotExist(err) {
		cleanup()
		return nil, nil, err
	}
	for _, pos := range e.state.Positions {
		e.contracts[pos.Contract.Key] = pos.Contract
	}
	for _, o := range e.state.Orders {
		e.contracts[o.Contract.Key] = o.Contract
	}
	return e, cleanup, nil
}
func (e *Engine) save(s Ledger) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(e.path), ".paper-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, e.path); err != nil {
		return err
	}
	e.state = s
	return nil
}
func clone(s Ledger) Ledger {
	out := s
	out.Orders = append([]Order{}, s.Orders...)
	out.Positions = map[string]Position{}
	for k, v := range s.Positions {
		out.Positions[k] = v
	}
	return out
}
func (e *Engine) register(cs []Contract) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range cs {
		e.contracts[c.Key] = c
	}
}
func (e *Engine) margin(s Ledger) int64 {
	var m int64
	for _, p := range s.Positions {
		if p.Qty < 0 {
			m += int64(-p.Qty/p.Contract.Lot) * e.cfg.Margin
		}
	}
	return m
}
func (e *Engine) reserved(s Ledger, skip string) int64 {
	var n int64
	for _, o := range s.Orders {
		if o.Status != "PENDING" || o.ID == skip {
			continue
		}
		if o.Side == "BUY" {
			n += o.Limit*int64(o.Qty) + e.cfg.Fee
		} else {
			n += int64(o.Qty/o.Contract.Lot)*e.cfg.Margin + e.cfg.Fee
		}
	}
	return n
}
func quoteOK(q Quote, now time.Time) bool {
	return !q.At.IsZero() && now.Sub(q.At) < 15*time.Second && now.Sub(q.At) > -5*time.Second && finite(q.LTP) && q.LTP > 0
}
func expired(c Contract, now time.Time) bool {
	t, err := time.ParseInLocation("2006-01-02 15:04", c.Expiry+" 15:30", ist)
	return err != nil || !now.Before(t)
}
func executionPrice(o Order, q Quote) (int64, error) {
	if !quoteOK(q, time.Now()) {
		return 0, errors.New("quote is stale or missing; no simulated fill")
	}
	p := q.Ask
	qty := q.AskQty
	if o.Side == "SELL" {
		p = q.Bid
		qty = q.BidQty
	}
	if !finite(p) || p <= 0 || p > 1e8 || q.Ask < q.Bid || qty < o.Qty {
		return 0, errors.New("insufficient top-of-book liquidity or invalid bid/ask; no simulated fill")
	}
	return money(p), nil
}
func (e *Engine) applyFill(s *Ledger, o *Order, price int64) error {
	p := s.Positions[o.Contract.Key]
	p.Contract = o.Contract
	signed := o.Qty
	if o.Side == "SELL" {
		signed = -signed
	}
	oldQty := p.Qty
	closeQty := 0
	var realized int64
	if oldQty*signed < 0 {
		closeQty = min(abs(oldQty), abs(signed))
		released := int64(math.Round(float64(p.Cost) * float64(closeQty) / float64(abs(oldQty))))
		sign := int64(1)
		if oldQty < 0 {
			sign = -1
		}
		realized = sign * (price*int64(closeQty) - released)
		p.Cost -= released
	}
	opening := abs(signed) - closeQty
	p.Cost += int64(opening) * price
	p.Qty += signed
	next := clone(*s)
	next.Cash -= int64(signed)*price + e.cfg.Fee
	next.Realized += realized
	next.Fees += e.cfg.Fee
	if p.Qty == 0 {
		delete(next.Positions, o.Contract.Key)
	} else {
		next.Positions[o.Contract.Key] = p
	}
	// Closing trades remain possible after losses even if the account is below reserve.
	if opening > 0 && next.Cash-e.margin(next)-e.reserved(next, o.ID) < 0 {
		return errors.New("insufficient paper buying power")
	}
	now := time.Now()
	o.Fill = price
	o.Fee = e.cfg.Fee
	o.Filled = &now
	o.Status = "FILLED"
	*s = next
	return nil
}
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
func (e *Engine) Submit(ctx context.Context, r OrderRequest) (Order, error) {
	if len(r.ID) < 8 || len(r.ID) > 80 || strings.ContainsAny(r.ID, "\r\n") {
		return Order{}, errors.New("a unique order id of 8..80 characters is required")
	}
	if r.Lots < 1 || r.Lots > 100 || (r.Side != "BUY" && r.Side != "SELL") || (r.Kind != "MARKET" && r.Kind != "LIMIT") {
		return Order{}, errors.New("invalid side, order type, or lots (1..100)")
	}
	if !finite(r.Limit) || r.Limit < 0 || r.Limit > 1e7 {
		return Order{}, errors.New("invalid limit price")
	}
	e.mu.Lock()
	for _, o := range e.state.Orders {
		if o.ID == r.ID {
			e.mu.Unlock()
			return duplicate(o, r)
		}
	}
	c, ok := e.contracts[r.Key]
	e.mu.Unlock()
	if !ok && validKey(r.Underlying) {
		cs, err := e.provider.Contracts(ctx, r.Underlying)
		if err != nil {
			return Order{}, err
		}
		e.register(cs)
		e.mu.Lock()
		c, ok = e.contracts[r.Key]
		e.mu.Unlock()
	}
	if !ok || (c.Type != "CE" && c.Type != "PE") || c.Lot <= 0 || c.Lot > 100000 {
		return Order{}, errors.New("select a valid NSE/BSE option contract from the chain")
	}
	if expired(c, time.Now()) {
		return Order{}, errors.New("contract has expired; settlement is not simulated")
	}
	o := Order{ID: r.ID, Contract: c, Side: r.Side, Kind: r.Kind, Qty: r.Lots * c.Lot, Limit: money(r.Limit), Status: "PENDING", Created: time.Now()}
	if o.Kind == "LIMIT" {
		if math.Abs(r.Limit*100-float64(o.Limit)) > 0.000001 {
			return Order{}, errors.New("limit price must use whole paise")
		}
		tick := int64(math.Round(c.Tick))
		if tick <= 0 {
			return Order{}, errors.New("contract tick size unavailable")
		}
		if o.Limit <= 0 || o.Limit%tick != 0 {
			return Order{}, fmt.Errorf("limit must be a multiple of ₹%.2f", float64(tick)/100)
		}
	}
	status, err := e.provider.Status(ctx, c.Exchange)
	if err != nil {
		return Order{}, err
	}
	if status != "NORMAL_OPEN" {
		return Order{}, errors.New("exchange is not in its normal trading session")
	}
	quotes, err := e.provider.Quotes(ctx, []string{c.Key})
	if err != nil {
		return Order{}, err
	}
	q := quotes[c.Key]
	price, fillErr := executionPrice(o, q)
	if o.Kind == "MARKET" && fillErr != nil {
		return Order{}, fillErr
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, existing := range e.state.Orders {
		if existing.ID == r.ID {
			return duplicate(existing, r)
		}
	}
	if len(e.state.Orders) >= 100000 {
		return Order{}, errors.New("ledger limit reached; archive this account before adding more orders")
	}
	s := clone(e.state)
	shouldFill := fillErr == nil && (o.Kind == "MARKET" || (o.Side == "BUY" && price <= o.Limit) || (o.Side == "SELL" && price >= o.Limit))
	if shouldFill {
		if err = e.applyFill(&s, &o, price); err != nil {
			return Order{}, err
		}
	}
	s.Orders = append(s.Orders, o)
	if o.Status == "PENDING" && s.Cash-e.margin(s)-e.reserved(s, "") < 0 {
		return Order{}, errors.New("insufficient paper buying power to reserve this limit order")
	}
	if err = e.save(s); err != nil {
		return Order{}, errors.New("could not persist order; nothing was committed")
	}
	e.quotes[c.Key] = q
	return o, nil
}
func duplicate(o Order, r OrderRequest) (Order, error) {
	if o.Contract.Key != r.Key || o.Side != r.Side || o.Kind != r.Kind || o.Qty != r.Lots*o.Contract.Lot || o.Limit != money(r.Limit) {
		return Order{}, errors.New("order id was already used for a different request")
	}
	return o, nil
}
func (e *Engine) Cancel(id string) (Order, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := clone(e.state)
	for i, o := range s.Orders {
		if o.ID == id {
			if o.Status != "PENDING" {
				return o, errors.New("only pending orders can be cancelled")
			}
			o.Status = "CANCELLED"
			s.Orders[i] = o
			if err := e.save(s); err != nil {
				return Order{}, err
			}
			return o, nil
		}
	}
	return Order{}, errors.New("order not found")
}
func (e *Engine) Refresh(ctx context.Context) {
	e.mu.Lock()
	keys := map[string]bool{}
	exchanges := map[string]bool{}
	for k, p := range e.state.Positions {
		keys[k] = true
		exchanges[p.Contract.Exchange] = true
	}
	for _, o := range e.state.Orders {
		if o.Status == "PENDING" {
			keys[o.Contract.Key] = true
			exchanges[o.Contract.Exchange] = true
		}
	}
	e.mu.Unlock()
	list := []string{}
	for k := range keys {
		list = append(list, k)
	}
	if len(list) == 0 {
		return
	}
	qs, err := e.provider.Quotes(ctx, list)
	statuses := map[string]string{}
	if err == nil {
		for ex := range exchanges {
			status, statusErr := e.provider.Status(ctx, ex)
			if statusErr == nil {
				statuses[ex] = status
			} else {
				err = statusErr
			}
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.feedError = ""
	if err != nil {
		e.feedError = err.Error()
	}
	for k, q := range qs {
		e.quotes[k] = q
	}
	if err == nil {
		e.lastRefresh = time.Now()
	}
	s := clone(e.state)
	changed := false
	now := time.Now()
	for i := range s.Orders {
		o := s.Orders[i]
		if o.Status != "PENDING" {
			continue
		}
		// DAY orders expire even when data is unavailable or after a server restart.
		end := time.Date(o.Created.In(ist).Year(), o.Created.In(ist).Month(), o.Created.In(ist).Day(), 15, 30, 0, 0, ist)
		if expired(o.Contract, now) || (e.cfg.Mode != "demo" && !now.Before(end)) {
			o.Status = "EXPIRED"
			o.Reason = "DAY validity or contract expiry elapsed"
			s.Orders[i] = o
			changed = true
			continue
		}
		if err != nil || statuses[o.Contract.Exchange] != "NORMAL_OPEN" {
			continue
		}
		price, fillErr := executionPrice(o, qs[o.Contract.Key])
		if fillErr != nil {
			continue
		}
		if (o.Side == "BUY" && price > o.Limit) || (o.Side == "SELL" && price < o.Limit) {
			continue
		}
		if fillErr = e.applyFill(&s, &o, price); fillErr != nil {
			o.Status = "REJECTED"
			o.Reason = fillErr.Error()
		}
		s.Orders[i] = o
		changed = true
	}
	if changed {
		if err := e.save(s); err != nil {
			e.feedError = "Ledger write failed; pending fills were not committed"
		}
	}
}

type PositionView struct {
	Position
	Mark    float64 `json:"mark"`
	Average float64 `json:"average"`
	PnL     float64 `json:"pnl"`
	Stale   bool    `json:"stale"`
	Expired bool    `json:"expired"`
}

func (e *Engine) Snapshot() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.state
	positions := []PositionView{}
	var value, unreal int64
	allFresh := true
	for k, p := range s.Positions {
		q := e.quotes[k]
		avg := float64(p.Cost) / float64(abs(p.Qty)) / 100
		mark := q.LTP
		if !finite(mark) || mark <= 0 {
			mark = avg
		}
		stale := !quoteOK(q, time.Now())
		if stale {
			allFresh = false
		}
		v := money(mark) * int64(p.Qty)
		basis := p.Cost
		if p.Qty < 0 {
			basis = -basis
		}
		pl := v - basis
		value += v
		unreal += pl
		positions = append(positions, PositionView{p, mark, avg, float64(pl) / 100, stale, expired(p.Contract, time.Now())})
	}
	sort.Slice(positions, func(i, j int) bool { return positions[i].Contract.Symbol < positions[j].Contract.Symbol })
	orders := append([]Order{}, s.Orders...)
	sort.SliceStable(orders, func(i, j int) bool { return orders[i].Created.After(orders[j].Created) })
	return map[string]any{"initial": float64(s.Initial) / 100, "cash": float64(s.Cash) / 100, "equity": float64(s.Cash+value) / 100, "realized": float64(s.Realized) / 100, "unrealized": float64(unreal) / 100, "fees": float64(s.Fees) / 100, "totalPnL": float64(s.Realized+unreal-s.Fees) / 100, "margin": float64(e.margin(s)) / 100, "reserved": float64(e.reserved(s, "")) / 100, "buyingPower": float64(s.Cash-e.margin(s)-e.reserved(s, "")) / 100, "positions": positions, "orders": orders, "marksFresh": allFresh, "feedError": e.feedError, "lastRefresh": e.lastRefresh}
}
