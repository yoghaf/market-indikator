import { useEffect, useRef } from 'react';
import { createChart, CandlestickSeries, HistogramSeries, createSeriesMarkers } from 'lightweight-charts';

/**
 * PriceChart — Chart rendering layer (v4: lightweight-charts v5 API)
 *
 * Panes:
 *   Top:    1-second OHLC candles
 *   Bottom: CVD histogram
 *   Overlay: Signal markers (▲ bull flip, ▼ bear flip, ● imbalance)
 *
 * PERFORMANCE:
 * - All updates imperative via series.update() — O(1), no re-render.
 * - Markers maintained in ring buffer (max 200), flushed via createSeriesMarkers.
 * - Flush throttled to at most once per 500ms.
 */
const MAX_MARKERS = 200;

export default function PriceChart({ onChartReady }) {
  const containerRef = useRef(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const chart = createChart(containerRef.current, {
      layout: {
        background: { color: '#080c16' },
        textColor: '#8a8a9a',
        fontFamily: "'JetBrains Mono', 'Inter', system-ui, monospace",
      },
      grid: {
        vertLines: { color: 'rgba(42, 46, 57, 0.2)' },
        horzLines: { color: 'rgba(42, 46, 57, 0.2)' },
      },
      crosshair: {
        mode: 0,
        vertLine: { color: 'rgba(224, 227, 235, 0.2)', style: 0 },
        horzLine: { color: 'rgba(224, 227, 235, 0.2)', style: 0 },
      },
      rightPriceScale: {
        borderColor: 'rgba(42, 46, 57, 0.4)',
        scaleMargins: { top: 0.05, bottom: 0.3 },
      },
      timeScale: {
        borderColor: 'rgba(42, 46, 57, 0.4)',
        timeVisible: true,
        secondsVisible: true,
        rightOffset: 5,
      },
      handleScale: { axisPressedMouseMove: true },
      handleScroll: { vertTouchDrag: false },
    });

    const candleSeries = chart.addSeries(CandlestickSeries, {
      upColor: '#00dc82',
      downColor: '#ef4444',
      borderUpColor: '#00dc82',
      borderDownColor: '#ef4444',
      wickUpColor: '#00dc82',
      wickDownColor: '#ef4444',
    });

    const cvdSeries = chart.addSeries(HistogramSeries, {
      priceFormat: { type: 'volume' },
      priceScaleId: 'cvd',
    });

    chart.priceScale('cvd').applyOptions({
      scaleMargins: { top: 0.75, bottom: 0.02 },
      borderVisible: false,
    });

    // ─── MARKER RING BUFFER (v5 API: createSeriesMarkers) ───
    const markers = [];
    let markerDirty = false;
    let markerTimer = null;
    let markerPrimitive = null;

    function flushMarkers() {
      if (!markerDirty) return;
      // Sort by time (required by lightweight-charts)
      markers.sort((a, b) => a.time - b.time);
      // v5: remove old primitive, create new one
      if (markerPrimitive) {
        try { markerPrimitive.detach(); } catch (_) {}
      }
      try {
        markerPrimitive = createSeriesMarkers(candleSeries, [...markers]);
      } catch (_) {
        // Silently handle if createSeriesMarkers fails
      }
      markerDirty = false;
      markerTimer = null;
    }

    // Resize
    const resizeObserver = new ResizeObserver((entries) => {
      const { width, height } = entries[0].contentRect;
      chart.applyOptions({ width, height });
    });
    resizeObserver.observe(containerRef.current);

    // Expose API
    if (onChartReady) {
      onChartReady({
        addSnapshot: (snap) => {
          const c = snap.candle1s;

          candleSeries.update({
            time: c.time,
            open: c.open,
            high: c.high,
            low: c.low,
            close: c.close,
          });

          cvdSeries.update({
            time: c.time,
            value: snap.cvd,
            color: c.delta >= 0
              ? 'rgba(0, 220, 130, 0.5)'
              : 'rgba(239, 68, 68, 0.5)',
          });
        },

        /**
         * addMarker — imperatively add a signal marker to the chart.
         * @param {number} time   - Unix timestamp (seconds)
         * @param {'bull'|'bear'|'imbalance'|'aggression'|'oi'} type
         * @param {string} text   - Short label (max 4 chars shown)
         */
        addMarker: (time, type, text) => {
          const marker = { time };

          switch (type) {
            case 'bull':
              marker.position = 'belowBar';
              marker.shape = 'arrowUp';
              marker.color = '#00dc82';
              marker.text = text || '▲';
              break;
            case 'bear':
              marker.position = 'aboveBar';
              marker.shape = 'arrowDown';
              marker.color = '#ef4444';
              marker.text = text || '▼';
              break;
            case 'imbalance':
              marker.position = 'aboveBar';
              marker.shape = 'circle';
              marker.color = '#eab308';
              marker.text = text || '●';
              break;
            case 'aggression':
              marker.position = 'belowBar';
              marker.shape = 'circle';
              marker.color = '#f97316';
              marker.text = text || '⚡';
              break;
            case 'oi':
              marker.position = 'aboveBar';
              marker.shape = 'square';
              marker.color = '#64b4ff';
              marker.text = text || 'OI';
              break;
            default:
              marker.position = 'aboveBar';
              marker.shape = 'circle';
              marker.color = '#8a8a9a';
              marker.text = text || '?';
          }

          markers.push(marker);

          // Trim ring buffer
          while (markers.length > MAX_MARKERS) markers.shift();

          // Throttled flush
          markerDirty = true;
          if (!markerTimer) {
            markerTimer = setTimeout(flushMarkers, 500);
          }
        },
      });
    }

    return () => {
      if (markerTimer) clearTimeout(markerTimer);
      if (markerPrimitive) {
        try { markerPrimitive.detach(); } catch (_) {}
      }
      resizeObserver.disconnect();
      chart.remove();
    };
  }, []);

  return (
    <div
      ref={containerRef}
      style={{ width: '100%', height: '100%' }}
    />
  );
}
