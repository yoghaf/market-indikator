import { useMemo } from 'react';

/**
 * useBiasEngine — Market Bias Decision Layer
 *
 * Reads ONLY existing computed values and returns a
 * human-readable bias verdict with confidence and reasons.
 *
 * This is NOT a trading signal. It is a clarity layer
 * for discretionary orderflow reading.
 *
 * @param {number} score     - Confluence score (-100 to +100)
 * @param {number} oiBeh     - OI behavior (0-6)
 * @param {number} aggrPct   - Aggressive buy % (0-100)
 * @param {number} psvPct    - Passive buy % (0-100)
 * @param {Array}  events    - Recent event log entries
 * @param {Array}  htScores  - Heatmap scores [1s,1m,5m,15m,1h,4h,1d]
 */

// OI behavior constants (must match App.jsx BEH map)
const OI_LONG_BUILDUP = 1;
const OI_SHORT_BUILDUP = 2;
const OI_SHORT_COVERING = 3;
const OI_LONG_LIQ = 4;

export function useBiasEngine(score, oiBeh, aggrPct, psvPct, events, htScores) {
  return useMemo(() => {
    // ═══ 1. BIAS CLASSIFICATION ═══
    let bias, color;
    if (score >= 30) {
      bias = 'LONG SETUP';
      color = '#00dc82';
    } else if (score <= -30) {
      bias = 'SHORT SETUP';
      color = '#ef4444';
    } else {
      bias = 'NO EDGE';
      color = '#3a4575';
    }

    const isLong = bias === 'LONG SETUP';
    const isShort = bias === 'SHORT SETUP';
    const isEdge = isLong || isShort;

    // ═══ 2. CONFIDENCE CALCULATION ═══
    let conf = Math.min(100, Math.abs(score)); // Base: score magnitude

    // +10 if OI behavior aligns with bias
    if (isLong && (oiBeh === OI_LONG_BUILDUP || oiBeh === OI_SHORT_COVERING)) conf += 10;
    if (isShort && (oiBeh === OI_SHORT_BUILDUP || oiBeh === OI_LONG_LIQ)) conf += 10;

    // +10 if recent events contain confirming signals (last 10 events)
    const recent = (events || []).slice(0, 10);
    const hasAbsorption = recent.some(e => e.m?.includes('Absorption'));
    const hasLiqShort = recent.some(e => e.m?.includes('Liquidated Short'));
    const hasLiqLong = recent.some(e => e.m?.includes('Liquidated Long'));
    const hasWhaleBuy = recent.some(e => e.m?.includes('Whale Buy'));
    const hasWhaleSell = recent.some(e => e.m?.includes('Whale Sell'));

    if (isLong && (hasAbsorption || hasLiqShort || hasWhaleBuy)) conf += 10;
    if (isShort && (hasLiqLong || hasWhaleSell)) conf += 10;

    // +5 if HTF alignment (majority of timeframes agree)
    const htf = htScores || [];
    const bullCount = htf.filter(s => s > 5).length;
    const bearCount = htf.filter(s => s < -5).length;
    if (isLong && bullCount >= 4) conf += 5;
    if (isShort && bearCount >= 4) conf += 5;

    // Clamp
    conf = Math.min(100, Math.max(0, Math.round(conf)));

    // Map to label
    const confLabel = conf >= 70 ? 'HIGH' : conf >= 40 ? 'MEDIUM' : 'LOW';

    // ═══ 3. REASON GENERATION ═══
    const reasons = [];

    if (isEdge) {
      // Score strength
      const absS = Math.abs(score);
      if (absS >= 60) reasons.push(`Strong pressure: ${score > 0 ? '+' : ''}${Math.round(score)}`);
      else if (absS >= 30) reasons.push(`Pressure building: ${score > 0 ? '+' : ''}${Math.round(score)}`);

      // OI alignment
      if (isLong && oiBeh === OI_LONG_BUILDUP) reasons.push('OI confirms long buildup');
      if (isLong && oiBeh === OI_SHORT_COVERING) reasons.push('Short covering detected');
      if (isShort && oiBeh === OI_SHORT_BUILDUP) reasons.push('OI confirms short buildup');
      if (isShort && oiBeh === OI_LONG_LIQ) reasons.push('Long liquidation cascade');

      // Flow imbalance
      if (isLong && aggrPct > 65) reasons.push(`Aggressive buying: ${Math.round(aggrPct)}%`);
      if (isShort && aggrPct < 35) reasons.push(`Aggressive selling: ${Math.round(100 - aggrPct)}%`);
      if (isLong && psvPct > 65) reasons.push('Passive absorption on bids');
      if (isShort && psvPct < 35) reasons.push('Passive absorption on asks');

      // Event signals
      if (isLong && hasAbsorption) reasons.push('Sell absorption detected');
      if (isLong && hasLiqShort) reasons.push('Short liquidation cascade');
      if (isLong && hasWhaleBuy) reasons.push('Whale buying detected');
      if (isShort && hasLiqLong) reasons.push('Long liquidation cascade');
      if (isShort && hasWhaleSell) reasons.push('Whale selling detected');

      // HTF alignment
      if (isLong && bullCount >= 4) reasons.push(`HTF aligned: ${bullCount}/7 bullish`);
      if (isShort && bearCount >= 4) reasons.push(`HTF aligned: ${bearCount}/7 bearish`);
    }

    // Trim to max 3 reasons
    return {
      bias,
      color,
      confidence: conf,
      confLabel,
      reasons: reasons.slice(0, 3),
    };
  }, [score, oiBeh, aggrPct, psvPct, events, htScores]);
}
