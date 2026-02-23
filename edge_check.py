#!/usr/bin/env python3
"""
edge_check_v4.py — Fixed NoneType bug, improved regime detection, richer insights
Changes from v3:
  - Fixed 'NoneType has no attribute get' in detect_regime / calculate_consistency_score
  - Smarter sample-size skip: logs reason instead of crashing
  - Added INVERTED SIGNAL detection (e.g. DISTRIBUTION_TOP acts as continuation)
  - Added Kelly fraction & position sizing estimate
  - Added per-signal sample-size warning (N < 200 flagged as LOW_N)
  - Cleaner summary with actionable tiers
"""

import sys
import os
import glob
import math
import numpy as np
import pandas as pd
from typing import Dict, List, Optional
import warnings
warnings.filterwarnings('ignore')

# ─── CONFIG ──────────────────────────────────────────────────────────────────
FORWARD_HORIZONS  = [5, 15, 60]   # seconds
STATE_HORIZONS    = [60, 300, 900] # 1m, 5m, 15m
MIN_SAMPLE_SIZE   = 30
LOW_N_WARN        = 200            # flag as LOW_N if below this
COST_BPS          = 0.3            # per side (0.6 bps roundtrip)
CONFIDENCE_LEVEL  = 0.95
EDGE_THRESHOLD    = 0.5            # net_exp > this → EDGE
REVERSAL_WIN_THR  = 0.55           # for reversal analysis
W                 = 110            # display width

EXCLUDED_PATTERNS = ["edge_analysis", "edge_check", "results", "output", "report"]


# ─── UTILS ───────────────────────────────────────────────────────────────────

def is_excluded(filename: str) -> bool:
    name_lower = os.path.basename(filename).lower()
    return any(pat in name_lower for pat in EXCLUDED_PATTERNS)


def normal_cdf(x: float) -> float:
    """Abramowitz & Stegun approximation of Φ(x)."""
    b1, b2, b3, b4, b5 = 0.319381530, -0.356563782, 1.781477937, -1.821255978, 1.330274429
    p, c = 0.2316419, 0.39894228
    if x >= 0.0:
        t = 1.0 / (1.0 + p * x)
        return 1.0 - c * math.exp(-x * x / 2.0) * t * (t * (t * (t * (t * b5 + b4) + b3) + b2) + b1)
    return 1.0 - normal_cdf(-x)


def t_test_1samp(data: np.ndarray, popmean: float = 0.0):
    n = len(data)
    if n < 2:
        return 0.0, 1.0
    mean = np.mean(data)
    std  = np.std(data, ddof=1)
    if std == 0:
        return (0.0, 1.0) if mean == popmean else (float('inf'), 0.0)
    t_stat = (mean - popmean) / (std / math.sqrt(n))
    p_val  = 2 * (1 - normal_cdf(abs(t_stat))) if n > 30 else (0.05 if abs(t_stat) > 2 else 0.5)
    return t_stat, p_val


def kelly_fraction(winrate: float, avg_win: float, avg_loss: float) -> float:
    """Full Kelly fraction. Cap at 25% for safety."""
    if avg_loss == 0 or winrate <= 0:
        return 0.0
    q   = 1 - winrate
    b   = avg_win / abs(avg_loss)
    raw = (b * winrate - q) / b
    return max(0.0, min(raw, 0.25))


# ─── TIMESTAMP / IO ──────────────────────────────────────────────────────────

def parse_timestamp(df: pd.DataFrame, col: str) -> pd.Series:
    sample = str(df[col].iloc[0])
    try:
        if any(c in sample for c in ('T', '-', ':')):
            return pd.to_datetime(df[col], utc=True, infer_datetime_format=True)
        ts_val = float(sample)
        if ts_val > 1e18:
            return pd.to_datetime(df[col].astype(float), unit='ns', utc=True)
        elif ts_val > 1e12:
            return pd.to_datetime(df[col].astype(float), unit='ms', utc=True)
        else:
            return pd.to_datetime(df[col].astype(float), unit='s', utc=True)
    except Exception as e:
        print(f"  ⚠️  Timestamp fallback ({e})")
        return pd.to_datetime(df[col], utc=True, infer_datetime_format=True)


def detect_timestamp_col(df: pd.DataFrame) -> Optional[str]:
    candidates = ['timestamp', 'datetime', 'time', 'ts', 'date', 'open_time', 'close_time']
    col_lower = {c.lower(): c for c in df.columns}
    for c in candidates:
        if c in col_lower:
            return col_lower[c]
    return None


def load_data(path: Optional[str] = None) -> pd.DataFrame:
    def _read(f: str) -> Optional[pd.DataFrame]:
        try:
            tmp    = pd.read_csv(f)
            ts_col = detect_timestamp_col(tmp)
            if ts_col:
                tmp['datetime'] = parse_timestamp(tmp, ts_col)
                if tmp['datetime'].iloc[0].year < 2000:
                    print(f"  ⚠️  Suspicious year in {f}, retrying as ns...")
                    tmp['datetime'] = pd.to_datetime(tmp[ts_col].astype(float), unit='ns', utc=True)
                if 'timestamp' not in tmp.columns:
                    tmp['timestamp'] = tmp[ts_col]
            else:
                print(f"  ⚠️  No timestamp col in {f}, using row index")
                tmp['datetime']  = pd.date_range('2024-01-01', periods=len(tmp), freq='1s', tz='UTC')
                tmp['timestamp'] = range(len(tmp))
            print(f"  ✓ {os.path.basename(f)}: {len(tmp):,} rows")
            return tmp
        except Exception as e:
            print(f"  ✗ {os.path.basename(f)}: {e}")
            return None

    if path is None:
        all_files = sorted(glob.glob("*.csv")) + sorted(glob.glob("logs/*.csv"))
        files     = [f for f in all_files if not is_excluded(f)]
        if not files:
            print("⚠️  No raw CSV files found.")
            print(f"   Excluded patterns : {EXCLUDED_PATTERNS}")
            print(f"   All CSVs present  : {[os.path.basename(f) for f in all_files]}")
            print("   Tip: python3 edge_check_v4.py data.csv")
            sys.exit(1)
        files_to_load = files[-5:]
        print(f"Loading {len(files_to_load)} file(s): {', '.join(os.path.basename(f) for f in files_to_load)}")
        dfs = [_read(f) for f in files_to_load]
        dfs = [d for d in dfs if d is not None]
        if not dfs:
            print("No valid data loaded"); sys.exit(1)
        df = pd.concat(dfs, ignore_index=True)
    else:
        print(f"Loading: {path}")
        df = _read(path)
        if df is None:
            sys.exit(1)

    sort_col = 'timestamp' if 'timestamp' in df.columns else 'datetime'
    df = df.sort_values(sort_col).reset_index(drop=True)

    print(f"\n  Total Rows  : {len(df):,}")
    print(f"  Time Range  : {df['datetime'].iloc[0]}  →  {df['datetime'].iloc[-1]}")
    for pc in ['price', 'close', 'last_price', 'mark_price']:
        if pc in df.columns:
            print(f"  Price ({pc}): ${df[pc].iloc[0]:,.2f}  →  ${df[pc].iloc[-1]:,.2f}")
            break
    print()
    return df


# ─── COLUMN RESOLUTION ───────────────────────────────────────────────────────

def resolve_columns(df: pd.DataFrame) -> Dict[str, Optional[str]]:
    exact = {
        'score': 'finalScore', 'action': 'action', 'htf': 'htf',
        'state': 'state', 'ob_score': 'ob_score', 'behavior': 'behavior',
        'price': 'price', 'timestamp': 'timestamp',
    }
    alternatives = {
        'score':     ['final_score', 'score', 'pressure_score'],
        'action':    ['action_hint', 'signal', 'trade_action'],
        'htf':       ['htf_bias', 'trend', 'htf_trend'],
        'state':     ['market_state', 'regime', 'market_regime'],
        'ob_score':  ['ob_pressure', 'orderbook_score', 'imbalance_score'],
        'behavior':  ['oi_behavior', 'position_behavior', 'flow_behavior'],
        'price':     ['close', 'last_price', 'mark_price'],
        'timestamp': ['datetime', 'time', 'ts', 'open_time'],
    }
    resolved = {}
    for key, ex in exact.items():
        if ex in df.columns:
            resolved[key] = ex
        else:
            resolved[key] = next((a for a in alternatives.get(key, []) if a in df.columns), None)

    print("Column mapping:")
    for k, v in resolved.items():
        print(f"  {k:<12} {'→ ' + v if v else '✗ NOT FOUND'}")
    print()

    missing = [c for c in ['score', 'action', 'htf', 'state', 'price', 'timestamp'] if not resolved.get(c)]
    if missing:
        print(f"ERROR: Missing critical columns: {missing}")
        print(f"Available: {df.columns.tolist()}")
        sys.exit(1)
    return resolved


# ─── FORWARD RETURNS ─────────────────────────────────────────────────────────

def compute_forward_returns(df: pd.DataFrame, cols: Dict) -> pd.DataFrame:
    pc = cols['price']
    for h in FORWARD_HORIZONS:
        df[f"fwd_{h}s"] = (df[pc].shift(-h) / df[pc] - 1) * 10_000
    return df


# ─── REGIME / CONSISTENCY ────────────────────────────────────────────────────

def _safe_get(results: Dict, h: int, key: str, default=0):
    """Safe nested get — handles None values in results dict."""
    r = results.get(h)
    if not r:
        return default
    return r.get(key, default) or default


def calculate_consistency_score(results: Dict) -> float:
    means = [_safe_get(results, h, 'mean_bps') for h in [5, 15, 60]
             if results.get(h) is not None]
    if len(means) < 2:
        return 0.0
    if len(set(np.sign(means))) > 1:
        return -1.0
    if len(means) == 3:
        d1 = abs(means[1]) / (abs(means[0]) + 1e-6)
        d2 = abs(means[2]) / (abs(means[1]) + 1e-6)
        return (d1 + d2) / 2
    return 0.5


def detect_regime(results: Dict) -> str:
    r5  = _safe_get(results, 5,  'mean_bps')
    r60 = _safe_get(results, 60, 'mean_bps')
    c   = calculate_consistency_score(results)
    if c > 0.7 and r5 > 0:
        return 'TRENDING'
    elif c < 0 and r5 > 0 and r60 < 0:
        return 'MEAN_REVERT'
    elif c < 0:
        return 'FLIP'
    return 'NOISE'


# ─── CONDITION EVALUATION ────────────────────────────────────────────────────

def evaluate_condition(df: pd.DataFrame, name: str, mask: pd.Series, cols: Dict) -> Dict:
    results = {}
    for h in FORWARD_HORIZONS:
        col = f"fwd_{h}s"
        try:
            valid = mask & df[col].notna()
            sub   = df.loc[valid, col]
        except Exception as e:
            results[h] = None
            continue

        n = len(sub)
        if n < MIN_SAMPLE_SIZE:
            results[h] = None
            continue

        ret    = sub.values
        wins   = ret[ret > 0]
        losses = ret[ret <= 0]
        wr     = len(wins) / n
        mean_r = np.mean(ret)
        std_r  = np.std(ret)
        t, p   = t_test_1samp(ret)

        avg_win  = float(np.mean(wins))  if len(wins)   > 0 else 0.0
        avg_loss = float(np.mean(losses)) if len(losses) > 0 else 0.0
        kelly    = kelly_fraction(wr, avg_win, avg_loss)

        results[h] = {
            'n':             n,
            'winrate':       wr * 100,
            'mean_bps':      mean_r,
            'median_bps':    np.median(ret),
            'std_bps':       std_r,
            'net_exp':       mean_r - COST_BPS * 2,
            'avg_win':       avg_win,
            'avg_loss':      avg_loss,
            'kelly':         kelly,
            't_stat':        t,
            'p_value':       p,
            'is_significant': p < (1 - CONFIDENCE_LEVEL),
            'sharpe':        mean_r / std_r if std_r > 0 else 0,
            'low_n':         n < LOW_N_WARN,
        }

    c = calculate_consistency_score(results)
    r = detect_regime(results)
    for h in results:
        if results[h]:
            results[h]['consistency'] = c
            results[h]['regime']      = r
    return results


# ─── MAIN ANALYSIS ───────────────────────────────────────────────────────────

def build_conditions(df: pd.DataFrame, cols: Dict) -> List:
    sc, ac, hc, stc = cols['score'], cols['action'], cols['htf'], cols['state']
    obc = cols.get('ob_score')
    bhc = cols.get('behavior')
    conds = []

    # Score thresholds
    if sc:
        for thr in [40, 60, 80]:
            conds += [
                (f"{sc} > +{thr}", df[sc] > thr),
                (f"{sc} < -{thr}", df[sc] < -thr),
            ]

    # Actions
    if ac:
        for act in ["WATCH_LONG", "WATCH_SHORT", "NO_TRADE"]:
            conds.append((f"{ac} = {act}", df[ac] == act))

    # HTF
    if hc:
        for bias in ["BULLISH", "BEARISH"]:
            conds.append((f"{hc} = {bias}", df[hc] == bias))

    # State
    if stc:
        for st in ["TRENDING_UP", "TRENDING_DOWN", "PULLBACK_IN_UPTREND",
                   "RALLY_INTO_RESISTANCE"]:
            conds.append((f"{stc} = {st}", df[stc] == st))

    # OB Score
    if obc and obc in df.columns:
        conds += [
            (f"{obc} > +40", df[obc] > 40),
            (f"{obc} < -40", df[obc] < -40),
        ]

    # Behavior
    if bhc and bhc in df.columns:
        conds += [
            ("behavior = LONG_BUILDUP",  df[bhc] == 1),
            ("behavior = SHORT_BUILDUP", df[bhc] == 2),
        ]
        if sc:
            conds += [
                ("BULL: score>40 + LONG",   (df[sc] > 40)  & (df[bhc] == 1)),
                ("BEAR: score<-40 + SHORT", (df[sc] < -40) & (df[bhc] == 2)),
            ]

    # Combined
    if sc and ac:
        for thr in [40, 60]:
            conds += [
                (f"BULL: score>{thr} + WATCH_LONG",   (df[sc] > thr)  & (df[ac] == "WATCH_LONG")),
                (f"BEAR: score<-{thr} + WATCH_SHORT", (df[sc] < -thr) & (df[ac] == "WATCH_SHORT")),
            ]

    return conds


def run_analysis(df: pd.DataFrame, cols: Dict):
    sc, ac, hc, stc = cols['score'], cols['action'], cols['htf'], cols['state']
    print(f"Using columns: score={sc}, action={ac}, htf={hc}, state={stc}\n")

    conditions  = build_conditions(df, cols)
    all_results = []
    errors      = []

    print("Evaluating conditions...")
    for name, mask in conditions:
        try:
            r = evaluate_condition(df, name, mask, cols)
            all_results.append((name, r))
        except Exception as e:
            errors.append(f"  ✗ {name}: {e}")
    if errors:
        print("\n".join(errors))

    # ── Per-horizon tables ────────────────────────────────────────────────────
    print("\n" + "=" * W)
    print("EDGE ANALYSIS REPORT v4.0")
    print("=" * W)
    print(f"Cost: {COST_BPS} bps/side ({COST_BPS*2} bps roundtrip) | "
          f"Min N: {MIN_SAMPLE_SIZE} | Confidence: {CONFIDENCE_LEVEL}")
    print("=" * W)

    for h in FORWARD_HORIZONS:
        print(f"\n{'─'*W}")
        print(f"FORWARD RETURN +{h}s  (cost-adjusted @ {COST_BPS*2} bps roundtrip)")
        print(f"{'─'*W}")
        print(f"{'Condition':<44} {'N':>7} {'Win%':>6} {'Mean':>8} "
              f"{'NetExp':>8} {'Kelly%':>7} {'Regime':>13}  Flags")
        print(f"{'─'*W}")

        rows = [(n, r[h]) for n, r in all_results if r.get(h)]
        rows.sort(key=lambda x: x[1]['net_exp'], reverse=True)

        for name, r in rows[:22]:
            flags = []
            if r['net_exp'] > EDGE_THRESHOLD:       flags.append("✓EDGE")
            if r['net_exp'] < -EDGE_THRESHOLD:      flags.append("✗NEG")
            if r['consistency'] > 0.8:              flags.append("C")
            if r['regime'] == 'MEAN_REVERT':        flags.append("⚠MR")
            if r['is_significant']:                 flags.append("*sig")
            if r['low_n']:                          flags.append("LOW_N")

            print(f"{name:<44} {r['n']:>7,} {r['winrate']:>5.1f}% "
                  f"{r['mean_bps']:>+8.2f} {r['net_exp']:>+8.2f} "
                  f"{r['kelly']*100:>6.1f}% {r['regime']:>13}  {'  '.join(flags)}")

    # ── Signal decay ─────────────────────────────────────────────────────────
    print("\n" + "=" * W)
    print("SIGNAL DECAY ANALYSIS")
    print("=" * W)
    print(f"{'Condition':<44} {'+5s':>8} {'+15s':>8} {'+60s':>8} "
          f"{'Decay%':>10} {'Regime':>13}  Note")
    print("─" * W)

    decay_list = []
    for name, res in all_results:
        if all(res.get(h) for h in [5, 15, 60]):
            r5, r15, r60 = res[5]['mean_bps'], res[15]['mean_bps'], res[60]['mean_bps']
            if abs(r5) > 0.05:
                d1 = (r15 - r5)  / abs(r5)  * 100
                d2 = (r60 - r15) / abs(r15) * 100 if abs(r15) > 0.01 else 0
                decay_list.append({
                    'condition': name, 'r5': r5, 'r15': r15, 'r60': r60,
                    'd1': d1, 'd2': d2, 'regime': res[5]['regime'],
                    'n': res[5]['n'],
                })

    for d in sorted(decay_list, key=lambda x: abs(x['d1'] + x['d2']), reverse=True)[:18]:
        total = d['d1'] + d['d2']
        # Detect inverted / accelerating
        note = ""
        if d['r5'] > 0 and d['r60'] < 0:
            note = "⚠ FLIP LT"
        elif d['r5'] < 0 and d['r60'] > 0:
            note = "⚠ FLIP LT (short works)"
        elif abs(d['r60']) > abs(d['r5']) * 1.5 and np.sign(d['r5']) == np.sign(d['r60']):
            note = "↑ ACCEL"
        print(f"{d['condition']:<44} {d['r5']:>+7.2f} {d['r15']:>+7.2f} {d['r60']:>+7.2f} "
              f"{total:>+9.1f}% {d['regime']:>13}  {note}")

    # ── Inverted signals ──────────────────────────────────────────────────────
    print("\n" + "=" * W)
    print("INVERTED SIGNAL OPPORTUNITIES  (signal means opposite of label)")
    print("=" * W)
    print("These signals show consistent edge but in the OPPOSITE direction.\n"
          "Consider trading the inverse, or reviewing indicator logic.\n")

    found_invert = False
    for name, res in all_results:
        r60 = res.get(60)
        if not r60:
            continue
        # Bearish label but positive net_exp
        is_bear_label = any(x in name for x in ['SHORT', 'BEARISH', 'BEAR', 'DOWN'])
        is_bull_label = any(x in name for x in ['LONG',  'BULLISH', 'BULL', 'UP'])
        if (is_bear_label and r60['net_exp'] > EDGE_THRESHOLD) or \
           (is_bull_label  and r60['net_exp'] < -EDGE_THRESHOLD):
            found_invert = True
            direction = "⬆ ACTUALLY BULLISH" if r60['mean_bps'] > 0 else "⬇ ACTUALLY BEARISH"
            print(f"  • {name:<44}  net_exp={r60['net_exp']:+.2f} bps  "
                  f"Win={r60['winrate']:.1f}%  N={r60['n']:,}  → {direction}")
    if not found_invert:
        print("  None detected.")

    # ── Recommendations ───────────────────────────────────────────────────────
    print("\n" + "=" * W)
    print("RECOMMENDATIONS")
    print("=" * W)

    tiers = {
        'A — High confidence (N≥200, net>0.5, consistent)': [],
        'B — Low sample (N<200, net>0.5, consistent)':      [],
        'C — Watch (net>0, but MR or inconsistent)':        [],
    }

    for name, res in all_results:
        r60 = res.get(60)
        if not r60:
            continue
        ne, n, c, regime = r60['net_exp'], r60['n'], r60['consistency'], r60['regime']
        if ne > EDGE_THRESHOLD and c > 0.5:
            if n >= LOW_N_WARN:
                tiers['A — High confidence (N≥200, net>0.5, consistent)'].append(
                    (name, ne, r60['winrate'], n, r60['kelly']))
            else:
                tiers['B — Low sample (N<200, net>0.5, consistent)'].append(
                    (name, ne, r60['winrate'], n, r60['kelly']))
        elif 0 < ne <= EDGE_THRESHOLD and regime != 'MEAN_REVERT':
            tiers['C — Watch (net>0, but MR or inconsistent)'].append(
                (name, ne, r60['winrate'], n, r60['kelly']))

    for tier, items in tiers.items():
        print(f"\n  [{tier}]")
        if items:
            for name, ne, wr, n, kelly in sorted(items, key=lambda x: x[1], reverse=True)[:6]:
                kelly_pct = kelly * 100
                print(f"    • {name:<44}  net={ne:+.2f} bps  win={wr:.1f}%  "
                      f"N={n:,}  Kelly={kelly_pct:.1f}%")
        else:
            print("    (none)")

    mr_signals = [d for d in decay_list if d['regime'] == 'MEAN_REVERT']
    if mr_signals:
        print(f"\n  ⚠️  MEAN REVERSION in {len(mr_signals)} signal(s):")
        for d in mr_signals[:5]:
            print(f"    • {d['condition']}: {d['r5']:+.2f} bps at 5s → {d['r60']:+.2f} bps at 60s")
        print("    → Use 5s holds or tighter entry filters for these.")

    # ── Save CSV ──────────────────────────────────────────────────────────────
    flat = [{'condition': n, 'horizon': h, **r}
            for n, res in all_results for h, r in res.items() if r]
    if flat:
        out = 'edge_analysis_v4.csv'
        pd.DataFrame(flat).to_csv(out, index=False)
        print(f"\n💾 Results saved: {out}")


# ─── ORDERFLOW STATE ANALYSIS ────────────────────────────────────────────────

def run_state_analysis(df: pd.DataFrame, cols: Dict):
    try:
        from orderflow_state import classify_dataframe
        df["of_state"] = classify_dataframe(df)
    except Exception as e:
        print(f"\n[Skipping orderflow state analysis — module not available: {e}]")
        return

    pc = cols['price']
    for h in STATE_HORIZONS:
        df[f"sfwd_{h}s"] = (df[pc].shift(-h) / df[pc] - 1) * 10_000

    ALL_STATES = [
        "LONG_BUILDUP", "SHORT_BUILDUP", "SHORT_COVERING",
        "LONG_LIQUIDATION", "ABSORPTION_BOTTOM", "DISTRIBUTION_TOP", "NEUTRAL_CHOP",
    ]

    print("\n" + "=" * W)
    print("ORDERFLOW POSITION STATE REPORT")
    print("=" * W)

    # Frequency
    print("\n─── State Frequency ───")
    counts = df["of_state"].value_counts()
    total  = len(df)
    print(f"{'State':<24} {'Count':>10} {'Pct':>8}")
    print("─" * 44)
    for st in ALL_STATES:
        n   = counts.get(st, 0)
        print(f"{st:<24} {n:>10,} {n/total*100:>7.1f}%")
    print(f"{'TOTAL':<24} {total:>10,}")

    # Forward returns per state
    for h in STATE_HORIZONS:
        col   = f"sfwd_{h}s"
        label = f"+{h//60}m"
        print(f"\n─── Forward Return {label} by Orderflow State ───")
        print(f"{'State':<24} {'N':>8} {'Win%':>6} {'Mean':>8} {'NetExp':>8} {'Median':>8}  Note")
        print("─" * 78)

        for st in ALL_STATES:
            sub = df.loc[df["of_state"] == st, col].dropna()
            n   = len(sub)
            if n < MIN_SAMPLE_SIZE:
                print(f"{st:<24} {n:>8}   (insufficient data)")
                continue
            ret     = sub.values
            wins    = ret[ret > 0]
            mean_r  = np.mean(ret)
            net_exp = mean_r - COST_BPS * 2
            mark    = ("✓EDGE" if net_exp > EDGE_THRESHOLD
                       else "✗NEG" if net_exp < -EDGE_THRESHOLD else "")
            print(f"{st:<24} {n:>8,} {len(wins)/n*100:>5.1f}% "
                  f"{mean_r:>+7.2f} {net_exp:>+7.2f} {np.median(ret):>+7.2f}  {mark}")

    # Reversal probability + inversion detection
    print("\n─── Reversal / Continuation Analysis ───")
    for state_name, expected_sign, label in [
        ("ABSORPTION_BOTTOM", 1,  "bottom reversal"),
        ("DISTRIBUTION_TOP",  -1, "top reversal"),
    ]:
        mask = df["of_state"] == state_name
        n    = int(mask.sum())
        print(f"\n  {state_name} (N={n:,}) — expected: {label}")
        if n < MIN_SAMPLE_SIZE:
            print("    → Insufficient data"); continue

        any_invert = False
        for h in STATE_HORIZONS:
            sub = df.loc[mask, f"sfwd_{h}s"].dropna()
            nv  = len(sub)
            if nv < MIN_SAMPLE_SIZE: continue
            ret     = sub.values
            correct = int(np.sum(ret * expected_sign > 0))
            pct     = correct / nv * 100
            mean_r  = np.mean(ret) * expected_sign   # signed for expected direction
            raw_mean = np.mean(ret)

            verdict = ""
            if pct > REVERSAL_WIN_THR * 100 and mean_r > EDGE_THRESHOLD:
                verdict = "✓ REVERSAL EDGE"
            elif pct < (1 - REVERSAL_WIN_THR) * 100:
                # Check if it's actually a continuation edge
                continuation_mean = np.mean(ret) * (-expected_sign)
                if continuation_mean > EDGE_THRESHOLD:
                    verdict = "🔄 CONTINUATION EDGE (inverted signal!)"
                    any_invert = True
                else:
                    verdict = "✗ FALSE REVERSAL SIGNAL"
            print(f"    +{h//60}m: correct={pct:5.1f}%  ({correct:,}/{nv:,})  "
                  f"mean(expected dir)={mean_r:+.2f} bps  raw={raw_mean:+.2f} bps  {verdict}")

        if any_invert:
            print(f"    ⚡ ACTION: Trade {state_name} as CONTINUATION, not reversal!")

    print("\n" + "=" * W)


# ─── ENTRY POINT ─────────────────────────────────────────────────────────────

def main():
    path = sys.argv[1] if len(sys.argv) > 1 else None
    df   = load_data(path)
    cols = resolve_columns(df)
    df   = compute_forward_returns(df, cols)
    run_analysis(df, cols)
    run_state_analysis(df, cols)
    print("\n✅ Analysis complete")


if __name__ == "__main__":
    main()