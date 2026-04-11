# Optimization TODO List - go-regexp-re

This document tracks the remaining optimization tasks based on the project's high-performance DFA strategy.

## 1. SIMD Warp Execution Engine Integration
- [x] **Task**: Integrate `warpPoints` metadata into `execLoop`.
- [x] **Implementation**:
    - In `execLoop`, check if the current state has a `warpPoint` (a byte that leads to progress while others stay in the same state).
    - Use `bytes.IndexByte` to SIMD-skip the input until that byte is found.
- [x] **Impact**: Significant throughput boost for patterns with long literal components.

## 2. SCC Analysis for Early Exit (Always True)
- [x] **Task**: Implement `findSCCs` in `internal/ir/dfa.go` using Tarjan's or Kosaraju's algorithm.
- [x] **Implementation**:
    - Identify Strongly Connected Components (SCCs).
    - Mark states as `isAlwaysTrue` if they belong to an SCC that is guaranteed to reach an accepting state regardless of further input.
    - Update `execLoop` to exit immediately when an `isAlwaysTrue` state is reached.
- [x] **Impact**: Faster matching for patterns like `.*` where trailing content doesn't affect the match outcome.

## 3. Unified Transition Table (Single Table, Multiple Entries)
- [ ] **Task**: Consolidate the separate Search and Match DFAs into a single physical transition table.
- [ ] **Implementation**:
    - Refactor `build` to correctly interleave search closures (for `SearchStartState`) and match paths (for `MatchStartState`) without priority conflicts.
    - Ensure submatch extraction (Phase 2) correctly identifies the entry point used.
- [ ] **Impact**: 50% reduction in transition table memory footprint and improved L3 cache efficiency.

## 4. Advanced AST Common Factorization
- [x] **Task**: Enhance `syntax.Optimize` to perform aggressive factorization of alternations.
- [x] **Example**: Convert `(apple|apply)` to `appl[ey]` (gosyntax normalized) at the AST level.
- [x] **Impact**: Reduces DFA state count and speeds up compilation for large alternation sets.

## 5. Branch & BCE Verification (Compiler Guardrails)
- [ ] **Task**: Use `go tool compile -S` to verify the quality of monomorphized loops.
- [ ] **Goal**:
    - Ensure `execLoop` instances are free of unnecessary `runtime.panicIndex` (Bounds Check Elimination).
    - Confirm that trait-based conditions (e.g., `trait.HasAnchors()`) are completely eliminated in the assembly.
- [ ] **Impact**: Minimizes Instruction Per Byte (IPB) by ensuring optimal machine code generation.
