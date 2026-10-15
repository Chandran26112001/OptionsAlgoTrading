<script>
  import { onMount } from 'svelte';
  import { CandlestickSeries, ColorType, createChart, LineSeries } from 'lightweight-charts';

  export let points = [];
  export let identity = '';
  let element, chart, candles, vwap, ema, supertrend, sar, observer, previousIdentity = '';
  const valid = value => value != null && Number.isFinite(value);
  const lineData = field => points.filter(point => valid(point[field])).map(point => ({ time: point.time, value: point[field] }));

  function update() {
    if (!candles) return;
    candles.setData(points.map(point => ({ time: point.time, open: point.open, high: point.high, low: point.low, close: point.close })));
    vwap.setData(lineData('vwap'));
    ema.setData(lineData('ema'));
    supertrend.setData(lineData('supertrend'));
    sar.setData(lineData('sar'));
    if (identity !== previousIdentity) chart.timeScale().fitContent();
    previousIdentity = identity;
  }

  onMount(() => {
    chart = createChart(element, {
      height: 330,
      layout: { background: { type: ColorType.Solid, color: '#ffffff' }, textColor: '#88918e', fontFamily: 'Inter, Segoe UI, sans-serif', fontSize: 11, attributionLogo: true },
      grid: { vertLines: { color: '#f4f6f5' }, horzLines: { color: '#f0f3f1' } },
      rightPriceScale: { borderVisible: false },
      timeScale: { borderVisible: false, timeVisible: true, secondsVisible: false, tickMarkFormatter: time => new Date(Number(time) * 1000).toLocaleTimeString('en-GB', { timeZone: 'Asia/Kolkata', hour: '2-digit', minute: '2-digit' }) },
      localization: { timeFormatter: time => new Date(Number(time) * 1000).toLocaleString('en-IN', { timeZone: 'Asia/Kolkata', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) },
      crosshair: { vertLine: { color: '#9daaa4', labelBackgroundColor: '#1c6855' }, horzLine: { color: '#9daaa4', labelBackgroundColor: '#1c6855' } },
    });
    candles = chart.addSeries(CandlestickSeries, { upColor: '#23846c', downColor: '#dd7870', borderVisible: false, wickUpColor: '#23846c', wickDownColor: '#dd7870', priceLineColor: '#23846c' });
    vwap = chart.addSeries(LineSeries, { color: '#5577dd', lineWidth: 2, title: 'VWAP', priceLineVisible: false, lastValueVisible: false });
    ema = chart.addSeries(LineSeries, { color: '#b67b32', lineWidth: 2, title: 'EMA', priceLineVisible: false, lastValueVisible: false });
    supertrend = chart.addSeries(LineSeries, { color: '#4a9b57', lineWidth: 2, title: 'Supertrend', priceLineVisible: false, lastValueVisible: false });
    sar = chart.addSeries(LineSeries, { color: '#9a67b3', lineWidth: 2, lineStyle: 2, title: 'Parabolic SAR', priceLineVisible: false, lastValueVisible: false });
    observer = new ResizeObserver(entries => chart.applyOptions({ width: entries[0].contentRect.width }));
    observer.observe(element);
    update();
    return () => { observer.disconnect(); chart.remove(); };
  });
  $: update();
</script>

<div class="chart-legend" aria-label="Chart legend">
  <span class="price"><i></i>Underlying price</span><span class="vwap"><i></i>VWAP</span><span class="ema"><i></i>EMA</span><span class="supertrend"><i></i>Supertrend</span><span class="sar"><i></i>Parabolic SAR</span>
</div>
<div class="technical-chart" bind:this={element} aria-label="Candlestick chart with technical indicators"></div>

<style>
  .chart-legend{display:flex;flex-wrap:wrap;gap:9px 15px;padding:0 24px 12px;color:#809075;font-size:9px}.chart-legend span{display:flex;align-items:center;gap:5px;white-space:nowrap}.chart-legend i{width:13px;height:2px;background:#23846c;display:inline-block}.chart-legend .price i{height:7px;width:7px;border-radius:50%}.chart-legend .vwap i{background:#5577dd}.chart-legend .ema i{background:#b67b32}.chart-legend .supertrend i{background:#4a9b57}.chart-legend .sar i{background:repeating-linear-gradient(90deg,#9a67b3 0 3px,transparent 3px 5px)}.technical-chart{width:100%;height:330px}@media(max-width:620px){.chart-legend{padding-inline:16px;gap:8px 11px}.technical-chart{height:280px}}
</style>
