package main

import "math"

// Indicators use chronological, completed one-minute candles from one session.
// EMA is SMA-seeded; Supertrend uses Wilder ATR; SAR uses Wilder acceleration.
type Technicals struct {
	Close      float64  `json:"close"`
	VWAP       *float64 `json:"vwap"`
	VWAPSource string   `json:"vwapSource"`
	VWAPIssue  string   `json:"vwapIssue"`
	EMA        *float64 `json:"ema"`
	Supertrend *float64 `json:"supertrend"`
	SAR        *float64 `json:"sar"`
	Count      int      `json:"count"`
	At         int64    `json:"at"`
}

// TechnicalPoint is one completed candle and its indicator values. Nil values
// intentionally represent an indicator warm-up period or unavailable VWAP.
type TechnicalPoint struct {
	Time       int64    `json:"time"`
	Open       float64  `json:"open"`
	High       float64  `json:"high"`
	Low        float64  `json:"low"`
	Close      float64  `json:"close"`
	VWAP       *float64 `json:"vwap"`
	EMA        *float64 `json:"ema"`
	Supertrend *float64 `json:"supertrend"`
	SAR        *float64 `json:"sar"`
}

func floatPtr(v float64) *float64 { return &v }

func technicals(cs []Candle, emaPeriod, atrPeriod int, multiplier, sarStep, sarMax float64) Technicals {
	out := Technicals{Count: len(cs)}
	if len(cs) == 0 {
		return out
	}
	out.Close = cs[len(cs)-1].Close
	out.At = cs[len(cs)-1].Time
	volume, weighted := 0.0, 0.0
	volumeComplete := true
	for _, c := range cs {
		if c.MissingVolume {
			volumeComplete = false
		}
		volume += c.Volume
		weighted += (c.High + c.Low + c.Close) / 3 * c.Volume
	}
	if volume > 0 && volumeComplete {
		v := weighted / volume
		out.VWAP = &v
	}
	if emaPeriod > 0 && len(cs) >= emaPeriod {
		v := 0.0
		for _, c := range cs[:emaPeriod] {
			v += c.Close
		}
		v /= float64(emaPeriod)
		for _, c := range cs[emaPeriod:] {
			v += (c.Close - v) * 2 / float64(emaPeriod+1)
		}
		out.EMA = &v
	}
	if atrPeriod > 0 && len(cs) >= atrPeriod {
		atr, upper, lower, trend := 0.0, 0.0, 0.0, 0.0
		for i, c := range cs {
			tr := c.High - c.Low
			if i > 0 {
				tr = math.Max(tr, math.Max(math.Abs(c.High-cs[i-1].Close), math.Abs(c.Low-cs[i-1].Close)))
			}
			if i < atrPeriod {
				atr += tr / float64(atrPeriod)
			} else {
				atr = (atr*float64(atrPeriod-1) + tr) / float64(atrPeriod)
			}
			if i < atrPeriod-1 {
				continue
			}
			bu, bl := (c.High+c.Low)/2+multiplier*atr, (c.High+c.Low)/2-multiplier*atr
			if i == atrPeriod-1 {
				upper, lower = bu, bl
				trend = upper
				continue
			}
			oldUpper := upper
			if bu < upper || cs[i-1].Close > upper {
				upper = bu
			}
			if bl > lower || cs[i-1].Close < lower {
				lower = bl
			}
			if trend == oldUpper {
				if c.Close > upper {
					trend = lower
				} else {
					trend = upper
				}
			} else {
				if c.Close < lower {
					trend = upper
				} else {
					trend = lower
				}
			}
		}
		out.Supertrend = &trend
	}
	if len(cs) >= 2 {
		rising := cs[1].Close >= cs[0].Close
		sar, ep, af := cs[0].Low, math.Max(cs[0].High, cs[1].High), sarStep
		if !rising {
			sar = cs[0].High
			ep = math.Min(cs[0].Low, cs[1].Low)
		}
		for i := 2; i < len(cs); i++ {
			c := cs[i]
			sar += af * (ep - sar)
			if rising {
				sar = math.Min(sar, math.Min(cs[i-1].Low, cs[i-2].Low))
				if c.Low < sar {
					rising = false
					sar = math.Max(ep, c.High)
					ep = c.Low
					af = sarStep
				} else if c.High > ep {
					ep = c.High
					af = math.Min(sarMax, af+sarStep)
				}
			} else {
				sar = math.Max(sar, math.Max(cs[i-1].High, cs[i-2].High))
				if c.High > sar {
					rising = true
					sar = math.Min(ep, c.Low)
					ep = c.High
					af = sarStep
				} else if c.Low < ep {
					ep = c.Low
					af = math.Min(sarMax, af+sarStep)
				}
			}
		}
		out.SAR = &sar
	}
	return out
}

// technicalSeries mirrors technicals, but retains each value for charting.
// Keeping the calculation server-side guarantees that the chart and cards use
// the same completed candles and configured parameters.
func technicalSeries(cs []Candle, emaPeriod, atrPeriod int, multiplier, sarStep, sarMax float64) []TechnicalPoint {
	out := make([]TechnicalPoint, 0, len(cs))
	volumeComplete := true
	for _, c := range cs {
		if c.MissingVolume {
			volumeComplete = false
		}
	}
	volume, weighted, ema, atr := 0.0, 0.0, 0.0, 0.0
	upper, lower, trend := 0.0, 0.0, 0.0
	rising, sarReady := false, false
	sar, ep, af := 0.0, 0.0, sarStep
	for i, c := range cs {
		point := TechnicalPoint{Time: c.Time, Open: c.Open, High: c.High, Low: c.Low, Close: c.Close}
		if volumeComplete {
			volume += c.Volume
			weighted += (c.High + c.Low + c.Close) / 3 * c.Volume
			if volume > 0 {
				v := weighted / volume
				point.VWAP = floatPtr(v)
			}
		}
		if emaPeriod > 0 && i == emaPeriod-1 {
			for _, seed := range cs[:emaPeriod] {
				ema += seed.Close
			}
			ema /= float64(emaPeriod)
			point.EMA = floatPtr(ema)
		} else if emaPeriod > 0 && i >= emaPeriod {
			ema += (c.Close - ema) * 2 / float64(emaPeriod+1)
			point.EMA = floatPtr(ema)
		}
		if atrPeriod > 0 {
			tr := c.High - c.Low
			if i > 0 {
				tr = math.Max(tr, math.Max(math.Abs(c.High-cs[i-1].Close), math.Abs(c.Low-cs[i-1].Close)))
			}
			if i < atrPeriod {
				atr += tr / float64(atrPeriod)
			} else {
				atr = (atr*float64(atrPeriod-1) + tr) / float64(atrPeriod)
			}
			if i == atrPeriod-1 {
				upper, lower = (c.High+c.Low)/2+multiplier*atr, (c.High+c.Low)/2-multiplier*atr
				trend = upper
				point.Supertrend = floatPtr(trend)
			} else if i >= atrPeriod {
				bu, bl := (c.High+c.Low)/2+multiplier*atr, (c.High+c.Low)/2-multiplier*atr
				oldUpper := upper
				if bu < upper || cs[i-1].Close > upper {
					upper = bu
				}
				if bl > lower || cs[i-1].Close < lower {
					lower = bl
				}
				if trend == oldUpper {
					if c.Close > upper {
						trend = lower
					} else {
						trend = upper
					}
				} else if c.Close < lower {
					trend = upper
				} else {
					trend = lower
				}
				point.Supertrend = floatPtr(trend)
			}
		}
		if i == 1 {
			rising, sarReady = c.Close >= cs[0].Close, true
			sar, ep = cs[0].Low, math.Max(cs[0].High, c.High)
			if !rising {
				sar, ep = cs[0].High, math.Min(cs[0].Low, c.Low)
			}
			point.SAR = floatPtr(sar)
		} else if i >= 2 && sarReady {
			sar += af * (ep - sar)
			if rising {
				sar = math.Min(sar, math.Min(cs[i-1].Low, cs[i-2].Low))
				if c.Low < sar {
					rising, sar, ep, af = false, math.Max(ep, c.High), c.Low, sarStep
				} else if c.High > ep {
					ep, af = c.High, math.Min(sarMax, af+sarStep)
				}
			} else {
				sar = math.Max(sar, math.Max(cs[i-1].High, cs[i-2].High))
				if c.High > sar {
					rising, sar, ep, af = true, math.Min(ep, c.Low), c.High, sarStep
				} else if c.Low < ep {
					ep, af = c.Low, math.Min(sarMax, af+sarStep)
				}
			}
			point.SAR = floatPtr(sar)
		}
		out = append(out, point)
	}
	return out
}
