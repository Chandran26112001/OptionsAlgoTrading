package main

import (
	"fmt"
	"strconv"
)

type AlgoConfig struct {
	OIWings      int     `json:"oiWings"`
	IVWings      int     `json:"ivWings"`
	Lookback     int     `json:"lookbackMinutes"`
	IVThreshold  float64 `json:"ivThreshold"`
	EMAPeriod    int     `json:"emaPeriod"`
	ATRPeriod    int     `json:"atrPeriod"`
	STMultiplier float64 `json:"stMultiplier"`
	SARStep      float64 `json:"sarStep"`
	SARMax       float64 `json:"sarMax"`
	Target       string  `json:"target"`
	MaxAge       int     `json:"maxAgeSeconds"`
	Refresh      int     `json:"refreshSeconds"`
}

func loadAlgoConfig() (AlgoConfig, error) {
	c := AlgoConfig{Target: env("ALGO_INDICATOR_TARGET", "underlying")}
	for _, v := range []struct {
		k, d     string
		p        *int
		min, max int
	}{
		{"ALGO_OI_STRIKES_EACH_SIDE", "7", &c.OIWings, 1, 50},
		{"ALGO_IV_STRIKES_EACH_SIDE", "4", &c.IVWings, 1, 50},
		{"ALGO_TREND_LOOKBACK_MINUTES", "5", &c.Lookback, 1, 60},
		{"ALGO_EMA_PERIOD", "20", &c.EMAPeriod, 2, 300},
		{"ALGO_SUPERTREND_ATR_PERIOD", "10", &c.ATRPeriod, 2, 100},
		{"ALGO_MAX_DATA_AGE_SECONDS", "180", &c.MaxAge, 60, 900},
		{"ALGO_REFRESH_SECONDS", "15", &c.Refresh, 15, 300},
	} {
		n, err := strconv.Atoi(env(v.k, v.d))
		if err != nil || n < v.min || n > v.max {
			return c, fmt.Errorf("%s must be %d..%d", v.k, v.min, v.max)
		}
		*v.p = n
	}
	for _, v := range []struct {
		k, d     string
		p        *float64
		min, max float64
	}{
		{"ALGO_IV_DIFFERENCE_THRESHOLD", "2", &c.IVThreshold, 0, 100},
		{"ALGO_SUPERTREND_MULTIPLIER", "3", &c.STMultiplier, 0.1, 20},
		{"ALGO_SAR_STEP", "0.02", &c.SARStep, 0.001, 1},
		{"ALGO_SAR_MAX", "0.2", &c.SARMax, 0.001, 1},
	} {
		n, err := strconv.ParseFloat(env(v.k, v.d), 64)
		if err != nil || !finite(n) || n < v.min || n > v.max {
			return c, fmt.Errorf("invalid %s", v.k)
		}
		*v.p = n
	}
	if c.SARStep > c.SARMax {
		return c, fmt.Errorf("ALGO_SAR_STEP must not exceed ALGO_SAR_MAX")
	}
	if c.Target != "underlying" && c.Target != "option" {
		return c, fmt.Errorf("ALGO_INDICATOR_TARGET must be underlying or option")
	}
	return c, nil
}
