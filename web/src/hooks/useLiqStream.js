import { useRef, useCallback, useEffect } from 'react';

/**
 * useLiqStream — Direct Binance Futures forceOrder WebSocket
 *
 * Connects to wss://fstream.binance.com/ws/btcusdt@forceOrder
 * Fires onLiq callback with parsed liquidation data:
 *   { side: 'BUY'|'SELL', price: number, qty: number, usdVal: number, ts: string, time: number }
 *
 * BUY = liquidated SHORT (forced buy to close short)
 * SELL = liquidated LONG (forced sell to close long)
 */
const LIQ_URL = 'wss://fstream.binance.com/ws/btcusdt@forceOrder';
const RECONNECT_MS = 3000;
const MIN_USD = 50000; // Only show liquidations >= $50K

export function useLiqStream(onLiq) {
  const cbRef = useRef(onLiq);
  cbRef.current = onLiq;
  const wsRef = useRef(null);
  const timerRef = useRef(null);

  const connect = useCallback(() => {
    if (wsRef.current) return;

    const ws = new WebSocket(LIQ_URL);
    wsRef.current = ws;

    ws.onmessage = (evt) => {
      try {
        const raw = JSON.parse(evt.data);
        const o = raw.o; // order object
        if (!o) return;

        const side = o.S; // BUY or SELL
        const price = parseFloat(o.p);
        const qty = parseFloat(o.q);
        const usdVal = price * qty;

        // Filter noise — only significant liquidations
        if (usdVal < MIN_USD) return;

        const ts = new Date().toLocaleTimeString('en-US', {
          hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit'
        });

        cbRef.current({
          side,
          price,
          qty,
          usdVal,
          ts,
          time: o.T || Date.now(),
        });
      } catch (err) {
        console.error('[LIQ] parse error:', err);
      }
    };

    ws.onclose = () => {
      wsRef.current = null;
      timerRef.current = setTimeout(connect, RECONNECT_MS);
    };

    ws.onerror = () => ws.close();

    console.log('[LIQ] Connected to Binance forceOrder');
  }, []);

  useEffect(() => {
    connect();
    return () => {
      if (wsRef.current) wsRef.current.close();
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, [connect]);
}
