import { useRef, useCallback } from 'react';
import { decode } from '@msgpack/msgpack';

const getWsUrl = () => {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  // If running on Vite dev server (port 5173), hardcode to backend port 8080
  if (window.location.port === '5173') {
    return `ws://${window.location.hostname}:8080/ws`;
  }
  // Otherwise (production/ngrok), use same host/port relative path
  return `${protocol}//${window.location.host}/ws`;
};

const WS_URL = getWsUrl();
const RECONNECT_DELAY_MS = 2000;

/**
 * useTradeStream — WebSocket data layer (v9: streaming history protocol)
 *
 * PROTOCOL:
 *   Message 1 (on connect): MsgPack uint32 = history snapshot count
 *     Detection: typeof decoded === 'number'
 *
 *   Message 2..N+1: Individual history snapshots (same format as live ticks)
 *     Format: FixArray(9) [price, cvd, time, candle1s, candle1m, ob, oi, score, htf]
 *
 *   Message N+2+: Live tick snapshots (identical format)
 *
 * @param {Function} onSnapshot - Called for EVERY snapshot (history + live)
 * @param {Function} onLoadingChange - Called with (active, current, total)
 */
export function useTradeStream(onSnapshot, onLoadingChange) {
  const onSnapshotRef = useRef(onSnapshot);
  onSnapshotRef.current = onSnapshot;
  const onLoadingRef = useRef(onLoadingChange);
  onLoadingRef.current = onLoadingChange;

  const wsRef = useRef(null);
  const reconnectRef = useRef(null);
  const historyTotal = useRef(0);
  const historyCount = useRef(0);

  const parseCandle = (c) => ({
    time: c[0],
    open: c[1],
    high: c[2],
    low: c[3],
    close: c[4],
    buyVol: c[5],
    sellVol: c[6],
    delta: c[7],
    avgScore: c[8],
  });

  const parseSnapshot = (raw) => {
    const ob = raw[5];
    const oiRaw = raw[6];
    const htfRaw = raw[8];

    return {
      price: raw[0],
      cvd: raw[1],
      time: raw[2],
      candle1s: parseCandle(raw[3]),
      candle1m: parseCandle(raw[4]),
      orderbook: {
        bestBid: ob[0],
        bestAsk: ob[1],
        spread: ob[2],
        imbalance: ob[3],
        score: ob[4],
      },
      oi: {
        value: oiRaw[0],
        delta1s: oiRaw[1],
        delta1m: oiRaw[2],
        behavior: oiRaw[3],
      },
      finalScore: raw[7],
      htf: htfRaw.map(parseCandle),
    };
  };

  const connect = useCallback(() => {
    if (wsRef.current) return;

    const ws = new WebSocket(WS_URL);
    ws.binaryType = 'arraybuffer';
    wsRef.current = ws;
    historyTotal.current = 0;
    historyCount.current = 0;

    ws.onopen = () => {
      console.log('[WS] Connected, waiting for history header...');
    };

    ws.onmessage = (evt) => {
      try {
        const raw = decode(new Uint8Array(evt.data));

        // ═══ HISTORY HEADER: MsgPack uint32 (count) ═══
        if (typeof raw === 'number' && historyTotal.current === 0) {
          historyTotal.current = raw;
          historyCount.current = 0;
          console.log(`[WS] History: expecting ${raw} snapshots`);
          if (onLoadingRef.current) {
            onLoadingRef.current(true, 0, raw);
          }
          return;
        }

        // ═══ SNAPSHOT (history or live) ═══
        const snapshot = parseSnapshot(raw);
        onSnapshotRef.current(snapshot);

        // Track history progress
        if (historyCount.current < historyTotal.current) {
          historyCount.current++;
          // Update loading progress every 100 snapshots (avoid excessive re-renders)
          if (historyCount.current % 100 === 0 || historyCount.current >= historyTotal.current) {
            if (onLoadingRef.current) {
              onLoadingRef.current(
                historyCount.current < historyTotal.current,
                historyCount.current,
                historyTotal.current
              );
            }
          }
          if (historyCount.current >= historyTotal.current) {
            console.log(`[WS] History complete: ${historyTotal.current} snapshots loaded`);
          }
        }
      } catch (err) {
        console.error('[WS] Message decode error:', err);
      }
    };

    ws.onclose = (evt) => {
      console.log(`[WS] Closed (code: ${evt.code})`);
      wsRef.current = null;
      historyTotal.current = 0;
      historyCount.current = 0;
      reconnectRef.current = setTimeout(connect, RECONNECT_DELAY_MS);
    };

    ws.onerror = (err) => {
      console.error('[WS] Error:', err);
      ws.close();
    };
  }, []);

  // Auto-connect on mount
  const mountedRef = useRef(false);
  if (!mountedRef.current) {
    mountedRef.current = true;
    setTimeout(connect, 0);
  }
}
