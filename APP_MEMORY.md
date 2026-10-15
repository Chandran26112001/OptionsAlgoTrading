# Optiondesk: what the app does

This is the plain-language guide to the application's **current features**. It is intended for you to read and for future development sessions to maintain as features change. `README.md` remains the place for detailed setup commands, environment-variable reference, API paths and deeper technical notes.

## The idea

Optiondesk is a local workspace for practising **options trading in the Indian market**. It shows market information, lets you place pretend options trades and keeps a private record of those trades on this computer.

It never sends an order to Upstox. Buying, selling, closing and cancelling in Optiondesk changes only the app's simulated account.

## Where market data comes from

**Live mode** requests option contracts, option-chain data, market quotes, intraday candles, exchange status and instrument search from Upstox. A bearer token stays in the Go server's environment or the root `.env` file. It is not sent to the browser. If the token is missing, expired or lacks access, the app shows a connection error; it does not replace live data with made-up prices.

**Demo mode** generates sample underlyings, option contracts, premiums, Greeks, order-book quotes and candlestick prices locally. These numbers are made up for trying the interface. They are not exchange data. The interface marks demo mode clearly, and its account has its own saved ledger, separate from the live-mode account.

The chart is drawn by TradingView's Lightweight Charts library. TradingView supplies the chart display, not the prices. In live mode its candles come from Upstox; in demo mode the Go backend makes synthetic candles.

## What you can do

### Explore the market

- Open the NIFTY 50, BANK NIFTY or SENSEX shortcuts.
- Search for a stock or index on NSE or BSE and load its available option contracts.
- Choose a contract expiry and explore call (CE) and put (PE) strikes.
- View premiums, open interest, implied volatility and the available option Greeks. See the put-to-call open-interest ratio and spot price.
- Focus the chain around the at-the-money strike or show all strikes.
- View an interactive one-minute candlestick chart for an underlying or a selected option. Pan and zoom the chart.

### Observe an algorithm setup

The **Algo dashboard** sidebar view gathers the inputs in `algorithm.txt`. Select an underlying, expiry and option, then compare CE and PE open interest around ATM (seven strikes on each side plus ATM) and average IV (four on each side plus ATM). It identifies the larger side and highlights an absolute IV difference greater than two percentage points. The selected option does not move ATM: ATM follows the underlying spot price. The dashboard refreshes its snapshot every three seconds by default; price, chain and quote context can change on each snapshot, while completed one-minute-candle indicators update after a candle closes.

India VIX and **US 30 Dow futures** show the latest Upstox quote, a five-minute comparison candle, the change, and their source times. US 30 uses the verified `GLOBAL_INDEX|DOW FUTURES` instrument rather than the cash Dow Jones index. These are periodic snapshots.

VWAP, EMA 20, Supertrend and Parabolic SAR currently use completed one-minute **underlying** candles from the current session. For NIFTY 50, if Upstox's index candles omit volume, the app builds VWAP with the current official Nifty Indices 50-stock constituent list and the sum of every constituent's matching Upstox one-minute volume—the method TradingView documents for NSE index volume. The VWAP card identifies that source. An omitted constituent bar is correctly counted as a zero-trade minute, while a component with no candle history, a returned null/invalid volume, official-list failure, or provider failure withholds VWAP. It never substitutes a future, ETF, partial basket, or fabricated data. All algorithm numbers, the refresh interval and the indicator target are configurable in `.env`. Change `ALGO_INDICATOR_TARGET` to `option` to calculate all four indicators on the selected option instead. Restart after configuration changes. README documents the calculation conventions and warm-up periods.

An index has no traded volume of its own; the tested NIFTY candles supplied zero volume. For live NIFTY 50, the app therefore uses its clearly labelled 50-constituent-volume calculation where every required component candle is available; other indices still show VWAP as unavailable unless their provider supplies volume. Missing inputs, incomplete strike ranges, insufficient history, candle gaps and stale data are disclosed. There is no synthetic fallback in live mode.

The final call stays **WAIT — rules not configured** because entry rules have not yet been provided. Indicator direction is descriptive. The dashboard does not place trades, change cash, register positions or modify the ledger.

### Practise orders

- Choose an option, buy or sell it, and enter a whole number of lots.
- Submit a simulated market order or a DAY limit order. Market buys use the current ask; market sells use the current bid.
- Close an open position from the positions list. Sell to reduce or close a long position; buy to reduce or close a short one. Partial closes and reversals are supported.
- Review and cancel pending limit orders.
- Export the paper-order journal as a CSV file.

In live mode, the app verifies the option instrument, exchange status, quote freshness, tick size and displayed top-of-book quantity before it records a market fill. A missing or stale quote does not count as a valid fill. This is a simple simulator: limit orders are checked against later snapshots, and it does not reproduce the exchange's order queue or guarantee real-world execution.

## Account and results

The dashboard tracks cash, total account value, available buying power, realized profit and loss, open positions, open-order reservations and fees. Position rows show quantity, average entry cost, the latest mark and unrealized profit or loss. The journal records order side, type, quantity, price, fill and status. You can see the journal on the trading desk, or open the dedicated Positions and Order Book views.

The local account starts with **₹10 lakh** when a new ledger is created. Configuration can change the starting capital for a new account. Changing the setting does not reset or change an existing account.

Positions and orders are saved in `data/paper-live.json` for live-data paper trading and `data/paper-demo.json` for demo trading. Restarting the app does not erase them. The matching `.lock` file tells the app that a server process owns the ledger; leave it in place while that process is running.

The ticket shows a configurable simulated short-option reserve and simulated fees. These are practice settings, not a broker's live margin or complete estimate of the costs of trading.

## How fresh the information is

By default, market quotes and the option chain are requested every **three seconds**. The interval is configurable. Chart candles refresh about every **15 seconds**. This is periodic snapshot polling; the app does not use a tick-by-tick WebSocket feed. Market-data access, exchange hours and rate limits can affect what live information is available.

## Boundaries to keep in mind

- The app is for one local user and is designed to bind to the computer's loopback address.
- Paper fills are local simulations. They do not guarantee that the same order or price would fill on an exchange.
- The short-option reserve is a simple configurable estimate. It does not reproduce SPAN margin, margin offsets, margin calls or broker risk checks.
- The app does not calculate a complete trading bill. Taxes, exchange charges and brokerage schedules are not included unless you explicitly configure the flat paper fee.
- Expiry, exercise, assignment, physical settlement and automatic square-off are not simulated. An expired position is shown as unsettled; the app does not invent a settlement price.
- Stop and stop-limit orders, multi-leg atomic orders, backtesting and tick-level streaming are not current features.
- LTP is used to mark account value and open-position P&L; it may differ from the price needed to close against the bid or ask. The interface identifies stale position marks.
- Actual Upstox access depends on your token, permissions and provider availability. A live-mode screen alone is not proof that an authenticated Upstox connection succeeded.

## For the next development session

The current implementation uses a Svelte frontend, Go backend and Upstox market-data client. To run the app or find detailed configuration, see [README.md](README.md). For development rules and validation commands, see [AGENTS.md](AGENTS.md).

When adding, changing or removing a user-visible feature, update this guide in plain language as part of the same change. Keep the market-data source for live and demo modes distinct, and explain new simulation assumptions where users could mistake them for broker behaviour.

### Latest verification — 15 October 2026

Backend tests and vet passed, including exact indicator examples, NIFTY 50 aggregate-volume calculation, zero-trade-minute aggregation, strict null/invalid-component-volume rejection, ATM-range aggregation, missing OI/IV handling, warm-up/freshness rules, parameter validation and an HTTP check proving that the dashboard leaves the paper ledger unchanged and returns WAIT. Frontend check and production build passed with no errors or warnings. A read-only authenticated live check downloaded the official 50-stock constituent list, read the corresponding Upstox candles and calculated NIFTY 50 constituent-volume VWAP as 22,888.63 from 230 completed one-minute candles. The dashboard now defaults to a three-second refresh; verified constituent-volume totals are cached until the next completed minute to preserve the Upstox rate budget. Existing paper-account files were not edited by this feature work.
