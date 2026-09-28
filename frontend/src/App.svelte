<script>
  import { onMount } from 'svelte';
  import { Activity, ArrowDownLeft, ArrowUpRight, BarChart3, BookOpen, Check, ChevronDown, ChevronRight, CircleHelp, Download, Layers, LayoutDashboard, ListOrdered, Search, Settings2, ShieldCheck, Wallet, X, Zap, RefreshCw, ExternalLink, TrendingUp } from '@lucide/svelte';
  import PriceChart from './PriceChart.svelte';

  let config = { mode: 'live', connected: false, pollSeconds: 3, marginPerLot: 150000, feePerOrder: 0 };
  let account = { initial: 1000000, cash: 1000000, equity: 1000000, buyingPower: 1000000, realized: 0, unrealized: 0, totalPnL: 0, fees: 0, margin: 0, reserved: 0, positions: [], orders: [], marksFresh: true };
  let page = 'desk', tableTab = 'positions', underlying = 'NSE_INDEX|Nifty 50', underlyingName = 'NIFTY 50';
  let contracts = [], expiries = [], expiry = '', rows = [], selectedKey = '', candles = [], chartTarget = 'underlying';
  let side = 'BUY', kind = 'MARKET', lots = 1, limit = '', busy = false, loading = true, refreshing = false;
  let error = '', accountError = '', chartError = '', ticketError = '', toast = '', settings = false, search = '', searchResults = [], searching = false, searchError = '';
  let status = 'NOT_CONNECTED', receivedAt = '', clock = Date.now(), showAll = false, generation = 0, chartGeneration = 0, lastChartFetch = 0;
  let retryOrder = null;
  const exitRetries = new Map();
  const currency = (n, digits = 2) => new Intl.NumberFormat('en-IN', { style: 'currency', currency: 'INR', minimumFractionDigits: digits, maximumFractionDigits: digits }).format(n || 0);
  const number = (n, digits = 2) => new Intl.NumberFormat('en-IN', { maximumFractionDigits: digits, minimumFractionDigits: digits }).format(n || 0);
  const compact = n => n >= 1e7 ? `${(n / 1e7).toFixed(1)}Cr` : n >= 1e5 ? `${(n / 1e5).toFixed(1)}L` : n >= 1000 ? `${(n / 1000).toFixed(1)}K` : number(n, 0);
  const dateLabel = s => s ? new Date(`${s.slice(0, 10)}T12:00:00+05:30`).toLocaleDateString('en-IN', { day: 'numeric', month: 'short' }) : '—';
  const timeLabel = s => new Date(s).toLocaleTimeString('en-IN', { timeZone: 'Asia/Kolkata', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });

  async function api(path, body) {
    const res = await fetch(`/api/${path}`, { ...(body ? { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Optiondesk-Request': 'paper' }, body: JSON.stringify(body) } : {}), signal: AbortSignal.timeout(40000) });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || 'Request failed');
    return data;
  }
  $: spot = rows[0]?.underlying_spot_price || 0;
  $: selected = contracts.find(c => c.instrument_key === selectedKey);
  $: selectedLeg = rows.flatMap(r => [r.call_options, r.put_options]).find(l => l?.instrument_key === selectedKey);
  $: atm = rows.length ? rows.reduce((a, b) => Math.abs(b.strike_price - spot) < Math.abs(a.strike_price - spot) ? b : a).strike_price : 0;
  $: atmIndex = rows.findIndex(r => r.strike_price === atm);
  $: visibleRows = showAll ? rows : rows.slice(Math.max(0, atmIndex - 5), atmIndex + 6);
  $: qty = (selected?.lot_size || 0) * Number(lots || 0);
  $: ticketPrice = kind === 'LIMIT' ? Number(limit) : (side === 'BUY' ? selectedLeg?.market_data?.ask_price : selectedLeg?.market_data?.bid_price) || 0;
  $: premium = qty * ticketPrice;
  $: age = receivedAt ? Math.max(0, Math.floor((clock - new Date(receivedAt).getTime()) / 1000)) : null;
  $: chainFresh = age !== null && age < 15 && !error;
  $: callOI = rows.reduce((n, r) => n + (r.call_options?.market_data?.oi || 0), 0);
  $: putOI = rows.reduce((n, r) => n + (r.put_options?.market_data?.oi || 0), 0);
  $: pending = account.orders.filter(o => o.status === 'PENDING').length;
  $: heading = page === 'desk' ? 'Your edge starts with practice.' : page === 'positions' ? 'A clear view of your exposure.' : 'Every decision, on the record.';
  $: chartKey = chartTarget === 'option' && selected ? selected.instrument_key : underlying;

  async function loadAccount() {
    try { account = await api('account'); accountError = ''; } catch (e) { accountError = e.message; }
  }
  async function loadMarket() {
    const gen = ++generation; loading = true; error = ''; rows = []; candles = []; selectedKey = ''; contracts = []; expiries = []; expiry = ''; receivedAt = ''; status = 'NOT_CONNECTED'; ++chartGeneration;
    try {
      const result = await api(`contracts?underlying=${encodeURIComponent(underlying)}`);
      if (gen !== generation) return;
      contracts = result.contracts; expiries = result.expiries; expiry = expiries[0] || '';
      if (!expiry) { error = 'No active option contracts were returned for this underlying.'; return; }
      await refreshChain(gen); await loadChart();
    } catch (e) { if (gen === generation) error = e.message; }
    finally { if (gen === generation) loading = false; }
  }
  async function refreshChain(gen = generation) {
    if (!expiry) return;
    const requestedExpiry = expiry;
    try {
      const data = await api(`chain?underlying=${encodeURIComponent(underlying)}&expiry=${expiry}`);
      if (gen !== generation || expiry !== requestedExpiry) return;
      rows = data.rows || []; status = data.status; receivedAt = data.receivedAt; error = data.statusError || '';
      if (!selectedKey && rows.length) {
        const nearest = rows.reduce((a, b) => Math.abs(b.strike_price - b.underlying_spot_price) < Math.abs(a.strike_price - a.underlying_spot_price) ? b : a);
        selectedKey = nearest.call_options.instrument_key;
      }
    } catch (e) { if (gen === generation && expiry === requestedExpiry) { error = e.message; status = 'UNAVAILABLE'; } }
  }
  async function loadChart() {
    const gen = ++chartGeneration;
    const key = chartTarget === 'option' && selectedKey ? selectedKey : underlying;
    lastChartFetch = Date.now(); chartError = '';
    try { const data = await api(`candles?key=${encodeURIComponent(key)}`); if (gen === chartGeneration) candles = data; }
    catch (e) { if (gen === chartGeneration) { candles = []; chartError = e.message; } }
  }
  async function refresh() {
    if (refreshing) return;
    refreshing = true;
    try { await Promise.all([loadAccount(), !loading && config.connected ? refreshChain() : Promise.resolve()]); if (!loading && config.connected && Date.now() - lastChartFetch > 15000) await loadChart(); }
    finally { refreshing = false; }
  }
  async function chooseUnderlying(key, name) { underlying = key; underlyingName = name; search = ''; searchResults = []; chartTarget = 'underlying'; await loadMarket(); }
  async function changeExpiry() { ++generation; ++chartGeneration; rows = []; selectedKey = ''; receivedAt = ''; candles = []; loading = true; try { await refreshChain(); await loadChart(); } finally { loading = false; } }
  function selectLeg(key) { selectedKey = key; ticketError = ''; retryOrder = null; if (chartTarget === 'option') { candles = []; loadChart(); } }
  async function runSearch() { if (search.trim().length < 2) return; const query = search.trim(); searching = true; searchError = ''; try { const result = await api(`search?q=${encodeURIComponent(query)}`); if (search.trim() === query) searchResults = result; if (!result.length) searchError = 'No matching underlyings found.'; } catch (e) { searchError = e.message; } finally { searching = false; } }
  async function submit() {
    if (busy || !selected) return;
    busy = true; ticketError = '';
    const payload = { key: selectedKey, underlying, side, kind, lots: Number(lots), limit: kind === 'LIMIT' ? Number(limit) : 0 };
    const signature = JSON.stringify(payload);
    if (!retryOrder || retryOrder.signature !== signature) retryOrder = { signature, id: crypto.randomUUID() };
    try { const result = await api('orders', { ...payload, id: retryOrder.id }); notify(result.status === 'FILLED' ? `Paper ${side.toLowerCase()} filled at ${currency(result.fillPaise / 100)}` : 'Limit order added to your paper order book'); retryOrder = null; await loadAccount(); }
    catch (e) { ticketError = e.message; }
    finally { busy = false; }
  }
  async function cancel(id) { try { await api(`orders/${encodeURIComponent(id)}/cancel`, {}); notify('Paper order cancelled'); await loadAccount(); } catch (e) { notify(e.message); } }
  async function exitPosition(p) {
    if (busy || !confirm(`Close all ${Math.abs(p.qty)} units of ${p.contract.trading_symbol} with a simulated market order?`)) return;
    busy = true;
    const signature = `${p.contract.instrument_key}:${p.qty}:${p.costPaise}`;
    if (!exitRetries.has(signature)) exitRetries.set(signature, crypto.randomUUID());
    try { await api('orders', { id: exitRetries.get(signature), key: p.contract.instrument_key, underlying: p.contract.underlying_key, side: p.qty > 0 ? 'SELL' : 'BUY', kind: 'MARKET', lots: Math.abs(p.qty) / p.contract.lot_size, limit: 0 }); exitRetries.delete(signature); notify('Paper position closed'); await loadAccount(); }
    catch (e) { notify(e.message); await loadAccount(); } finally { busy = false; }
  }
  function focusModal(node) {
    const previous = document.activeElement;
    node.focus();
    const keydown = e => {
      if (e.key === 'Escape') { settings = false; return; }
      if (e.key !== 'Tab') return;
      const items = [...node.querySelectorAll('button:not(:disabled), a[href], input:not(:disabled), select:not(:disabled)')];
      const first = items[0], last = items[items.length - 1];
      if (e.shiftKey && (document.activeElement === first || document.activeElement === node)) { e.preventDefault(); last?.focus(); }
      else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first?.focus(); }
    };
    node.addEventListener('keydown', keydown);
    return { destroy() { node.removeEventListener('keydown', keydown); previous?.focus(); } };
  }
  function notify(message) { toast = message; setTimeout(() => { if (toast === message) toast = ''; }, 6000); }
  onMount(() => {
    let timer, clockTimer, active = true;
    (async () => { try { config = await api('config'); await loadAccount(); if (config.connected) await loadMarket(); else { loading = false; error = 'Connect your Upstox token to start receiving market data.'; } } catch (e) { error = e.message; loading = false; } if (active) timer = setInterval(refresh, config.pollSeconds * 1000); })();
    clockTimer = setInterval(() => clock = Date.now(), 1000);
    return () => { active = false; clearInterval(timer); clearInterval(clockTimer); };
  });
</script>

<svelte:head><title>Optiondesk · {page === 'desk' ? 'Trading desk' : page === 'positions' ? 'Positions' : 'Order book'}</title></svelte:head>

<div class="app-shell">
  <aside class="sidebar">
    <a class="brand" href="/" onclick={e => { e.preventDefault(); page = 'desk'; }} aria-label="Optiondesk home"><span class="brand-mark"><Activity size={23} strokeWidth={2.4}/></span>optiondesk<span class="brand-dot">.</span></a>
    <div class="workspace-label">YOUR WORKSPACE</div>
    <nav aria-label="Main navigation">
      <button class:active={page === 'desk'} onclick={() => page = 'desk'}><LayoutDashboard size={18}/> Trading desk</button>
      <button class:active={page === 'positions'} onclick={() => page = 'positions'}><Layers size={18}/> Positions <span class="nav-count">{account.positions.length}</span></button>
      <button class:active={page === 'orders'} onclick={() => page = 'orders'}><ListOrdered size={18}/> Order book {#if pending}<span class="nav-count">{pending}</span>{/if}</button>
    </nav>
    <div class="sidebar-note"><span class="note-icon"><ShieldCheck size={20}/></span><strong>Real markets.<br/>Room to learn.</strong><p>Build conviction with virtual capital. Every trade stays here.</p><span class="small-pill">100% PAPER TRADING</span></div>
    <div class="sidebar-bottom"><button onclick={() => settings = true}><Settings2 size={18}/> Workspace settings</button><a href="https://upstox.com/developer/api-documentation/option-chain/" target="_blank" rel="noreferrer"><CircleHelp size={18}/> Market data guide <ExternalLink size={12}/></a><div class="local-account"><span>PT</span><div><strong>Personal workspace</strong><small>Local paper account</small></div><ShieldCheck size={16}/></div></div>
  </aside>

  <div class="workspace">
    <header class="topbar"><div class="breadcrumb">Workspace <ChevronRight size={13}/> <strong>{page === 'desk' ? 'Trading desk' : page === 'positions' ? 'Positions' : 'Order book'}</strong></div><div class="topbar-right"><span class="paper-badge"><span></span> Paper account</span><span class="topbar-divider"></span><span class="clock">{new Date(clock).toLocaleTimeString('en-GB', { timeZone: 'Asia/Kolkata', hour: '2-digit', minute: '2-digit' })} <small>IST</small></span><button class="avatar" onclick={() => settings = true} aria-label="Account settings">PT</button></div></header>
    <main>
      <div class="page-heading"><div><div class="eyebrow">THE OPTIONS WORKSPACE</div><h1>{heading}</h1><p>Indian options. Live perspective. Zero capital at risk.</p></div><div class:demo={config.mode === 'demo'} class="connection"><span class:connected={config.connected && chainFresh}></span>{config.mode === 'demo' ? 'DEMO · synthetic data' : !config.connected ? 'Data not connected' : chainFresh ? 'Upstox · live polling' : 'Waiting for data'}</div></div>
      {#if config.mode === 'demo'}<div class="banner demo-banner"><Zap size={16}/><span><strong>Demo workspace</strong> — all prices and charts are synthetic. Your live-data paper account is stored separately.</span></div>{/if}
      {#if error || accountError || account.feedError}<div class="banner"><Activity size={17}/><span>{accountError || error || account.feedError}</span>{#if !config.connected}<button onclick={() => settings = true}>Connect data <ArrowUpRight size={14}/></button>{:else}<button onclick={() => !expiry ? loadMarket() : refresh()}>Retry <RefreshCw size={13}/></button>{/if}</div>{/if}
      <section class="metrics" aria-label="Account summary">
        <div class="metric"><div class="metric-label">Portfolio value <Wallet size={15}/></div><strong>{currency(account.equity)}</strong><small>Started with {currency(account.initial, 0)}</small></div>
        <div class="metric"><div class="metric-label">Total P&L <TrendingUp size={15}/></div><strong class:positive={account.totalPnL >= 0} class:negative={account.totalPnL < 0}>{account.totalPnL >= 0 ? '+' : ''}{currency(account.totalPnL)}</strong><small><span class:positive={account.totalPnL >= 0} class:negative={account.totalPnL < 0}>{account.totalPnL >= 0 ? '+' : ''}{number(account.totalPnL / account.initial * 100)}%</span> all time{!account.marksFresh ? ' · stale marks' : ''}</small></div>
        <div class="metric"><div class="metric-label">Available to trade <ArrowUpRight size={15}/></div><strong>{currency(account.buyingPower)}</strong><small>{currency(account.margin + account.reserved, 0)} reserved</small></div>
        <div class="metric"><div class="metric-label">Open positions <Layers size={15}/></div><strong>{String(account.positions.length).padStart(2, '0')} <span class="metric-unit">positions</span></strong><small>Unrealized <span class:positive={account.unrealized >= 0} class:negative={account.unrealized < 0}>{currency(account.unrealized)}</span></small></div>
      </section>

      {#if page === 'desk'}
      <div class="desk-layout">
        <div class="market-column">
          <section class="panel chart-panel">
            <div class="market-toolbar"><div class="underlyings"><button class:chosen={underlying === 'NSE_INDEX|Nifty 50'} onclick={() => chooseUnderlying('NSE_INDEX|Nifty 50', 'NIFTY 50')}>NIFTY 50</button><button class:chosen={underlying === 'NSE_INDEX|Nifty Bank'} onclick={() => chooseUnderlying('NSE_INDEX|Nifty Bank', 'BANK NIFTY')}>BANK NIFTY</button><button class:chosen={underlying === 'BSE_INDEX|SENSEX'} onclick={() => chooseUnderlying('BSE_INDEX|SENSEX', 'SENSEX')}>SENSEX</button></div><form class="search-box" onsubmit={e => { e.preventDefault(); runSearch(); }}><Search size={15}/><input aria-label="Search stock or index" placeholder="Search stocks…" bind:value={search}/><button type="submit" aria-label="Run search"><ChevronRight size={14}/></button></form></div>
            {#if searchResults.length || searching || searchError}<div class="search-results"><div class="search-results-head"><span>{searching ? 'Searching…' : searchError || 'Select an underlying'}</span><button aria-label="Close search" onclick={() => {searchResults = []; searchError = '';}}><X size={14}/></button></div>{#each searchResults as result}<button onclick={() => chooseUnderlying(result.instrument_key, result.trading_symbol || result.name)}><strong>{result.trading_symbol || result.name}</strong><span>{result.name} · {result.exchange || 'NSE'}</span><ChevronRight size={14}/></button>{/each}</div>{/if}
            <div class="chart-heading"><div><h2>{chartTarget === 'option' && selected ? selected.trading_symbol : underlyingName}<span class="exchange-tag">{underlying.startsWith('BSE') ? 'BSE' : 'NSE'}</span></h2><div class="spot-price">{chartTarget === 'option' ? currency(selectedLeg?.market_data?.ltp) : spot ? number(spot) : '—'} <span class="quote-label">{chartTarget === 'option' ? 'Option premium' : 'Underlying spot'}</span></div></div><div class="chart-controls"><div class="segmented small"><button class:chosen={chartTarget === 'underlying'} onclick={() => { chartTarget = 'underlying'; candles = []; loadChart(); }}>Index / stock</button><button disabled={!selected} class:chosen={chartTarget === 'option'} onclick={() => { chartTarget = 'option'; candles = []; loadChart(); }}>Option</button></div><span class="interval">1m</span></div></div>
            <div class="chart-container"><PriceChart {candles} identity={chartKey}/>{#if !candles.length}<div class="chart-empty"><BarChart3 size={29} strokeWidth={1.2}/><strong>{loading ? 'Loading market perspective…' : config.connected ? 'No candles available' : 'Your market perspective starts here'}</strong><span>{chartError || (config.connected ? 'Intraday candles appear when the provider has data.' : 'Connect your token to view the intraday chart.')}</span></div>{/if}</div>
            <div class="chart-footer"><span><span class="tiny-dot"></span> {status === 'NORMAL_OPEN' ? 'Normal session' : status === 'NOT_CONNECTED' ? 'Awaiting connection' : status.replaceAll('_', ' ').toLowerCase()}</span><span>1-minute candles · IST <a href="https://www.tradingview.com/" target="_blank" rel="noreferrer">Charts by TradingView ↗</a></span></div>
          </section>

          <section class="panel chain-panel"><div class="panel-heading"><div><h2>Option chain <span class="muted-count">{rows.length} strikes</span></h2><p>Select a premium to build your paper trade.</p></div><label class="expiry-select"><span>Expiry</span><select aria-label="Option expiry" bind:value={expiry} onchange={changeExpiry} disabled={!expiries.length}>{#if !expiries.length}<option value="">Select expiry</option>{/if}{#each expiries as exp}<option value={exp}>{dateLabel(exp)} {exp.slice(0,4)}</option>{/each}</select><ChevronDown size={13}/></label></div>
            <div class="chain-summary"><span><i class="legend-call"></i> Calls</span><span>PCR <strong>{callOI ? number(putOI / callOI) : '—'}</strong><span class="summary-divider">|</span> Spot <strong>{spot ? number(spot) : '—'}</strong></span><span>Puts <i class="legend-put"></i></span></div>
            <div class="chain-scroll"><table class="chain"><thead><tr><th>OI</th><th>IV %</th><th class="premium-heading">CALL LTP</th><th class="strike-col">STRIKE</th><th class="premium-heading">PUT LTP</th><th>IV %</th><th>OI</th></tr></thead><tbody>{#each visibleRows as row}<tr class:atm={row.strike_price === atm}><td class:itm-call={row.strike_price < spot}>{compact(row.call_options?.market_data?.oi || 0)}</td><td class:itm-call={row.strike_price < spot}>{number(row.call_options?.option_greeks?.iv || 0, 1)}</td><td class:itm-call={row.strike_price < spot}><button class="premium call" class:selected={selectedKey === row.call_options?.instrument_key} onclick={() => selectLeg(row.call_options.instrument_key)} aria-label={`Select ${row.strike_price} call`}>{number(row.call_options?.market_data?.ltp)} <ArrowUpRight size={11}/></button></td><td class="strike-col"><strong>{number(row.strike_price, 0)}</strong>{#if row.strike_price === atm}<span class="atm-tag">ATM</span>{/if}</td><td class:itm-put={row.strike_price > spot}><button class="premium put" class:selected={selectedKey === row.put_options?.instrument_key} onclick={() => selectLeg(row.put_options.instrument_key)} aria-label={`Select ${row.strike_price} put`}>{number(row.put_options?.market_data?.ltp)} <ArrowUpRight size={11}/></button></td><td class:itm-put={row.strike_price > spot}>{number(row.put_options?.option_greeks?.iv || 0, 1)}</td><td class:itm-put={row.strike_price > spot}>{compact(row.put_options?.market_data?.oi || 0)}</td></tr>{/each}</tbody></table>{#if !rows.length}<div class="empty-state chain-empty"><Layers size={26} strokeWidth={1.3}/><strong>{loading ? 'Finding option contracts…' : 'The next trade starts with a good view.'}</strong><p>{config.connected ? 'Choose an underlying and expiry to explore available contracts.' : 'Live strikes, premiums, open interest and Greeks will appear once connected.'}</p></div>{/if}</div>
            <div class="panel-footer"><span>{age !== null ? `Received ${age}s ago · ${config.pollSeconds}s polling` : 'Waiting for market data'}{age !== null && !chainFresh ? ' · STALE' : ''}</span><button onclick={() => showAll = !showAll}>{showAll ? 'Near ATM' : 'All strikes'} <ChevronDown size={12}/></button></div>
          </section>
        </div>

        <aside class="ticket-column"><section class="panel ticket"><div class="ticket-heading"><h2>Order ticket</h2><span class="outline-pill">PAPER</span></div>
          <div class="selected-contract"><span class="contract-icon"><Layers size={20}/></span><div><strong>{selected ? `${number(selected.strike_price, 0)} ${selected.instrument_type}` : 'Select an option'}</strong><small>{selected ? `${selected.trading_symbol.split(' ')[0]} · ${dateLabel(selected.expiry)} expiry` : 'Choose a premium from the chain'}</small></div>{#if selected}<span class:put-tag={selected.instrument_type === 'PE'} class="type-tag">{selected.instrument_type === 'CE' ? 'CALL' : 'PUT'}</span>{/if}</div>
          <div class="side-switch"><button class:buy-active={side === 'BUY'} onclick={() => side = 'BUY'}>Buy <ArrowDownLeft size={14}/></button><button class:sell-active={side === 'SELL'} onclick={() => side = 'SELL'}>Sell <ArrowUpRight size={14}/></button></div>
          <div class="ticket-label">Order type</div><div class="segmented order-types"><button class:chosen={kind === 'MARKET'} onclick={() => kind = 'MARKET'}>Market</button><button class:chosen={kind === 'LIMIT'} onclick={() => {kind = 'LIMIT'; if (!limit) limit = selectedLeg?.market_data?.ltp || '';}}>Limit</button></div>
          <div class="ticket-label quantity-label"><label for="lots">Quantity <span>in lots</span></label><span>{selected?.lot_size || '—'} units / lot</span></div><div class="quantity"><button aria-label="Decrease lots" disabled={Number(lots) <= 1} onclick={() => lots = Math.max(1, Number(lots) - 1)}>−</button><input id="lots" type="number" min="1" max="100" step="1" bind:value={lots}/><button aria-label="Increase lots" disabled={Number(lots) >= 100} onclick={() => lots = Math.min(100, Number(lots) + 1)}>+</button></div>
          <label class="ticket-label price-label" for="price">{kind === 'LIMIT' ? 'Limit price' : 'Indicative fill price'}<span>INR</span></label><div class="price-input"><span>₹</span><input id="price" type="number" min="0" step={selected?.tick_size ? selected.tick_size / 100 : 0.05} value={kind === 'MARKET' ? (ticketPrice ? ticketPrice.toFixed(2) : '') : limit} oninput={e => limit = e.currentTarget.value} disabled={kind === 'MARKET'} placeholder="—"/>{#if kind === 'MARKET'}<small>{side === 'BUY' ? 'ASK' : 'BID'}</small>{/if}</div>
          <div class="quote-pair"><span>Bid <strong>{selectedLeg ? number(selectedLeg.market_data.bid_price) : '—'}</strong></span><span>Ask <strong>{selectedLeg ? number(selectedLeg.market_data.ask_price) : '—'}</strong></span></div>
          <div class="trade-estimate"><div><span>Quantity</span><strong>{number(qty, 0)} units</strong></div><div><span>Premium {side === 'BUY' ? 'debit' : 'credit'}</span><strong>{currency(premium)}</strong></div><div><span>Simulated fee</span><strong>{currency(config.feePerOrder)}</strong></div>{#if side === 'SELL'}<div><span>Reserve / new short lot</span><strong>{currency(config.marginPerLot, 0)}</strong></div>{/if}<div class="estimate-total"><span>Available funds</span><strong>{currency(account.buyingPower)}</strong></div></div>
          {#if ticketError}<div class="ticket-error" role="alert">{ticketError}</div>{/if}
          <button class="submit-order" class:sell-submit={side === 'SELL'} disabled={busy || !selected || !chainFresh || status !== 'NORMAL_OPEN' || !Number.isInteger(Number(lots)) || lots < 1 || lots > 100 || (kind === 'LIMIT' && !(Number(limit) > 0))} onclick={submit}>{#if busy}<RefreshCw size={16} class="spinning"/> Submitting…{:else}{side === 'BUY' ? 'Buy' : 'Sell'} paper option <ArrowUpRight size={17}/>{/if}</button>
          <div class="ticket-disclaimer"><ShieldCheck size={13}/><span>Simulated execution. No real orders.</span></div><p class="execution-note">{kind === 'LIMIT' ? 'DAY limit order. Fills only at your limit or better.' : 'Fills at a fresh ask (buy) or bid (sell), subject to available top-of-book quantity.'} {side === 'SELL' ? 'Short reserves are a simplified paper model.' : ''}</p>
        </section>
        <section class="panel greeks-panel"><h3>Under the premium <span>GREEKS</span></h3><div class="greeks-grid">{#each [['Delta', 'delta', 3], ['Theta', 'theta', 2], ['Gamma', 'gamma', 4], ['Vega', 'vega', 2]] as [name, key, digits]}<div><span>{name}</span><strong>{selectedLeg ? number(selectedLeg.option_greeks?.[key], digits) : '—'}</strong></div>{/each}</div><div class="iv-line"><span>Implied volatility</span><strong>{selectedLeg ? `${number(selectedLeg.option_greeks?.iv)}%` : '—'}</strong></div></section>
        <div class="practice-note"><BookOpen size={17}/><p>Trade the process.<br/><strong>Let the results teach you.</strong></p></div>
        </aside>
      </div>
      {/if}

      <section class="panel portfolio-panel"><div class="portfolio-header"><div class="portfolio-tabs"><button class:chosen={page === 'positions' || (page === 'desk' && tableTab === 'positions')} onclick={() => {tableTab = 'positions'; if (page !== 'desk') page = 'positions';}}>Positions <span>{account.positions.length}</span></button><button class:chosen={page === 'orders' || (page === 'desk' && tableTab === 'orders')} onclick={() => {tableTab = 'orders'; if (page !== 'desk') page = 'orders';}}>Orders <span>{account.orders.length}</span></button></div><a class="export-link" href="/api/export" download><Download size={14}/> Export CSV</a></div>
        {#if page === 'positions' || (page === 'desk' && tableTab === 'positions')}
          <div class="table-scroll"><table class="positions-table"><thead><tr><th>CONTRACT</th><th>QTY</th><th>AVG. PRICE</th><th>LTP</th><th>UNREALIZED P&L</th><th></th></tr></thead><tbody>{#each account.positions as p}<tr><td><strong>{p.contract.trading_symbol}</strong><small>{dateLabel(p.contract.expiry)} · {p.qty > 0 ? 'LONG' : 'SHORT'}{p.expired ? ' · EXPIRED / unsettled' : ''}</small></td><td>{p.qty}</td><td>{currency(p.average)}</td><td>{currency(p.mark)}{#if p.stale}<small class="stale-label">Stale / indicative</small>{/if}</td><td class:positive={p.pnl >= 0} class:negative={p.pnl < 0}>{p.pnl >= 0 ? '+' : ''}{currency(p.pnl)}</td><td><button class="exit-button" disabled={busy || p.expired} onclick={() => exitPosition(p)}>Exit <ArrowUpRight size={12}/></button></td></tr>{/each}</tbody></table></div>
          {#if !account.positions.length}<div class="empty-state positions-empty"><span class="empty-circle"><Layers size={22} strokeWidth={1.3}/></span><strong>A fresh slate. A world of possibilities.</strong><p>Your open paper positions will appear here. Start with a contract from the option chain.</p></div>{/if}
        {:else}
          <div class="table-scroll"><table class="positions-table"><thead><tr><th>CONTRACT / TIME (IST)</th><th>SIDE</th><th>QTY</th><th>TYPE / LIMIT</th><th>FILL</th><th>STATUS</th><th></th></tr></thead><tbody>{#each account.orders as o}<tr><td><strong>{o.contract.trading_symbol}</strong><small>{dateLabel(o.created)} · {timeLabel(o.created)}</small></td><td><span class:negative={o.side === 'SELL'} class:positive={o.side === 'BUY'}>{o.side}</span></td><td>{o.qty}</td><td>{o.kind}<small>{o.kind === 'LIMIT' ? currency(o.limitPaise / 100) : 'Bid / ask'}</small></td><td>{o.fillPaise ? currency(o.fillPaise / 100) : '—'}</td><td><span class="status-tag" class:filled={o.status === 'FILLED'} class:pending={o.status === 'PENDING'}>{o.status}</span>{#if o.reason}<small class="order-reason">{o.reason}</small>{/if}</td><td>{#if o.status === 'PENDING'}<button class="exit-button" onclick={() => cancel(o.id)}>Cancel</button>{/if}</td></tr>{/each}</tbody></table></div>
          {#if !account.orders.length}<div class="empty-state positions-empty"><span class="empty-circle"><ListOrdered size={22} strokeWidth={1.3}/></span><strong>Your trading journal starts with one decision.</strong><p>Filled, pending and cancelled paper orders will be saved here.</p></div>{/if}
        {/if}
        <div class="portfolio-footer"><span>Realized P&L <strong class:positive={account.realized >= 0} class:negative={account.realized < 0}>{currency(account.realized)}</strong></span><span>Simulated fees <strong>{currency(account.fees)}</strong></span><span class="storage-note"><ShieldCheck size={12}/> Saved locally</span></div>
      </section>
      <footer class="page-footer"><span>OPTIONDESK <span class="footer-dot">·</span> Built for deliberate practice.</span><span>Paper fills exclude taxes, slippage and settlement. {config.mode === 'demo' ? 'Synthetic demo data.' : 'Market snapshots refresh every ' + config.pollSeconds + ' seconds.'}</span></footer>
    </main>
  </div>
</div>

{#if toast}<div class="toast" role="status"><Check size={17}/>{toast}<button aria-label="Dismiss notification" onclick={() => toast = ''}><X size={15}/></button></div>{/if}
{#if settings}<div class="modal-backdrop" role="presentation"><div class="settings-modal" use:focusModal role="dialog" aria-modal="true" aria-labelledby="settings-title" tabindex="-1"><div class="modal-heading"><span class="brand-mark"><Settings2 size={22}/></span><button onclick={() => settings = false} aria-label="Close settings"><X size={20}/></button></div><div class="eyebrow">YOUR LOCAL WORKSPACE</div><h2 id="settings-title">Connect. Practice. Refine.</h2><p>Keep your token on your computer. The Go backend reads it from the root <code>.env</code> file.</p><ol><li>Copy <code>.env.example</code> to <code>.env</code>.</li><li>Set <code>UPSTOX_ACCESS_TOKEN</code> to your bearer token.</li><li>Keep <code>MARKET_DATA_MODE=live</code> and restart the server.</li></ol><div class="settings-summary"><div><span>Market data</span><strong>{config.mode === 'demo' ? 'Synthetic demo' : 'Upstox · GET only'}</strong></div><div><span>Starting capital</span><strong>{currency(account.initial, 0)}</strong></div><div><span>Short reserve / lot</span><strong>{currency(config.marginPerLot, 0)}</strong></div><div><span>Refresh interval</span><strong>{config.pollSeconds} seconds</strong></div></div><p class="settings-fine">To explore without a token, set <code>MARKET_DATA_MODE=demo</code> and restart. Demo and live-data ledgers stay separate. Capital changes apply to new ledgers only. Short reserves are configurable estimates; taxes, SPAN, exercise and expiry settlement are not simulated.</p><button class="submit-order" onclick={() => settings = false}>Back to workspace <ArrowUpRight size={16}/></button></div></div>{/if}
