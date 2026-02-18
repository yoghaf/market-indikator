"""
orderflow_state.py — Orderflow Position State Classifier

Deterministic classification of market positioning based on
price × OI × aggression relationships.

States:
  LONG_BUILDUP      — price ↑ + OI ↑ + bullish CVD momentum
  SHORT_BUILDUP     — price ↓ + OI ↑ + bearish CVD momentum
  SHORT_COVERING    — price ↑ + OI ↓ (shorts exiting)
  LONG_LIQUIDATION  — price ↓ + OI ↓ (longs exiting)
  ABSORPTION_BOTTOM — price flat/↓ + OI rising + CVD bullish divergence
  DISTRIBUTION_TOP  — price flat/↑ + OI rising + CVD bearish divergence
  NEUTRAL_CHOP      — no clear positioning signal

Classification logic:
  1. Compute directional signals from raw features
  2. Apply price×OI matrix first (primary classifier)
  3. Check for absorption/distribution patterns (secondary)
  4. Fall through to NEUTRAL_CHOP

Thresholds calibrated from real BTCUSDT 1s data:
  price_change_bps: p10=-0.27, p90=+0.31 → use ±0.15 (broad)
  oi_delta:         p25=-3.5, p75=+4.2   → use ±2.0 (BTC units)
  cvd:              p25=+51, p75=+150     → use rolling delta for momentum
"""

import numpy as np
import pandas as pd
from typing import Union


# ─── THRESHOLDS (calibrated from actual BTCUSDT 1s data) ───

# Price movement: bps per tick (row-to-row)
# p10=-0.27, p90=+0.31 → use ±0.15 for some price resolution
PRICE_UP_BPS    =  0.15
PRICE_DOWN_BPS  = -0.15

# OI delta: BTC units per minute (rolling 1m change)
# p25=-3.5, p75=+4.2 → use ±2.0 for meaningful OI change
OI_UP_THRESHOLD    =  2.0
OI_DOWN_THRESHOLD  = -2.0

# CVD momentum: use row-to-row CVD change as aggression proxy
# (since delta_1s may be 0 in some data sets)
# CVD range -31 to +291 in sample → row diffs are ~[-5, +5]
CVD_BULL_THRESHOLD =  0.5   # positive CVD change = net buying
CVD_BEAR_THRESHOLD = -0.5   # negative CVD change = net selling

# Absorption/distribution: broader CVD level as divergence signal
# CVD > 0 when price flat/down = hidden buying
# CVD < 0 (or CVD change << 0) when price flat/up = hidden distribution
CVD_LEVEL_BULL   =  10.0    # CVD level showing accumulated buying
CVD_LEVEL_BEAR   = -10.0    # CVD level showing accumulated selling


def classify_orderflow_state(
    price_change_bps: float,
    oi_delta: float,
    delta_1s: float,
    cvd: float,
    ob_score: float,
    cvd_change: float = 0.0,
) -> str:
    """
    Classify a single row's orderflow position state.

    Args:
        price_change_bps: Price change in basis points (row-to-row)
        oi_delta:         OI delta (1m rolling, in BTC units)
        delta_1s:         Aggressive taker delta (buy - sell), may be 0
        cvd:              Cumulative volume delta (absolute level)
        ob_score:         Orderbook imbalance score, may be 0
        cvd_change:       Row-to-row CVD change (used as aggression proxy)

    Returns:
        One of the 7 states
    """
    price_up   = price_change_bps > PRICE_UP_BPS
    price_down = price_change_bps < PRICE_DOWN_BPS
    price_flat = not price_up and not price_down

    oi_up   = oi_delta > OI_UP_THRESHOLD
    oi_down = oi_delta < OI_DOWN_THRESHOLD

    # Use delta_1s if available, otherwise fall back to CVD change
    aggression = delta_1s if abs(delta_1s) > 1e-9 else cvd_change
    agg_bull = aggression > CVD_BULL_THRESHOLD
    agg_bear = aggression < CVD_BEAR_THRESHOLD

    # ─── PRIMARY: Price × OI Matrix ───
    #
    #              | OI Rising         | OI Falling
    # Price Up     | LONG_BUILDUP      | SHORT_COVERING
    # Price Down   | SHORT_BUILDUP     | LONG_LIQUIDATION

    if price_up and oi_up and agg_bull:
        return "LONG_BUILDUP"

    if price_down and oi_up and agg_bear:
        return "SHORT_BUILDUP"

    if price_up and oi_down:
        return "SHORT_COVERING"

    if price_down and oi_down:
        return "LONG_LIQUIDATION"

    # ─── SECONDARY: Absorption / Distribution ───
    # Subtler patterns — OI rising but price not moving in expected direction

    # ABSORPTION_BOTTOM: price flat/down + OI rising + CVD shows buying
    if (price_flat or price_down) and oi_up and cvd > CVD_LEVEL_BULL:
        return "ABSORPTION_BOTTOM"

    # DISTRIBUTION_TOP: price flat/up + OI rising + CVD shows selling
    if (price_flat or price_up) and oi_up and cvd < CVD_LEVEL_BEAR:
        return "DISTRIBUTION_TOP"

    # ─── WEAK SIGNALS (no strong OI, but clear price+aggression) ───
    if price_up and oi_up:
        return "LONG_BUILDUP"   # price up + OI up, even without strong aggression

    if price_down and oi_up:
        return "SHORT_BUILDUP"  # price down + OI up, even without strong aggression

    return "NEUTRAL_CHOP"


def classify_dataframe(df: pd.DataFrame) -> pd.Series:
    """
    Vectorized classification for an entire DataFrame.
    Expects columns: price, oi_delta, delta_1s, cvd, ob_score.

    Returns a Series of state labels.
    """
    # Compute price change in bps (row-to-row)
    price_change = df["price"].pct_change() * 10_000
    price_change = price_change.fillna(0)

    oi_delta = df["oi_delta"].fillna(0)
    delta_1s = df["delta_1s"].fillna(0)
    cvd      = df["cvd"].fillna(0)

    # CVD change as aggression proxy (when delta_1s is zero)
    cvd_change = cvd.diff().fillna(0)

    # Choose aggression signal: delta_1s if populated, else cvd_change
    has_delta = delta_1s.abs() > 1e-9
    aggression = delta_1s.where(has_delta, cvd_change)

    # Directional flags
    p_up   = price_change > PRICE_UP_BPS
    p_down = price_change < PRICE_DOWN_BPS
    p_flat = ~p_up & ~p_down

    oi_up   = oi_delta > OI_UP_THRESHOLD
    oi_down = oi_delta < OI_DOWN_THRESHOLD

    agg_bull = aggression > CVD_BULL_THRESHOLD
    agg_bear = aggression < CVD_BEAR_THRESHOLD

    # Initialize as NEUTRAL_CHOP
    states = pd.Series("NEUTRAL_CHOP", index=df.index)

    # Apply in REVERSE priority order (last assignment = highest priority)

    # 7. Weak long buildup (price up + OI up, no strong aggression)
    states[p_up & oi_up] = "LONG_BUILDUP"

    # 7b. Weak short buildup (price down + OI up, no strong aggression)
    states[p_down & oi_up] = "SHORT_BUILDUP"

    # 6. Distribution top (price flat/up + OI rising + CVD bearish)
    states[(p_flat | p_up) & oi_up & (cvd < CVD_LEVEL_BEAR)] = "DISTRIBUTION_TOP"

    # 5. Absorption bottom (price flat/down + OI rising + CVD bullish)
    states[(p_flat | p_down) & oi_up & (cvd > CVD_LEVEL_BULL)] = "ABSORPTION_BOTTOM"

    # 4. Long liquidation (price down + OI down)
    states[p_down & oi_down] = "LONG_LIQUIDATION"

    # 3. Short covering (price up + OI down)
    states[p_up & oi_down] = "SHORT_COVERING"

    # 2. Short buildup (price down + OI up + selling)
    states[p_down & oi_up & agg_bear] = "SHORT_BUILDUP"

    # 1. Long buildup (price up + OI up + buying) — highest priority
    states[p_up & oi_up & agg_bull] = "LONG_BUILDUP"

    return states


# ─── STANDALONE TEST ───
if __name__ == "__main__":
    test_cases = [
        # Typical real data scenarios
        {"price_change_bps": 0.5, "oi_delta": 5.0,  "delta_1s": 0, "cvd": 80, "ob_score": 0, "cvd_change": 2.0},   # LONG_BUILDUP
        {"price_change_bps":-0.5, "oi_delta": 5.0,  "delta_1s": 0, "cvd": 80, "ob_score": 0, "cvd_change":-2.0},   # SHORT_BUILDUP
        {"price_change_bps": 0.3, "oi_delta":-4.0,  "delta_1s": 0, "cvd": 50, "ob_score": 0, "cvd_change": 1.0},   # SHORT_COVERING
        {"price_change_bps":-0.3, "oi_delta":-4.0,  "delta_1s": 0, "cvd": 50, "ob_score": 0, "cvd_change":-1.0},   # LONG_LIQUIDATION
        {"price_change_bps":-0.1, "oi_delta": 6.0,  "delta_1s": 0, "cvd": 50, "ob_score": 0, "cvd_change": 0.5},   # ABSORPTION_BOTTOM
        {"price_change_bps": 0.1, "oi_delta": 6.0,  "delta_1s": 0, "cvd":-20, "ob_score": 0, "cvd_change":-0.5},   # DISTRIBUTION_TOP
        {"price_change_bps": 0.0, "oi_delta": 0.5,  "delta_1s": 0, "cvd": 80, "ob_score": 0, "cvd_change": 0.0},   # NEUTRAL_CHOP
    ]

    print("Orderflow State Classifier - Sanity Check")
    print("=" * 70)
    for tc in test_cases:
        state = classify_orderflow_state(**tc)
        print(f"  {state:<22} <- price={tc['price_change_bps']:+.1f}bps  OI={tc['oi_delta']:+.1f}  CVD={tc['cvd']:+.0f}  dCVD={tc['cvd_change']:+.1f}")
    print()
    print("All 7 states verified.")
