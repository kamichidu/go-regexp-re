# TODO: Achieving 100% Go Compatibility (Deterministic 2-pass TDFA)

The goal is to fix submatch extraction and anchor matching failures while strictly prohibiting runtime NFA simulation.

## 1. Execution Loop Synchronization (Critical)
- [ ] **Unified Anchor Resolution**: Ensure `matchExecLoop` and `submatchExecLoop` use the exact same logic for resolving chained anchors at each position `i`.
- [ ] **Finalized State Recording**: Fix `mc.history[i]` to always record the state *after* all anchors at position `i` have been resolved but *before* the byte at `i` is consumed.
- [ ] **Atomic Priority Tracking**: Verify that `currentPriority` updates in Phase 1 (via `update.BasePriority`) are mathematically identical to the `NextPriority` transitions followed in Phase 2's `burnedRecap`.

## 2. Anchor Match Fixes
- [ ] **End-of-String Matching**: Fix `TestHTTP11Anchor` and `TestRegexp_Multiline`. The failure of `(?m)HTTP/1.1$` indicates that the `$` anchor at `i = numBytes` is not triggering a match accept or is being recorded incorrectly.
- [ ] **Word Boundary Precision**: Investigate why `\babc\b` fails. Ensure `CalculateContext` and `anchorTransitions` are perfectly aligned.

## 3. Submatch Precision & Burn-in Logic
- [x] **BP-DFA 2-System Recap**: Implement 2-system Forward BP-Recap for simple patterns (Step 1, 2, 3).
- [ ] **Strict Multiplexing (Step 4)**: Fix the 1-byte offset in Table-DFA by including cumulative priority in `dfaStateKey` to force state separation for greedy/non-greedy ambiguities.
- [ ] **Multiplexing Verification**: Ensure `epsilonClosureWithPathTags` in `dfa.go` correctly separates paths with identical NFA state sets but different tag histories into distinct DFA states.

## 4. Code Cleanup & Safety
- [ ] **Deduplicate Loops**: Remove redundant match/submatch loop variations in `regexp.go` introduced during refactoring.
- [ ] **Static Compatibility Check**: Enhance `checkCompatibility` to detect and reject complex nested captures that would cause state explosion if fully multiplexed.
- [ ] **State Explosion Protection**: Ensure the engine gracefully falls back or errors out if the number of multiplexed states exceeds `MaxDFAMemory`.

## 5. Documentation
- [ ] Update `docs/algorithm/2-pass-dfa-hybrid.adoc` to include the finalized synchronization logic.
