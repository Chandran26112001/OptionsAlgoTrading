<script>
  import { onMount } from 'svelte';
  import { createChart, CandlestickSeries, ColorType } from 'lightweight-charts';
  export let candles = [];
  export let identity = '';
  let element, chart, series, observer, previousIdentity = '', lastData = [];
  onMount(() => {
    chart = createChart(element, {
      height: 246,
      layout: { background: { type: ColorType.Solid, color: '#ffffff' }, textColor: '#88918e', fontFamily: 'Inter, Segoe UI, sans-serif', fontSize: 11, attributionLogo: true },
      grid: { vertLines: { color: '#f4f6f5' }, horzLines: { color: '#f0f3f1' } },
      rightPriceScale: { borderVisible: false },
      timeScale: { borderVisible: false, timeVisible: true, secondsVisible: false, tickMarkFormatter: time => new Date(Number(time) * 1000).toLocaleTimeString('en-GB', { timeZone: 'Asia/Kolkata', hour: '2-digit', minute: '2-digit' }) },
      localization: { timeFormatter: time => new Date(Number(time) * 1000).toLocaleString('en-IN', { timeZone: 'Asia/Kolkata', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) },
      crosshair: { vertLine: { color: '#9daaa4', labelBackgroundColor: '#1c6855' }, horzLine: { color: '#9daaa4', labelBackgroundColor: '#1c6855' } },
    });
    series = chart.addSeries(CandlestickSeries, { upColor: '#23846c', downColor: '#dd7870', borderVisible: false, wickUpColor: '#23846c', wickDownColor: '#dd7870', priceLineColor: '#23846c' });
    observer = new ResizeObserver(entries => chart.applyOptions({ width: entries[0].contentRect.width }));
    observer.observe(element);
    update(candles, identity);
    return () => { observer.disconnect(); chart.remove(); };
  });
  function update(data, key) {
    if (!series) return;
    if (key !== previousIdentity || data.length !== lastData.length || data[0]?.time !== lastData[0]?.time) {
      series.setData(data);
      if (key !== previousIdentity || !lastData.length) chart.timeScale().fitContent();
    } else if (data.length) {
      // Intraday history can revise the most recent candle.
      series.update(data[data.length - 1]);
    }
    previousIdentity = key; lastData = data;
  }
  $: update(candles, identity);
</script>
<div class="price-chart" bind:this={element}></div>
