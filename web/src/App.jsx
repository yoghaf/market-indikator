import { useRef, useState, useCallback, useMemo } from 'react';
import { useTradeStream } from './hooks/useTradeStream';
import { useLiqStream } from './hooks/useLiqStream';
import { useBiasEngine } from './hooks/useBiasEngine';
import PriceChart from './components/PriceChart';
import './App.css';

/* ═══════════════════════════════════════════════════════════════
   BTCUSDT ORDERFLOW DECISION TERMINAL v4
   
   LAYOUT (50/50):
     LEFT  50% — Chart + Position Engine + Flow Momentum + Gauge
     RIGHT 50% — Event Log (~60%) + OI + Heatmap + State History
   
   FEATURES:
     • BTC/USD unit toggle (instant conversion)
     • Price info header bar (levels, score, confidence)
     • Enhanced event log with BTC sizes + icons
     • State history timeline with timestamps
   ═══════════════════════════════════════════════════════════════ */

const BEH = ['NEUTRAL', 'LONG BUILDUP', 'SHORT BUILDUP', 'SHORT COVERING', 'LONG LIQUIDATION'];
const BEH_C = ['n', 'lb', 'sb', 'sc', 'll'];
const TF_LABELS = ['1s', '1m', '5m', '15m', '1h', '4h', '1d'];
const HM_ROWS = ['SCORE', 'DELTA', 'VOL'];
const STATE_COLORS = {
  LONG_BUILDUP: '#00dc82', SHORT_BUILDUP: '#ef4444', SHORT_COVERING: '#4a9cd4',
  LONG_LIQUIDATION: '#d48a30', ABSORPTION_BOTTOM: '#00b868', DISTRIBUTION_TOP: '#c03838',
  NEUTRAL_CHOP: '#2a3050',
};

/* ═══ HELPERS ═══ */
const cls5 = s => s >= 50 ? 'xb' : s >= 15 ? 'b' : s > -15 ? 'n' : s > -50 ? 'br' : 'xbr';
const stLabel = s => s >= 15 ? 'BULLISH' : s > -15 ? 'NEUTRAL' : 'BEARISH';
const regLabel = (bias, fs) => { const a = Math.abs(fs); if (a < 10) return 'RANGE'; if (bias === 'RANGE') return a > 40 ? 'SQUEEZE' : 'RANGE'; return 'TREND'; };
const confCalc = (fs, im, bh) => { let c = Math.min(Math.abs(fs), 60); if (Math.abs(im) > 0.1) c += 15; if (bh === 1 || bh === 2) c += 10; if (Math.abs(fs) > 70) c += 8; return Math.min(Math.round(c), 99); };
const heatBg = s => { const c = Math.max(-100, Math.min(100, s)); if (c >= 0) { const t = c / 100; return `rgba(0,${130 + 90 * t | 0},${60 + 70 * t | 0},${(.12 + .65 * t).toFixed(2)})`; } const t = -c / 100; return `rgba(${160 + 79 * t | 0},${35 + 33 * t | 0},${35 + 33 * t | 0},${(.12 + .65 * t).toFixed(2)})`; };
const htfBias = h => { const a = .3 * (h[4] || 0) + .35 * (h[5] || 0) + .35 * (h[6] || 0); return a > 15 ? 'BULLISH' : a < -15 ? 'BEARISH' : 'RANGE'; };
const normDelta = (d, maxD) => maxD > 0 ? Math.max(-100, Math.min(100, (d / maxD) * 100)) : 0;
const normVol = (v, maxV) => maxV > 0 ? Math.min(100, (v / maxV) * 100) : 0;

/* ═══ UNIT-AWARE FORMATTERS ═══ */
function makeFormatters(unit, price) {
  const isUSD = unit === 'USD';
  const conv = v => isUSD ? v * price : v;
  const suffix = isUSD ? '' : ' BTC';
  const prefix = isUSD ? '$' : '';

  const fmtVol = v => {
    const val = conv(v);
    const abs = Math.abs(val);
    if (abs >= 1_000_000) return `${prefix}${(val / 1_000_000).toFixed(1)}M${suffix}`;
    if (abs >= 1_000) return `${prefix}${(val / 1_000).toFixed(1)}K${suffix}`;
    return `${prefix}${val.toFixed(isUSD ? 0 : 2)}${suffix}`;
  };

  const fmtDelta = v => {
    const sign = v >= 0 ? '+' : '';
    return `${sign}${fmtVol(v)}`;
  };

  const fmtOI = v => {
    if (!v) return '—';
    const val = conv(v);
    const sign = val >= 0 ? '+' : '';
    const abs = Math.abs(val);
    if (abs >= 1_000_000) return `${sign}${(val / 1_000_000).toFixed(1)}M`;
    if (abs >= 1_000) return `${sign}${(val / 1_000).toFixed(1)}K`;
    return `${sign}${val.toFixed(isUSD ? 0 : 1)}`;
  };

  return { fmtVol, fmtDelta, fmtOI, conv };
}

// Static formatters (unit-agnostic)
const F = {
  p: v => v > 0 ? `$${v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}` : '—',
  p0: v => v > 0 ? v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 }) : '—',
  c: v => `${v >= 0 ? '+' : ''}${v.toFixed(1)}`,
  pct: v => `${(v * 100).toFixed(1)}%`,
  s: v => v >= 0 ? 'pos' : 'neg',
};

/* ═══ EVENT ENGINE v7: defensive, balanced thresholds ═══ */
const MAX_EV = 80, CD = 3000;
function mkEE() {
  let ps = 0, po = 0, pb = 0, dE = 0, rE = 0, tk = 0, refPrice = 0;
  const cd = {}, ok = t => { const n = Date.now(); if (cd[t] && n - cd[t] < CD) return false; cd[t] = n; return true; };
  return s => {
    try {
      const ev = [];
      const ts = new Date().toLocaleTimeString('en-US', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' });
      const ct = s.candle1s?.time || 0;
      const sc = s.finalScore || 0;
      const ob = s.orderbook?.score || 0;
      const d1 = s.candle1s?.delta || 0;
      const bh = s.oi?.behavior || 0;
      const rng = (s.candle1s?.high || 0) - (s.candle1s?.low || 0);
      const oiD = s.oi?.delta1m || 0;
      const vol = (s.candle1s?.buyVol || 0) + (s.candle1s?.sellVol || 0);
      const price = s.price || 0;

      dE = .15 * Math.abs(d1) + .85 * dE;
      rE = .1 * Math.abs(rng) + .9 * rE;
      tk++;

      // Debug first 3 ticks
      if (tk <= 3) console.log('[EE] tick', tk, 'score:', sc, 'price:', price, 'delta:', d1);

      // ─── 0. STARTUP ───
      if (tk === 1) {
        ev.push({ ts, m: 'Stream Connected', detail: 'Feed active', c: 'info', icon: '⚡', time: ct, price });
        refPrice = price;
      }

      // ─── 1. PRICE MOVE ($10 cumulative) ───
      if (refPrice > 0 && price > 0) {
        const move = price - refPrice;
        if (Math.abs(move) >= 10 && ok('pm')) {
          const up = move > 0;
          ev.push({ ts, m: up ? 'Price Up' : 'Price Down', detail: `${up ? '+' : ''}$${move.toFixed(0)}`, btcSize: vol, c: up ? 'bull' : 'bear', icon: up ? '▲' : '▼', time: ct, price });
          refPrice = price;
        }
      }

      // ─── 2. SCORE SHIFT (±5) ───
      const sDiff = sc - ps;
      if (Math.abs(sDiff) >= 5 && ok('sc')) {
        const up = sDiff > 0;
        ev.push({ ts, m: up ? 'Pressure Rising' : 'Pressure Falling', detail: `${up ? '+' : ''}${Math.round(sDiff)} → ${Math.round(sc)}`, btcSize: vol, c: up ? 'bull' : 'bear', icon: up ? '🟢' : '🔴', time: ct, price });
      }

      // ─── 3. LTF FLIP ───
      if (ps < -5 && sc > 8 && ok('f'))
        ev.push({ ts, m: 'Long Buildup', detail: `+${Math.round(sc)} pressure`, btcSize: vol, c: 'bull', icon: '📈', mk: { t: 'bull', x: 'B' }, time: ct, price });
      if (ps > 5 && sc < -8 && ok('f'))
        ev.push({ ts, m: 'Short Buildup', detail: `${Math.round(sc)} pressure`, btcSize: vol, c: 'bear', icon: '📉', mk: { t: 'bear', x: 'S' }, time: ct, price });

      // ─── 4. WALLS (OB > 12) ───
      if (Math.abs(ob) > 12 && Math.abs(po) <= 12 && ok('i')) {
        const bull = ob > 0;
        ev.push({ ts, m: `${bull ? 'Support' : 'Resistance'} Wall`, detail: `OB ${bull ? '+' : ''}${Math.round(ob)}`, btcSize: vol, c: bull ? 'bull' : 'bear', icon: bull ? '🛡' : '🧱', time: ct, price });
      }

      // ─── 5. AGGRESSIVE FLOW (skip warmup, 1.3x avg) ───
      if (tk >= 40 && dE > 0.00001 && Math.abs(d1) > dE * 1.3 && ok('a')) {
        const buy = d1 > 0;
        ev.push({ ts, m: buy ? 'Whale Buy' : 'Whale Sell', detail: `${(Math.abs(d1) / dE).toFixed(1)}x avg`, btcSize: Math.abs(d1), c: buy ? 'whale' : 'liq', icon: buy ? '🐋' : '🦈', time: ct, price });
      }

      // ─── 6. MOMENTUM EXPANSION (1.5x, after warmup) ───
      if (tk >= 20 && rE > 0.1 && rng > rE * 1.5 && ok('m')) {
        const up = (s.candle1s?.close || 0) > (s.candle1s?.open || 0);
        ev.push({ ts, m: `${up ? 'Bull' : 'Bear'} Expansion`, detail: `${(rng / rE).toFixed(1)}x range`, btcSize: vol, c: up ? 'bull' : 'bear', icon: up ? '🚀' : '🔻', time: ct, price });
      }

      // ─── 7. OI BEHAVIOR CHANGE ───
      if (bh !== pb && bh !== 0 && ok('o'))
        ev.push({ ts, m: `${BEH[bh] || 'OI Change'}`, detail: 'OI regime shift', btcSize: vol, c: bh <= 2 ? (bh === 1 ? 'bull' : 'bear') : 'info', icon: bh === 1 ? '📈' : bh === 2 ? '📉' : '🔄', time: ct, price });

      // ─── 8. OI DELTA (>5) ───
      if (Math.abs(oiD) > 5 && ok('oid')) {
        const up = oiD > 0;
        ev.push({ ts, m: up ? 'OI Building' : 'OI Unwind', detail: `${up ? '+' : ''}${oiD.toFixed(1)}`, btcSize: Math.abs(oiD) / 1000, c: up ? 'bull' : 'bear', icon: up ? '📊' : '📉', time: ct, price });
      }

      // ─── 9. ABSORPTION (OB > 12, opposing score) ───
      if (ob > 12 && sc < -3 && ok('abs'))
        ev.push({ ts, m: 'Absorption Bottom', detail: `bid wall +${Math.round(ob)}`, btcSize: vol, c: 'absorption', icon: '🧲', time: ct, price });
      if (ob < -12 && sc > 3 && ok('abst'))
        ev.push({ ts, m: 'Distribution Top', detail: `ask wall ${Math.round(ob)}`, btcSize: vol, c: 'distribution', icon: '🌊', time: ct, price });

      // ─── 10. HEARTBEAT (every 120 ticks ≈ 30s) ───
      if (tk > 1 && tk % 120 === 0 && ok('hb')) {
        const label = sc >= 15 ? 'BULLISH' : sc <= -15 ? 'BEARISH' : 'NEUTRAL';
        ev.push({ ts, m: `State: ${label}`, detail: `Score ${sc >= 0 ? '+' : ''}${Math.round(sc)}`, c: 'info', icon: '◉', time: ct, price });
      }

      ps = sc; po = ob; pb = bh;
      return ev.slice(0, 3); // rate limit
    } catch (err) {
      console.error('[EE] crash:', err);
      return [];
    }
  };
}

/* ═══ FULL-CIRCLE GAUGE (compact) ═══ */
function FullGauge({ value, conf }) {
  const v = Math.max(-100, Math.min(100, Math.round(value)));
  const c5 = cls5(v);
  const color = c5 === 'xb' ? '#00dc82' : c5 === 'b' ? '#00b868' : c5 === 'n' ? '#3a4575' : c5 === 'br' ? '#c03838' : '#ef4444';
  const glow = Math.abs(v) > 15;
  const S = 160, cx = S / 2, cy = S / 2, r = 64;
  const SA = 135, SW = 270;
  const norm = (v + 100) / 200;
  const ea = SA + SW * norm;
  const toRad = a => ((a - 90) * Math.PI) / 180;
  const pt = a => ({ x: cx + r * Math.cos(toRad(a)), y: cy + r * Math.sin(toRad(a)) });
  const arcPath = (from, to) => { const s = pt(from), e = pt(to); return `M ${s.x} ${s.y} A ${r} ${r} 0 ${(to - from) > 180 ? 1 : 0} 1 ${e.x} ${e.y}`; };

  const ticks = [];
  for (let i = 0; i <= 20; i++) {
    const angle = SA + (i / 20) * SW, outer = pt(angle), isMajor = i % 5 === 0;
    const innerR = r - (isMajor ? 8 : 4);
    const inner = { x: cx + innerR * Math.cos(toRad(angle)), y: cy + innerR * Math.sin(toRad(angle)) };
    ticks.push(<line key={i} x1={outer.x} y1={outer.y} x2={inner.x} y2={inner.y} stroke={isMajor ? 'rgba(255,255,255,0.12)' : 'rgba(255,255,255,0.04)'} strokeWidth={isMajor ? 1.2 : 0.6} />);
  }

  return (
    <svg width={S} height={S} viewBox={`0 0 ${S} ${S}`} className="fg">
      <defs><linearGradient id="gg" x1="0%" y1="100%" x2="100%" y2="0%"><stop offset="0%" stopColor="#ef4444" stopOpacity="0.12" /><stop offset="50%" stopColor="#3a4575" stopOpacity="0.04" /><stop offset="100%" stopColor="#00dc82" stopOpacity="0.12" /></linearGradient></defs>
      {ticks}
      <path d={arcPath(SA, SA + SW)} fill="none" stroke="url(#gg)" strokeWidth="7" strokeLinecap="round" />
      {norm > 0.005 && <path d={arcPath(SA, ea)} fill="none" stroke={color} strokeWidth="7" strokeLinecap="round" style={glow ? { filter: `drop-shadow(0 0 10px ${color})` } : undefined} />}
      <text x={cx} y={cy - 14} textAnchor="middle" dominantBaseline="central" className="fg-num" fill={color} style={glow ? { filter: `drop-shadow(0 0 16px ${color})` } : undefined}>{v > 0 ? '+' : ''}{v}</text>
      <text x={cx} y={cy + 6} textAnchor="middle" className="fg-sublabel">PRESSURE</text>
      <text x={cx} y={cy + 24} textAnchor="middle" className="fg-conf" fill={color}>{conf}%</text>
    </svg>
  );
}

/* ═══ CVD SPARKLINE ═══ */
function CVDSpark({ history, width = 140, height = 36 }) {
  if (!history || history.length < 2) return null;
  const W = width, H = height, p = 2, vals = history.slice(-40);
  const min = Math.min(...vals), max = Math.max(...vals), rng = max - min || 1;
  const pts = vals.map((v, i) => `${p + (i / (vals.length - 1)) * (W - p * 2)},${H - p - ((v - min) / rng) * (H - p * 2)}`).join(' ');
  const col = vals[vals.length - 1] >= 0 ? '#00dc82' : '#ef4444';
  return <svg width={W} height={H} viewBox={`0 0 ${W} ${H}`}><polyline points={pts} fill="none" stroke={col} strokeWidth="1.5" opacity="0.7" /></svg>;
}

/* ═══ STATE HISTORY STRIP ═══ */
function StateHistory({ events }) {
  const stateEvents = events.filter(e =>
    e.m.includes('BUILDUP') || e.m.includes('COVERING') || e.m.includes('LIQUIDATION') ||
    e.m.includes('Absorption') || e.m.includes('Flip') || e.m.includes('Buildup')
  ).slice(0, 10);

  const badges = stateEvents.map(e => {
    let state = 'NEUTRAL_CHOP';
    if (e.m.includes('LONG BUILDUP') || e.m.includes('Long Buildup')) state = 'LONG_BUILDUP';
    else if (e.m.includes('SHORT BUILDUP') || e.m.includes('Short Buildup')) state = 'SHORT_BUILDUP';
    else if (e.m.includes('COVERING')) state = 'SHORT_COVERING';
    else if (e.m.includes('LIQUIDATION')) state = 'LONG_LIQUIDATION';
    else if (e.m.includes('Absorption')) state = 'ABSORPTION_BOTTOM';
    return { state, color: STATE_COLORS[state] || STATE_COLORS.NEUTRAL_CHOP, ts: e.ts };
  }).filter(b => b.state !== 'NEUTRAL_CHOP');

  return (
    <div className="sh">
      <div className="sh-top">
        <span className="sh-icon">◉</span>
        <span className="sh-label">STATE HISTORY</span>
        {badges.slice(0, 4).map((b, i) => (
          <span key={i} className="sh-badge" style={{ background: b.color }}>{b.state.replace(/_/g, ' ')}</span>
        ))}
      </div>
      <div className="sh-timeline">
        <div className="sh-tline">
          {badges.length === 0
            ? <div className="sh-seg" style={{ flex: 1, background: STATE_COLORS.NEUTRAL_CHOP }} />
            : badges.map((b, i) => <div key={i} className="sh-seg" style={{ flex: 1, background: b.color }} />)
          }
        </div>
        <div className="sh-tsrow">
          {badges.map((b, i) => <span key={i} className="sh-ts">{b.ts}</span>)}
          {badges.length === 0 && <span className="sh-ts">—</span>}
        </div>
      </div>
    </div>
  );
}

/* ═══ APP ═══ */
export default function App() {
  const chartRef = useRef(null);
  const [theme, setTheme] = useState(() => localStorage.getItem('theme') || 'dark');
  const toggleTheme = useCallback(() => setTheme(t => { const n = t === 'dark' ? 'light' : 'dark'; localStorage.setItem('theme', n); return n; }), []);
  const [unit, setUnit] = useState('BTC');
  const toggleUnit = useCallback(() => setUnit(u => u === 'BTC' ? 'USD' : 'BTC'), []);
  const [showHTF, setShowHTF] = useState(false); // HTF Data Panel
  const [chartTF, setChartTF] = useState('1s'); // Chart Toggle
  const toggleChartTF = useCallback(() => setChartTF(t => t === '1s' ? '1m' : '1s'), []);

  const [h, setH] = useState({
    price: 0, cvd: 0, delta1s: 0, delta1m: 0, buyVol: 0, sellVol: 0,
    spread: 0, imb: 0, ob: 0, oi: 0, oid1m: 0, beh: 0,
    fs: 0, htf: [0, 0, 0, 0, 0, 0, 0], // Score history
    htfCandles: [], // Full candle data
    ticks: 0, conn: false, lat: 0,
  });
  const [evts, setEvts] = useState([]);
  const [cvdH, setCvdH] = useState([]);
  const [wsLoading, setWsLoading] = useState({ active: true, current: 0, total: 0 });

  // Refs for performance
  const dR = useRef({
    price: 0, cvd: 0, delta1s: 0, delta1m: 0, buyVol: 0, sellVol: 0,
    spread: 0, imb: 0, ob: 0, oi: 0, oid1m: 0, beh: 0,
    fs: 0, htf: [0, 0, 0, 0, 0, 0, 0], htfCandles: [], ticks: 0, lat: 0,
  });
  const eB = useRef([]), eE = useRef(mkEE()), sT = useRef(0), tR = useRef(null);
  const historyLoadingRef = useRef(true);

  const onSnap = useCallback(s => {
    // Determine which candle to feed to chart
    const activeCandle = (chartTF === '1m') ? s.candle1m : s.candle1s;
    
    // Feed chart (Note: we lie to PriceChart about it being 'candle1s' so it renders it)
    if (chartRef.current) chartRef.current.addSnapshot({ ...s, candle1s: activeCandle });
    
    const now = performance.now();
    const ne = eE.current(s);
    if (ne.length) {
      // Chart markers only during live (history would add hundreds)
      if (!historyLoadingRef.current && chartRef.current) {
        for (const e of ne) if (e.mk) chartRef.current.addMarker(e.time, e.mk.t, e.mk.x);
      }
      eB.current.push(...ne);
    }
    const d = dR.current;
    
    d.price = s.price; d.cvd = s.cvd; d.delta1s = s.candle1s.delta; d.delta1m = s.candle1m.delta;
    d.buyVol = s.candle1s.buyVol; d.sellVol = s.candle1s.sellVol;
    d.spread = s.orderbook.spread; d.imb = s.orderbook.imbalance; d.ob = s.orderbook.score;
    d.oi = s.oi.value; d.oid1m = s.oi.delta1m; d.beh = s.oi.behavior;
    d.fs = s.finalScore; d.htf[0] = s.finalScore; d.htf[1] = s.candle1m.avgScore || 0;
    
    // Store full HTF candles
    d.htfCandles = s.htf; 
    
    for (let i = 0; i < 5; i++) d.htf[i + 2] = s.htf[i]?.avgScore || 0;
    d.ticks++; d.lat = sT.current > 0 ? Math.round(now - sT.current) : 0; sT.current = now;
    if (!tR.current) tR.current = setTimeout(() => {
      setH({ ...d, htf: [...d.htf], htfCandles: [...(d.htfCandles || [])], conn: true });
      setCvdH(prev => [...prev, d.cvd].slice(-60));
      if (eB.current.length) { setEvts(p => [...eB.current, ...p].slice(0, MAX_EV)); eB.current = []; }
      tR.current = null;
    }, 250);
  }, [chartTF]); // Re-create callback when chartTF changes to switch feed immediately

  // Loading state callback from useTradeStream
  const onLoadingChange = useCallback((active, current, total) => {
    historyLoadingRef.current = active;
    setWsLoading({ active, current, total });
  }, []);

  useTradeStream(onSnap, onLoadingChange);
  const onChart = useCallback(api => { chartRef.current = api; }, []);

  // Liquidation feed — inject into event buffer
  const onLiq = useCallback((liq) => {
    const isShort = liq.side === 'BUY'; // BUY = liquidated short (forced buy)
    const label = isShort ? 'Liquidated Short' : 'Liquidated Long';
    const usdStr = liq.usdVal >= 1000000 
      ? `$${(liq.usdVal / 1000000).toFixed(1)}M`
      : `$${(liq.usdVal / 1000).toFixed(1)}K`;
    const ev = {
      ts: liq.ts,
      m: label,
      detail: usdStr,
      btcSize: liq.qty,
      usdOnly: false,
      c: isShort ? 'bull' : 'bear',
      icon: '💀',
      time: liq.time,
      price: liq.price,
    };
    eB.current.push(ev);
    // Force flush quickly
    if (!tR.current) tR.current = setTimeout(() => {
      const d = dR.current;
      setH({ ...d, htf: [...d.htf], htfCandles: [...(d.htfCandles || [])], conn: true });
      if (eB.current.length) { setEvts(p => [...eB.current, ...p].slice(0, MAX_EV)); eB.current = []; }
      tR.current = null;
    }, 100);
  }, []);
  useLiqStream(onLiq);

  // Derived values
  const fs = Math.round(h.fs), c5 = cls5(fs);
  const bias = htfBias(h.htf), regime = regLabel(bias, fs), confVal = confCalc(h.fs, h.imb, h.beh);
  const tv = h.buyVol + h.sellVol, bp = tv > 0 ? h.buyVol / tv : 0.5;
  // 7 real timeframes: 1s, 1m, 5m, 15m, 1h, 4h, 1d
  const htScores = [h.htf[0], h.htf[1], h.htf[2], h.htf[3], h.htf[4], h.htf[5], h.htf[6]];
  // Build delta/vol arrays from htfCandles (indices 0..4 = 5m,15m,1h,4h,1d)
  const htDeltas = [h.delta1s || 0, h.delta1m || 0, ...(h.htfCandles || []).map(c => c?.delta || 0)];
  const htVols = [h.buyVol + h.sellVol, 0, ...(h.htfCandles || []).map(c => c ? (c.buyVol + c.sellVol) : 0)];
  const maxDelta = Math.max(...htDeltas.map(Math.abs), 0.001);
  const maxVol = Math.max(...htVols, 0.001);
  const buyW = bp * 100, sellW = (1 - bp) * 100;
  const state = stLabel(fs);

  // Dual-Bar Logic
  const bv = h.buyVol || 0;
  const sv = h.sellVol || 0;
  const totV = bv + sv || 1;
  const aggrBuyPct = (bv / totV) * 100;
  
  // Passive Logic: Market Sells feed Passive Buys, Market Buys feed Passive Sells
  // If OI is increasing, the passive side is "Holding/Absorbing"
  const oiChg = h.oid1m || 0; 
  const absorptionFactor = Math.min(1.5, Math.max(1, Math.abs(oiChg) / 10)); // Boost passive size if OI moves
  
  // Passive Buy Strength = Market Sells (liquidity taken) * Absorption
  let psvBuyStr = sv;
  // Passive Sell Strength = Market Buys (liquidity taken) * Absorption
  let psvSellStr = bv;
  
  // If Price Down but OI Up -> Passive Buyers strongly absorbing
  if (h.delta1m < 0 && oiChg > 0) psvBuyStr *= absorptionFactor;
  // If Price Up but OI Up -> Passive Sellers strongly absorbing
  if (h.delta1m > 0 && oiChg > 0) psvSellStr *= absorptionFactor;
  
  const totP = psvBuyStr + psvSellStr || 1;
  const psvBuyPct = (psvBuyStr / totP) * 100;

  // ═══ MARKET BIAS ENGINE ═══
  const mBias = useBiasEngine(fs, h.beh, aggrBuyPct, psvBuyPct, evts, htScores);

  // Unit-aware formatters (recalculated when unit or price changes)
  const UF = useMemo(() => makeFormatters(unit, h.price), [unit, h.price]);

  // Event size formatter (respects unit toggle, except usdOnly events)
  const fmtEvSize = (e) => {
    if (!e.btcSize || e.btcSize < 0.001) return '';
    if (e.usdOnly) return `$${(e.btcSize * (e.price || h.price)).toLocaleString('en-US', { maximumFractionDigits: 0 })}`;
    return UF.fmtVol(e.btcSize);
  };

  return (
    <div className={`T ${theme}`}>

      {/* ═══ LOADING OVERLAY ═══ */}
      {wsLoading.active && (
        <div className="LOAD-overlay">
          <div className="LOAD-content">
            <div className="LOAD-orb" />
            <div className="LOAD-text">{wsLoading.total > 0 ? 'LOADING HISTORY' : 'CONNECTING TO FEED...'}</div>
            <div className="LOAD-sub">
              {wsLoading.total > 0 ? `${wsLoading.current.toLocaleString()} / ${wsLoading.total.toLocaleString()} snapshots` : 'Establishing connection'}
            </div>
            {wsLoading.total > 0 && (
              <div className="LOAD-bar">
                <div className="LOAD-fill" style={{ width: `${(wsLoading.current / wsLoading.total * 100)}%` }} />
              </div>
            )}
          </div>
        </div>
      )}
        {/* ═══ HEADER ═══ */}
      <header className="H">
        <div className="HL">
          <span className="Hlogo">◈</span>
          <span className="Hpair">BTCUSDT</span>
          <span className="Hperp">PERPETUAL</span>
        </div>
        <div className="HR">
          <span className="Hticks">{h.ticks.toLocaleString()}</span>
          {/* Chart TF Toggle */}
          <div className="Utog" onClick={toggleChartTF}>
            <span className={`Utog-o ${chartTF === '1s' ? 'active' : ''}`}>1s</span>
            <span className={`Utog-o ${chartTF === '1m' ? 'active' : ''}`}>1m</span>
          </div>
          {/* Unit Toggle */}
          <div className="Utog" onClick={toggleUnit}>
            <span className={`Utog-o ${unit === 'BTC' ? 'active' : ''}`}>BTC</span>
            <span className={`Utog-o ${unit === 'USD' ? 'active' : ''}`}>USD</span>
          </div>
          <button className="Htheme" onClick={() => setShowHTF(!showHTF)} style={{ color: showHTF ? 'var(--g)' : 'var(--t2)' }}>DATA</button>
          <button className="Htheme" onClick={toggleTheme}>{theme === 'dark' ? '☀' : '☾'}</button>
          <span className={`Hled ${h.conn ? 'on' : ''}`} />
        </div>
      </header>

      {/* ═══ HTF DATA PANEL ═══ */}
      {showHTF && (
        <div className="HTF-panel">
          <div className="HTF-head"><span>MARKET STRUCTURE</span><span className="HTF-close" onClick={() => setShowHTF(false)}>×</span></div>
          <div className="HTF-grid">
            <div className="HTF-row head"><span>TF</span><span>PRICE</span><span>DELTA</span><span>VOL</span><span>SCORE</span></div>
            {h.htfCandles && h.htfCandles.map((c, i) => {
              const tf = ['5m', '15m', '1H', '4H', '1D'][i];
              if (!c) return null;
              const chg = c.close - c.open;
              return (
                <div key={tf} className="HTF-row">
                  <span className="HTF-tf">{tf}</span>
                  <span className={`HTF-val ${F.s(chg)}`}>{F.p0(c.close)}</span>
                  <span className={`HTF-val ${F.s(c.delta)}`}>{UF.fmtDelta(c.delta)}</span>
                  <span className="HTF-vol">{UF.fmtVol(c.buyVol + c.sellVol)}</span>
                  <span className={`HTF-score ${cls5(c.avgScore)}`}>{Math.round(c.avgScore)}</span>
                </div>
              );
            })}
          </div>
        </div>
      )}

      {/* ═══ PRICE INFO BAR ═══ */}
      <div className="PIB">

        <div className="PIB-price">
          <span className={`PIB-main ${c5}`}>{F.p0(h.price)}</span>
        </div>
        <div className="PIB-metrics">
          <div className={`PIB-score ${c5}`}>{fs > 0 ? '+' : ''}{fs}</div>
          <div className="PIB-sep">|</div>
          <div className={`PIB-state ${c5}`}>{state}</div>
          <div className="PIB-sep">|</div>
          <div className="PIB-conf">{confVal}% <span className="PIB-cl">CONFIDENCE</span></div>
          <div className="PIB-sep">|</div>
          <div className="PIB-lat">{h.lat || '—'} ms</div>
        </div>
      </div>

      {/* ═══ MAIN GRID ═══ */}
      <main className="G">

        {/* ═══ LEFT 50% — CONTEXT ═══ */}
        <section className="PL">

          {/* Chart */}
          <div className="PL-chart"><PriceChart onChartReady={onChart} /></div>

          {/* ═══ MARKET BIAS PANEL ═══ */}
          <div className="MBP" style={{ borderLeftColor: mBias.color }}>
            <div className="MBP-hd">
              <span className="MBP-title" style={{ color: mBias.color }}>{mBias.bias}</span>
              <span className="MBP-conf" style={{ color: mBias.color }}>{mBias.confidence}%
                <span className="MBP-cl">{mBias.confLabel}</span>
              </span>
            </div>
            {mBias.reasons.length > 0 && (
              <ul className="MBP-reasons">
                {mBias.reasons.map((r, i) => <li key={i}>{r}</li>)}
              </ul>
            )}
          </div>

          {/* Position Engine */}
          <div className="PE">
            <div className="PE-head"><span className="PE-icon">⚙</span> POSITION ENGINE</div>
            <div className="PE-body">

              {/* Gauge (compact, left side) */}
              <div className="PE-gauge">
                <FullGauge value={h.fs} conf={confVal} />
              </div>

              {/* Flow panels (right side)  */}
              <div className="PE-flows">
                {/* Aggressive Flow */}
                <div className="AF">
                  <div className="AF-top">
                    <span className="AF-label">AGGRESSIVE FLOW</span>
                    <span className={`AF-total ${F.s(h.delta1m)}`}>{UF.fmtDelta(h.delta1m)}</span>
                  </div>
                  <div className="AF-bar-wrap">
                    <span className="AF-val pos">+{UF.fmtVol(h.buyVol)}</span>
                    <div className="AF-bar">
                      <div className="AF-buy" style={{ width: `${buyW}%` }} />
                      <div className="AF-mid" />
                      <div className="AF-sell" style={{ width: `${sellW}%` }} />
                    </div>
                    <span className="AF-val neg">-{UF.fmtVol(h.sellVol)}</span>
                  </div>
                  <div className="AF-labels"><span>BUY</span><span>SELL</span></div>
                </div>

                {/* CVD Flow Momentum */}
                <div className="FM">
                  <div className="FM-top">
                    <span className="FM-label">FLOW MOMENTUM</span>
                    <span className={`FM-delta ${F.s(h.cvd)}`}>{F.c(h.cvd)}</span>
                  </div>
                  <div className="FM-body">
                    <span className={`FM-val ${F.s(h.cvd)}`}>{F.c(h.cvd)}</span>
                    <CVDSpark history={cvdH} width={180} height={30} />
                  </div>
                </div>
              </div>
            </div>
          </div>
        </section>

        {/* ═══ RIGHT 50% — DECISION ═══ */}
        <section className="PR">

          {/* Event Log Terminal (~60% height) */}
          <div className="EL">
            <div className="EL-header">
              <span className="EL-dot">◉</span>
              <span className="EL-title">EVENT LOG</span>
              {evts.slice(0, 3).map((e, i) => {
                let st = null;
                if (e.m.includes('Absorption')) st = 'ABSORPTION_BOTTOM';
                else if (e.m.includes('COVERING')) st = 'SHORT_COVERING';
                else if (e.m.includes('BUILDUP') || e.m.includes('Buildup')) st = e.m.includes('LONG') || e.m.includes('Long') ? 'LONG_BUILDUP' : 'SHORT_BUILDUP';
                if (!st) return null;
                return <span key={i} className="EL-badge" style={{ background: STATE_COLORS[st] }}>{st.replace(/_/g, ' ')}</span>;
              })}
              <span className="EL-arrow">▸</span>
            </div>
            <div className="EL-list">
              {evts.length === 0
                ? <div className="EL-empty">Monitoring orderflow...</div>
                : evts.map((e, i) => (
                  <div key={i} className={`EL-row ${e.c}`}>
                    <span className="EL-icon">{e.icon || '●'}</span>
                    <span className="EL-ts">{e.ts}</span>
                    <span className="EL-msg">{e.m}</span>
                    {e.btcSize > 0.001 && <span className="EL-size">{fmtEvSize(e)}</span>}
                    {e.price > 0 && <span className="EL-price">at ${F.p0(e.price)}</span>}
                    {e.detail && <span className="EL-detail">{e.detail}</span>}
                  </div>
                ))}
            </div>
          </div>

          {/* OI Behavior */}
          <div className="OI">
            <div className="OI-head"><span className="OI-ic">◈</span> OI BEHAVIOR</div>
            <div className="OI-body">
              <span className={`OI-val ${F.s(h.oid1m)}`}>{UF.fmtOI(h.oid1m)}</span>
              <div className="OI-bar-wrap">
                <div className={`OI-bar ${F.s(h.oid1m)}`} style={{ width: `${Math.min(100, Math.abs(h.oid1m || 0) / 50 * 100)}%` }} />
              </div>
              <span className={`OI-badge ${BEH_C[h.beh]}`}>{BEH[h.beh]}</span>
            </div>

            {/* DUAL FLOW METERS */}
            <div className="OI-meters">
              {/* Aggressive: Buy Vol vs Sell Vol */}
              <div className="OI-meter-row">
                <div className="OI-meter-hd">
                  <span className="OI-meter-lbl">AGGRESSIVE</span>
                  <span className="OI-meter-val">{Math.round(aggrBuyPct || 50)}% BUY</span>
                </div>
                <div className="OI-meter-track">
                  <div className="OI-meter-fill pos" style={{ width: `${aggrBuyPct || 50}%` }} />
                  <div className="OI-meter-fill neg" style={{ width: `${100 - (aggrBuyPct || 50)}%` }} />
                </div>
              </div>
              
              {/* Passive: Absorption Strength */}
              <div className="OI-meter-row">
                <div className="OI-meter-hd">
                  <span className="OI-meter-lbl">PASSIVE (ABSORPTION)</span>
                  <span className="OI-meter-val">{Math.round(psvBuyPct || 50)}% BUY</span>
                </div>
                <div className="OI-meter-track">
                  <div className="OI-meter-fill pos" style={{ width: `${psvBuyPct || 50}%`, opacity: 0.8 }} />
                  <div className="OI-meter-fill neg" style={{ width: `${100 - (psvBuyPct || 50)}%`, opacity: 0.8 }} />
                </div>
              </div>
            </div>
          </div>

          {/* Heatmap — 3 rows (Score/Delta/Vol) × 7 TFs */}
          <div className="HM">
            <div className="HM-head"><span className="HM-ic">◈</span> MARKET HEATMAP</div>
            <div className="HM-labels">
              <span className="HM-rl"> </span>
              {TF_LABELS.map(t => <span key={t} className="HM-tf">{t}</span>)}
            </div>
            <div className="HM-grid">
              {/* Row 1: SCORE */}
              <div className="HM-row">
                <span className="HM-rl">SCR</span>
                {htScores.map((s, i) => <div key={i} className="HM-cell" style={{ background: heatBg(s || 0) }} title={`Score: ${Math.round(s || 0)}`} />)}
              </div>
              {/* Row 2: DELTA */}
              <div className="HM-row">
                <span className="HM-rl">DLT</span>
                {htDeltas.map((d, i) => <div key={i} className="HM-cell" style={{ background: heatBg(normDelta(d, maxDelta)) }} title={`Delta: ${d?.toFixed(4) || 0}`} />)}
              </div>
              {/* Row 3: VOLUME */}
              <div className="HM-row">
                <span className="HM-rl">VOL</span>
                {htVols.map((v, i) => <div key={i} className="HM-cell" style={{ background: heatBg(normVol(v, maxVol)) }} title={`Vol: ${v?.toFixed(4) || 0}`} />)}
              </div>
            </div>
          </div>

          {/* State History */}
          <StateHistory events={evts} />
        </section>
      </main>
    </div>
  );
}
