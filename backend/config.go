package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

var ist = time.FixedZone("IST", 19800)

type Config struct {
	Token, Mode, Port, DataDir string
	Capital, Margin, Fee       int64
	Poll                       time.Duration
}

func loadConfig() (Config, error) {
	if f, err := os.Open(".env"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				return Config{}, fmt.Errorf("invalid .env entry")
			}
			k = strings.TrimSpace(k)
			v = strings.Trim(strings.TrimSpace(v), "\"'")
			if _, set := os.LookupEnv(k); !set {
				if err := os.Setenv(k, v); err != nil {
					return Config{}, err
				}
			}
		}
		if err := sc.Err(); err != nil {
			return Config{}, err
		}
	} else if !os.IsNotExist(err) {
		return Config{}, err
	}
	c := Config{Token: strings.TrimSpace(strings.TrimPrefix(os.Getenv("UPSTOX_ACCESS_TOKEN"), "Bearer ")), Mode: env("MARKET_DATA_MODE", "live"), Port: env("PORT", "8080"), DataDir: env("DATA_DIR", "data")}
	if c.Mode != "live" && c.Mode != "demo" {
		return c, fmt.Errorf("MARKET_DATA_MODE must be live or demo")
	}
	for _, item := range []struct {
		k, d string
		p    *int64
	}{{"PAPER_INITIAL_CAPITAL", "1000000", &c.Capital}, {"PAPER_SHORT_MARGIN_PER_LOT", "150000", &c.Margin}, {"PAPER_FEE_PER_ORDER", "0", &c.Fee}} {
		v, err := strconv.ParseFloat(env(item.k, item.d), 64)
		if err != nil || !finite(v) || v < 0 || v > 1e10 {
			return c, fmt.Errorf("invalid %s", item.k)
		}
		*item.p = money(v)
	}
	if c.Capital == 0 || c.Margin == 0 {
		return c, fmt.Errorf("capital and short reserve must be positive")
	}
	sec, err := strconv.Atoi(env("POLL_INTERVAL_SECONDS", "3"))
	if err != nil || sec < 3 || sec > 60 {
		return c, fmt.Errorf("poll interval must be 3..60 seconds")
	}
	c.Poll = time.Duration(sec) * time.Second
	port, err := strconv.Atoi(c.Port)
	if err != nil || port < 1024 || port > 65535 {
		return c, fmt.Errorf("invalid PORT")
	}
	return c, nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
