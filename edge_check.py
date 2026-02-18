"""
edge_check.py — Quantitative Edge Validation for Market Indikator

Loads daily CSV snapshots and evaluates predictive power of:
  • Final composite pressure score (±40, ±60 thresholds)
  • Action hints (WATCH_LONG, WATCH_SHORT)
  • Event flags (momentum expansion, imbalance spikes)

Forward returns computed at +5s, +15s, +60s horizons.

For each condition, outputs:
  • Sample size (N)
  • Win rate (% of positive forward returns)
  • Mean return (bps)
  • Expectancy = winrate × avg_win - (1-winrate) × avg_loss

Statistical reasoning:
  A strategy has edge if expectancy > 0 AND sample size > 30.
  We use simple directional returns (no spread/slippage) as a
  first-pass filter. If edge survives here, it warrants deeper
  analysis with execution costs.

Usage:
  python edge_check.py                    # auto-loads latest CSV
  python edge_check.py logs/2026-02-17.csv  # specific file
"""

import sys
import os
import glob
import numpy as np
import pandas as pd
from pathlib import Path
from orderflow_state import classify_dataframe


# ─── CONFIG ───
FORWARD_HORIZONS = [5, 15, 60]  # seconds
STATE_HORIZONS = [60, 300, 900]  # 1m, 5m, 15m for state analysis
MIN_SAMPLE_SIZE = 10  # minimum N to report


def load_data(path: str | None = None) -> pd.DataFrame:
    """Load the latest CSV from logs/ or a specific file."""
    if path is None:
        files = sorted(glob.glob("logs/*.csv"))
        if not files:
            print("No CSV files found in logs/")
            sys.exit(1)
        path = files[-1]

    print(f"Loading: {path}")
    df = pd.read_csv(path)
    df["datetime"] = pd.to_datetime(df["timestamp"], unit="ms", utc=True)
    df = df.sort_values("timestamp").reset_index(drop=True)
    print(f"  Rows: {len(df):,}")
    print(f"  Time: {df['datetime'].iloc[0]} → {df['datetime'].iloc[-1]}")
    print(f"  Price: ${df['price'].iloc[0]:,.2f} → ${df['price'].iloc[-1]:,.2f}")
    print()
    return df


def compute_forward_returns(df: pd.DataFrame) -> pd.DataFrame:
    """Add forward return columns: fwd_5s, fwd_15s, fwd_60s (in bps)."""
    for h in FORWARD_HORIZONS:
        col = f"fwd_{h}s"
        df[col] = (df["price"].shift(-h) / df["price"] - 1) * 10_000  # bps
    return df


def evaluate_condition(
    df: pd.DataFrame,
    name: str,
    mask: pd.Series,
) -> dict:
    """Evaluate a single condition across all forward horizons."""
    subset = df[mask].dropna(subset=[f"fwd_{h}s" for h in FORWARD_HORIZONS])
    n = len(subset)

    result = {"condition": name, "N": n}

    if n < MIN_SAMPLE_SIZE:
        for h in FORWARD_HORIZONS:
            result[f"wr_{h}s"] = None
            result[f"mean_{h}s"] = None
            result[f"exp_{h}s"] = None
        return result

    for h in FORWARD_HORIZONS:
        col = f"fwd_{h}s"
        returns = subset[col].values

        wins = returns[returns > 0]
        losses = returns[returns <= 0]

        winrate = len(wins) / n if n > 0 else 0
        mean_ret = np.mean(returns)

        avg_win = np.mean(wins) if len(wins) > 0 else 0
        avg_loss = np.mean(np.abs(losses)) if len(losses) > 0 else 0
        expectancy = winrate * avg_win - (1 - winrate) * avg_loss

        result[f"wr_{h}s"] = winrate
        result[f"mean_{h}s"] = mean_ret
        result[f"exp_{h}s"] = expectancy

    return result


def run_analysis(df: pd.DataFrame):
    """Run all edge evaluations and print results."""

    # ─── DEFINE CONDITIONS ───
    conditions = [
        # Score thresholds
        ("finalScore > +40", df["final_score"] > 40),
        ("finalScore > +60", df["final_score"] > 60),
        ("finalScore < -40", df["final_score"] < -40),
        ("finalScore < -60", df["final_score"] < -60),

        # Action hints
        ("action = WATCH_LONG", df["action_hint"] == "WATCH_LONG"),
        ("action = WATCH_SHORT", df["action_hint"] == "WATCH_SHORT"),
        ("action = NO_TRADE", df["action_hint"] == "NO_TRADE"),

        # HTF bias
        ("htf = BULLISH", df["htf_bias"] == "BULLISH"),
        ("htf = BEARISH", df["htf_bias"] == "BEARISH"),

        # Market state
        ("state = TRENDING_UP", df["market_state"] == "TRENDING_UP"),
        ("state = TRENDING_DOWN", df["market_state"] == "TRENDING_DOWN"),
        ("state = PULLBACK_IN_UPTREND", df["market_state"] == "PULLBACK_IN_UPTREND"),
        ("state = RALLY_INTO_RESISTANCE", df["market_state"] == "RALLY_INTO_RESISTANCE"),

        # Orderbook extremes
        ("ob_score > +40", df["ob_score"] > 40),
        ("ob_score < -40", df["ob_score"] < -40),

        # OI behavior
        ("behavior = LONG_BUILDUP", df["behavior"] == 1),
        ("behavior = SHORT_BUILDUP", df["behavior"] == 2),

        # Combined conditions (higher conviction)
        ("BULL: score>40 + action=LONG",
         (df["final_score"] > 40) & (df["action_hint"] == "WATCH_LONG")),
        ("BEAR: score<-40 + action=SHORT",
         (df["final_score"] < -40) & (df["action_hint"] == "WATCH_SHORT")),

        # Score + OI confirmation
        ("BULL: score>40 + long_buildup",
         (df["final_score"] > 40) & (df["behavior"] == 1)),
        ("BEAR: score<-40 + short_buildup",
         (df["final_score"] < -40) & (df["behavior"] == 2)),
    ]

    # ─── EVALUATE ───
    results = []
    for name, mask in conditions:
        results.append(evaluate_condition(df, name, mask))

    # ─── DISPLAY ───
    print("=" * 100)
    print("EDGE ANALYSIS REPORT")
    print("=" * 100)
    print()

    for h in FORWARD_HORIZONS:
        print(f"─── Forward Return: +{h}s ───")
        print(f"{'Condition':<42} {'N':>6} {'WinRate':>8} {'Mean(bps)':>10} {'Expectancy':>11}")
        print("-" * 80)

        for r in results:
            wr = r.get(f"wr_{h}s")
            mean = r.get(f"mean_{h}s")
            exp = r.get(f"exp_{h}s")

            if wr is None:
                print(f"{r['condition']:<42} {r['N']:>6}   {'(insufficient data)':>30}")
            else:
                marker = ""
                if exp is not None and exp > 0.5 and r["N"] >= 30:
                    marker = " ✓ EDGE"
                elif exp is not None and exp < -0.5 and r["N"] >= 30:
                    marker = " ✗ NEG"

                print(
                    f"{r['condition']:<42} {r['N']:>6} "
                    f"{wr:>7.1%} {mean:>+10.2f} {exp:>+11.2f}{marker}"
                )

        print()

    # ─── INTERPRETATION ───
    print("=" * 100)
    print("INTERPRETATION GUIDE:")
    print("  • Expectancy > 0 with N > 30 suggests potential edge")
    print("  • ✓ EDGE = expectancy > 0.5 bps with sufficient samples")
    print("  • Higher timeframe returns (+60s) are more robust indicators")
    print("  • Cross-validate: if +5s and +60s both show edge, signal is strong")
    print("  • Next step: add spread + slippage costs to filter spurious edges")
    print("=" * 100)


# ═══════════════════════════════════════════════════════════════
#  ORDERFLOW POSITION STATE ANALYSIS
# ═══════════════════════════════════════════════════════════════

def run_state_analysis(df: pd.DataFrame):
    """Classify orderflow states and produce statistical report."""

    # Classify every row
    df["of_state"] = classify_dataframe(df)

    # Compute forward returns at state horizons (+1m, +5m, +15m)
    for h in STATE_HORIZONS:
        col = f"sfwd_{h}s"
        if col not in df.columns:
            df[col] = (df["price"].shift(-h) / df["price"] - 1) * 10_000  # bps

    ALL_STATES = [
        "LONG_BUILDUP", "SHORT_BUILDUP", "SHORT_COVERING",
        "LONG_LIQUIDATION", "ABSORPTION_BOTTOM", "DISTRIBUTION_TOP",
        "NEUTRAL_CHOP",
    ]

    print()
    print("=" * 100)
    print("ORDERFLOW POSITION STATE REPORT")
    print("=" * 100)
    print()

    # ─── 1. STATE FREQUENCY ───
    print("─── State Frequency ───")
    counts = df["of_state"].value_counts()
    total = len(df)
    print(f"{'State':<24} {'Count':>8} {'Pct':>8}")
    print("-" * 44)
    for st in ALL_STATES:
        n = counts.get(st, 0)
        pct = n / total * 100 if total > 0 else 0
        print(f"{st:<24} {n:>8} {pct:>7.1f}%")
    print(f"{'TOTAL':<24} {total:>8}")
    print()

    # ─── 2. FORWARD RETURNS PER STATE ───
    for h in STATE_HORIZONS:
        col = f"sfwd_{h}s"
        label = f"+{h//60}m" if h >= 60 else f"+{h}s"
        print(f"─── Forward Return {label} by Orderflow State ───")
        print(f"{'State':<24} {'N':>6} {'WinRate':>8} {'Mean(bps)':>10} {'Median':>8} {'Expectancy':>11}")
        print("-" * 72)

        for st in ALL_STATES:
            mask = df["of_state"] == st
            subset = df[mask].dropna(subset=[col])
            n = len(subset)

            if n < MIN_SAMPLE_SIZE:
                print(f"{st:<24} {n:>6}   {'(insufficient)':>40}")
                continue

            returns = subset[col].values
            wins = returns[returns > 0]
            losses = returns[returns <= 0]
            wr = len(wins) / n
            mean = np.mean(returns)
            med = np.median(returns)
            avg_win = np.mean(wins) if len(wins) > 0 else 0
            avg_loss = np.mean(np.abs(losses)) if len(losses) > 0 else 0
            exp = wr * avg_win - (1 - wr) * avg_loss

            marker = ""
            if exp > 0.5 and n >= 30:
                marker = " ✓"
            elif exp < -0.5 and n >= 30:
                marker = " ✗"

            print(f"{st:<24} {n:>6} {wr:>7.1%} {mean:>+10.2f} {med:>+8.2f} {exp:>+11.2f}{marker}")

        print()

    # ─── 3. REVERSAL ANALYSIS ───
    print("─── Reversal Probability Analysis ───")
    print()
    reversal_states = [
        ("ABSORPTION_BOTTOM", "bullish", 1),   # expect price UP after
        ("DISTRIBUTION_TOP",  "bearish", -1),  # expect price DOWN after
    ]

    for state_name, direction, sign in reversal_states:
        mask = df["of_state"] == state_name
        subset = df[mask]
        n = len(subset)
        print(f"  {state_name} (N={n})")

        if n < MIN_SAMPLE_SIZE:
            print(f"    → insufficient data (need ≥{MIN_SAMPLE_SIZE})")
            print()
            continue

        for h in STATE_HORIZONS:
            col = f"sfwd_{h}s"
            valid = subset.dropna(subset=[col])
            nv = len(valid)
            if nv < 5:
                continue

            returns = valid[col].values
            # Reversal = price moved in expected direction
            reversals = np.sum(returns * sign > 0)
            rev_pct = reversals / nv * 100
            mean_ret = np.mean(returns) * sign  # positive = correct direction

            label = f"+{h//60}m" if h >= 60 else f"+{h}s"
            verdict = "✓ REVERSAL EDGE" if rev_pct > 55 and mean_ret > 0.5 else ""
            print(f"    {label}: reversal {rev_pct:5.1f}% ({reversals}/{nv})  "
                  f"mean {mean_ret:+.2f} bps  {verdict}")

        print()

    # ─── 4. VERDICT ───
    print("=" * 100)
    print("POSITION STATE VERDICT:")
    print("  • States with positive expectancy at +5m/+15m may indicate predictive power")
    print("  • ABSORPTION_BOTTOM + reversal >55% → potential bottom detection")
    print("  • DISTRIBUTION_TOP + reversal >55% → potential top detection")
    print("  • Combine with HTF bias for higher conviction entries")
    print("  • Collect ≥8h of data for statistically meaningful results")
    print("=" * 100)


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else None
    df = load_data(path)
    df = compute_forward_returns(df)
    run_analysis(df)
    run_state_analysis(df)


if __name__ == "__main__":
    main()
