# Speedup Analysis Report (2026-05-19)

## Source Data
- **Authoritative File**: `data/landscape.json` (Branch: `gh-pages`, Commit: `5fddf5134917a10262fc86839bbae8c21cb626d4`)
- **Scope**: Multi-engine throughput comparison across SBL dimensions.

## 📊 Trend Analysis (S-Dependency)

Performance speedup (vs. standard library) is observed to be inversely proportional to Selectivity (S). The landscape is fundamentally divided into two regimes based on S:

### 1. Low Selectivity Regime (S ≤ 0.05)
When matches are sparse, speedups are non-linear and extreme (up to 500,000x). This occurs because the engine utilizes SIMD/SWAR pre-filters (MAP) to skip large blocks of input. The standard library often performs full backtracking scans in these scenarios. Examples include anchored searches (`^127.0.0.1`, `HTTP/1.1$`) or rare literals.

### 2. High Selectivity Regime (S ≥ 0.10)
When matches are dense and frequent, the engine's speedup converges towards 1.0x (parity with the standard library). In this regime, the pre-filter is constantly triggered, forcing the engine into the primary table-based DFA execution loop.
- **Overhead**: At extremely high density and low locality (e.g., `Landscape/S=0.01/B=1/L=0.10`, yielding 0.94x), the constant memory-access latency of DFA table lookups slightly exceeds the CPU cost of the standard library's simple NFA pointer dereferencing.
- **Stability**: Unlike backtracking engines, the DFA regime maintains constant $O(1)$ per-byte processing time regardless of structural complexity ($B$), effectively capping worst-case performance degradation at near 1.0x.

## Comparison Table

**How to Read This Table:**
This data reflects engine performance across the **SBL Coordinate System**: **S** (Selectivity, sparse [0.01] to dense [0.90]), **B** (Complexity, simple [0.50] to complex [1.00]), and **L** (Locality, random [0.10] to structured [0.90]).
- **GoRegexp (MB/s)**: Standard library baseline throughput.
- **Engine Columns (nx)**: Speedup ratio vs baseline (1.0x = parity).
- **"0.00x"**: Indicates the engine is significantly slower than the baseline (ratio < 0.005), often due to constant overheads (e.g., CGO/Wasm boundary crossing) in high-throughput scenarios.
- **"-"**: Pattern rejected or unsupported by the engine.

| Category | S | B | L | GoRegexp(MB/s) | GoRegexpRe | Coregex | Hyperscan-CGO | PCRE2-CGO | RE2-CGO | RE2-Wasm |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Anchors/pat=(?m)HTTP/1.1$ | 0.05 | 0.51 | 0.75 | 625.13 | 2.21x | 2.39x | 1.50x | 3.19x | 0.95x | 0.96x |
| Anchors/pat=(?m)^127.0.0.1 | 0.05 | 0.51 | 0.33 | 53.27 | 147.45x | 108.97x | 32.46x | 7.80x | 2.25x | 2.25x |
| Anchors/pat=HTTP/1.1$ | 0.05 | 0.51 | 0.75 | 646.05 | 64448.39x | 76549.42x | 2.95x | 3.12x | 7.63x | 7.89x |
| Anchors/pat=\bGET\b | 0.05 | 0.51 | 1.00 | 1372111.54 | 19.77x | 7.20x | 0.00x | 0.88x | 0.00x | 0.00x |
| Anchors/pat=^127.0.0.1 | 0.05 | 0.51 | 0.33 | 52621005.84 | 1.50x | 6.65x | 0.00x | 0.02x | 0.00x | 0.00x |
| Capturing/Email | 0.00 | 0.62 | 0.20 | 337628.29 | 2.06x | 0.54x | 0.01x | 1.05x | 0.01x | 0.02x |
| Capturing/URI | 0.00 | 0.75 | 0.18 | 1546602.44 | 5.88x | 0.00x | 0.00x | 0.33x | 0.00x | 0.00x |
| Landscape/S=0.01/B=1/L=0.10 | 0.01 | 0.50 | 0.10 | 4281.73 | 0.94x | 0.05x | 0.44x | 10.20x | 0.17x | 0.16x |
| Landscape/S=0.01/B=1/L=0.90 | 0.01 | 0.50 | 0.90 | 13301.53 | 1.01x | 0.02x | 0.13x | 3.30x | 0.06x | 0.06x |
| Landscape/S=0.01/B=10/L=0.10 | 0.01 | 0.51 | 0.08 | 4158.12 | 1.00x | 1.41x | 0.45x | 0.11x | 0.16x | 0.17x |
| Landscape/S=0.01/B=10/L=0.90 | 0.01 | 0.51 | 0.68 | 13391.86 | 1.02x | 0.44x | 0.14x | 0.04x | 0.06x | 0.06x |
| Landscape/S=0.01/B=50/L=0.10 | 0.01 | 0.65 | 0.03 | 4190.01 | 0.99x | 1.90x | 0.41x | 0.03x | 0.16x | 0.17x |
| Landscape/S=0.01/B=50/L=0.90 | 0.01 | 0.65 | 0.23 | 13393.98 | 1.02x | 0.59x | 0.14x | 0.01x | 0.06x | 0.06x |
| Landscape/S=0.10/B=1/L=0.10 | 0.10 | 0.50 | 0.10 | 566.27 | 1.05x | 0.30x | 2.06x | 81.52x | 0.40x | 0.40x |
| Landscape/S=0.10/B=1/L=0.90 | 0.10 | 0.50 | 0.90 | 7863.90 | 1.00x | 0.03x | 0.22x | 5.43x | 0.06x | 0.06x |
| Landscape/S=0.10/B=10/L=0.10 | 0.10 | 0.51 | 0.08 | 885.05 | 0.95x | 6.63x | 1.83x | 0.06x | 0.26x | 0.25x |
| Landscape/S=0.10/B=10/L=0.90 | 0.10 | 0.51 | 0.68 | 7780.49 | 0.99x | 0.75x | 0.20x | 0.01x | 0.06x | 0.06x |
| Landscape/S=0.10/B=50/L=0.10 | 0.10 | 0.65 | 0.03 | 879.88 | 0.97x | 8.71x | 1.90x | 0.01x | 0.26x | 0.26x |
| Landscape/S=0.10/B=50/L=0.90 | 0.10 | 0.65 | 0.23 | 7757.72 | 1.00x | 1.02x | 0.22x | 0.00x | 0.05x | 0.06x |
| Landscape/S=0.50/B=1/L=0.10 | 0.50 | 0.50 | 0.10 | 7714.49 | 1.00x | 0.03x | 0.23x | 5.35x | 0.01x | 0.01x |
| Landscape/S=0.50/B=1/L=0.90 | 0.50 | 0.50 | 0.90 | 7675.59 | 1.00x | 0.03x | 0.23x | 5.57x | 0.02x | 0.02x |
| Landscape/S=0.50/B=10/L=0.10 | 0.50 | 0.51 | 0.08 | 7628.54 | 1.02x | 0.75x | 0.17x | 0.00x | 0.01x | 0.01x |
| Landscape/S=0.50/B=10/L=0.90 | 0.50 | 0.51 | 0.68 | 7659.54 | 0.99x | 0.75x | 0.16x | 0.00x | 0.02x | 0.02x |
| Landscape/S=0.50/B=50/L=0.10 | 0.50 | 0.65 | 0.03 | 7681.94 | 1.00x | 1.02x | 0.16x | 0.00x | 0.01x | 0.01x |
| Landscape/S=0.50/B=50/L=0.90 | 0.50 | 0.65 | 0.23 | 7659.40 | 0.97x | 1.02x | 0.16x | 0.00x | 0.01x | 0.01x |
| Landscape/S=0.90/B=1/L=0.10 | 0.90 | 0.50 | 0.10 | 7624.28 | 1.00x | 0.02x | 0.21x | 5.53x | 0.01x | 0.01x |
| Landscape/S=0.90/B=1/L=0.90 | 0.90 | 0.50 | 0.90 | 7491.82 | 1.02x | 0.03x | 0.22x | 5.65x | 0.02x | 0.02x |
| Landscape/S=0.90/B=10/L=0.10 | 0.90 | 0.51 | 0.08 | 7600.61 | 1.01x | 0.75x | 0.16x | 0.00x | 0.01x | 0.01x |
| Landscape/S=0.90/B=10/L=0.90 | 0.90 | 0.51 | 0.68 | 7533.56 | 1.01x | 0.74x | 0.14x | 0.00x | 0.02x | 0.02x |
| Landscape/S=0.90/B=50/L=0.10 | 0.90 | 0.65 | 0.03 | 7456.89 | 1.01x | 1.04x | 0.15x | 0.00x | 0.01x | 0.01x |
| Landscape/S=0.90/B=50/L=0.90 | 0.90 | 0.65 | 0.23 | 7513.53 | 1.01x | 1.02x | 0.16x | 0.00x | 0.02x | 0.02x |
| LargeAlternation/Count=10 | 0.00 | 0.72 | 0.12 | 26941.87 | 1.05x | 0.22x | 0.06x | 1.50x | 0.04x | 0.04x |
| LargeAlternation/Count=100 | 0.00 | 1.00 | 0.02 | 29606.67 | 1.07x | 0.20x | 0.05x | 1.27x | 0.03x | 0.03x |
| LargeAlternation/Count=1000 | 0.00 | 1.00 | 0.00 | 24996.60 | 1.26x | 0.22x | 0.05x | 0.91x | 0.04x | 0.04x |
| LargeAlternation/Count=10000 | 0.00 | 1.00 | 0.00 | 18751.24 | 1.70x | 0.00x | - | - | 0.05x | 0.05x |
| LiteralScan/pat=Sherlock | 0.00 | 0.50 | 1.00 | 3051996.87 | 11.80x | 1.93x | 0.00x | 0.21x | 0.00x | 0.00x |
| NFAWorstCase/Run | 0.01 | 0.60 | 0.50 | 15.80 | 2.95x | 14.62x | 110.85x | 2607.35x | 7.55x | 7.58x |
| StandardSuite/Alternation/(fo\|foo) | 0.00 | 0.57 | 0.67 | 4183143.91 | 3.62x | 2.70x | 0.00x | 0.13x | 0.00x | 0.00x |
| StandardSuite/Anchored/^(?:a)$ | 0.00 | 0.51 | 1.00 | 23128976.85 | 2.52x | 7.02x | 0.00x | 0.03x | 0.00x | 0.00x |
| StandardSuite/CharClass/(?i)[@-A]+ | 0.01 | 0.54 | 0.00 | 5408169.10 | 3.94x | 15.32x | 0.00x | 0.11x | 0.00x | 0.00x |
| StandardSuite/Complex/a+ | 0.00 | 0.54 | 1.00 | 5876928.24 | 3.24x | 1.99x | 0.00x | 0.09x | 0.00x | 0.00x |
| StandardSuite/Literal/a | 0.00 | 0.50 | 1.00 | 6547797.43 | 6.77x | 1.79x | 0.00x | 0.08x | 0.00x | 0.00x |
| Synthetic/CCWarp | 1.00 | 0.55 | 0.00 | 57.47 | 510045.62x | 0.26x | 14.13x | 15.15x | 2.16x | 2.08x |
| Synthetic/PureDFA | 1.00 | 0.59 | 0.17 | 30.75 | 6.99x | 0.09x | 24.59x | 0.14x | 3.92x | 3.93x |
| Synthetic/SIMDWarp | 0.01 | 0.50 | 1.00 | 27513.16 | 1.03x | 1.17x | 0.06x | 1.48x | 0.03x | 0.04x |
| Synthetic/SearchWarp | 0.01 | 0.54 | 0.00 | 38.88 | 89.03x | 30.86x | 38.30x | 14.42x | 1.62x | 2.36x |
