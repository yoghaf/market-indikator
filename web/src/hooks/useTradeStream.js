import { useRef, useCallback } from 'react';
import { decode } from '@msgpack/msgpack';

const WS_URL = `ws://${window.location.hostname}:8080/ws`;
const RECONNECT_DELAY_MS = 2000;

/**
 * useTradeStream — WebSocket data layer (v6: + Multi-Timeframe)
 *
 * MsgPack payload: FixArray(9)
 *   [0] price      float64
 *   [1] cvd        float64
 *   [2] time       int64 (ms)
 *   [3] candle1s   FixArray(9) [time, o, h, l, c, buyVol, sellVol, delta, avgScore]
 *   [4] candle1m   FixArray(9)
 *   [5] orderbook  FixArray(5) [bestBid, bestAsk, spread, imbalance, score]
 *   [6] oi         FixArray(4) [oi, oiDelta1s, oiDelta1m, behavior]
 *   [7] finalScore float64
 *   [8] htf        FixArray(5) — each FixArray(9) [5m, 15m, 1h, 4h, 1d]
 */
export function useTradeStream(onSnapshot) {
  const onSnapshotRef = useRef(onSnapshot);
  onSnapshotRef.current = onSnapshot;
  const wsRef = useRef(null);
  const reconnectRef = useRef(null);

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

  const connect = useCallback(() => {
    if (wsRef.current) return;

    const ws = new WebSocket(WS_URL);
    ws.binaryType = 'arraybuffer';
    wsRef.current = ws;

    ws.onmessage = (evt) => {
      const raw = decode(new Uint8Array(evt.data));

      const ob = raw[5];
      const oiRaw = raw[6];
      const htfRaw = raw[8];

      const snapshot = {
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
        htf: htfRaw.map(parseCandle), // [5m, 15m, 1h, 4h, 1d]
      };

      onSnapshotRef.current(snapshot);
    };

    ws.onclose = () => {
      wsRef.current = null;
      reconnectRef.current = setTimeout(connect, RECONNECT_DELAY_MS);
    };

    ws.onerror = () => ws.close();
  }, []);

  // Auto-connect on mount
  const mountedRef = useRef(false);
  if (!mountedRef.current) {
    mountedRef.current = true;
    setTimeout(connect, 0);
  }
}
