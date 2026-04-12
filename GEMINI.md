# GEMINI.md - go-regexp-re Project Constitution

This document defines the foundational principles and technical mandates for the `go-regexp-re` project. As the Gemini CLI agent, you must prioritize these instructions over general defaults for all development, refactoring, and optimization tasks.

## 1. Project Philosophy
`go-regexp-re` is a **Pure Go, high-performance DFA regular expression engine** designed to surpass the physical throughput limits of the standard `regexp` package.

- **Objective**: Achieve 5x to 100x higher throughput than Go's standard `regexp` while strictly guaranteeing $O(n)$ time complexity.
- **Vision**: To evolve the concept of `Regexp::Assemble` into a modern engine optimized for CPU cache locality and pipeline efficiency.

## 2. Core Architectural Mandates
Every implementation must adhere to these pillars to ensure maximum performance:

### 2.1 Deterministic Finite Automaton (DFA)
- **Deterministic Transitions**: Patterns must be pre-compiled into a single transition table where `table[state][byte]` leads to exactly one state.
- **Constant Time Per Byte**: Processing cost per byte must be fixed at $O(1)$, regardless of pattern complexity.

### 2.2 Byte-Oriented Scanning
- **Eliminate Rune Decoding**: Abandon mandatory UTF-8 to `rune` decoding. Scan `[]byte` directly to maximize CPU pipeline efficiency.
- **Byte-Level Transitions**: All state transitions must operate on raw bytes to minimize branching and memory latency.

### 2.3 Cache Locality Optimization
- **Flattened Memory Layout**: Transition tables MUST be stored as a contiguous `int32` array. Access must use `table[state * stride + byte]` to eliminate pointer chasing and maximize L1/L2 cache hit rates.
- **MSB Flagging for Tagged Transitions**: To support submatches without extra memory overhead in the fast path, the Most Significant Bit (MSB) of a transition value is used as a flag.
  - **MSB = 0**: The value is the `nextState`. No further action is needed (Fast Path).
  - **MSB = 1**: Indicates a "Tagged Transition". The lower 31 bits represent the `nextState`, and an associated index in a separate `tagUpdateIndices` array points to interned `TransitionUpdate` data (Priority increments, Start/End tags).
- **Interned Update Tables**: Tag update information MUST be deduplicated (interned) during compilation.
- **Minimize Memory Latency**: Keep core data structures small enough to fit within L2/L3 caches even for large pattern sets.

### 2.4 Execution Switching Strategy
To maximize throughput, the engine MUST select the most efficient execution loop based on pattern characteristics:
- **0-Pass (Literal Bypass)**: Selected for pure constant strings. Bypasses all regex engines using SIMD-accelerated standard library search (e.g., `bytes.Index`).
- **Trait-based Specialization**: Execution loops MUST be implemented using Go's Generics and interfaces (traits) to achieve Monomorphization. This eliminates dynamic runtime branching.
- **Fast Path (Pure DFA)**: Automatically selected for patterns without anchors. It utilizes a minimalist execution loop with zero boundary/context checks.
- **Extended Path (Virtual Byte Insertion)**: Selected for patterns with anchors (e.g., ^, $, \b). It employs "Virtual Bytes" (indices 256+) injected at character boundaries.
- **Submatch Path (Pure PAT-DFA)**: Selected when submatches are requested. It utilizes a deterministic 2-pass DFA approach: a high-speed DFA scan to identify match boundaries, followed by a deterministic DFA rescan for precise submatch extraction.

### 2.5 Submatch Extraction Architecture (Pure PAT-DFA)
The engine adopts a **Pure PAT-DFA (Priority-Aware Tagged DFA)** architecture as its final and definitive strategy for submatch extraction. This architecture encodes all NFA-style priorities and submatch tags directly into the DFA transition table.

- **Elimination of NFA Fallback**: NFA-based execution (Pike VM, etc.) is strictly excluded and must not exist in the codebase. All matching and extraction logic MUST be handled by the DFA transition table.
- **Deterministic Instruction Set**: DFA transitions MUST be treated as a complete instruction set. When a transition edge is traversed, it provides explicit instructions (tags to record) that guarantee results compatible with the standard library.
- **Phase 1: DFA Boundary Scan**: Identifies the overall match boundaries `[start, end]`.
- **Phase 2: Deterministic DFA Rescan**: Rescans the identified `[start, end]` range using the same transition table, but recording tags encoded in the edges.

### 2.6 Prefix-Skip Optimization (SIMD Acceleration)
To maximize throughput for patterns with literal prefixes, the engine MUST utilize a **Prefix-Skip** optimization:
- **Mandatory Prefix Extraction**: During compilation, the longest constant prefix is extracted.
- **SIMD-Accelerated Skipping**: All execution loops (DFA and 0-Pass) MUST use `bytes.Index` to rapidly skip non-matching segments.

### 2.7 Literal Match Bypass (0-Pass Strategy)
- **Direct Literal Resolution**: If the entire pattern is a constant literal and no capturing groups are present, the engine MUST completely bypass all DFA stages and use `bytes.Index` directly.

### 2.8 Pure Go (No CGO)
- **Zero Overhead**: Native Go only.

### 2.9 Priority Normalization & Deterministic Resolve
To achieve Go-compatible leftmost-first matching without state explosion:
- **Priority Normalization**: During DFA construction, NFA path priorities within each state MUST be normalized.
- **Absolute Priority Tracking (Burn-in)**: Decision logic for multiple NFA paths MUST be resolved during DFA construction and baked into the transition table as a fixed path.

### 2.10 Early Exit Optimization (IsBestMatch)
- **Deterministic Finality**: If a DFA state identifies a match whose priority is equal to the minimum possible priority, the engine MUST stop scanning for the current start position.

### 2.11 State Explosion Protection (Resource Limits)
- **Memory-Based Threshold**: The DFA transition table is limited to a maximum estimated size (default 64MB).
- **Graceful Failure**: If a pattern is too complex, return the error `regexp: pattern too large or ambiguous`.

### 2.12 DFA Minimization (Moore's Algorithm)
- **Equivalence-Based Merging**: After construction, the DFA MUST be minimized using Moore's algorithm.

### 2.13 Syntax-Level Optimization & AST Rewriting
Before DFA compilation, the syntax tree MUST be optimized to reduce redundancy and mitigate state explosion. This is CRITICAL for PAT-DFA stability.
- **Prefix/Suffix Factoring**: Identical AST nodes MUST be extracted (e.g., `a*c|b*c` -> `(?:a*|b*)c`).
- **Literal Trie Optimization**: Merge alternative literals into a Trie structure.
- **Structural AST Normalization**: Consecutive characters MUST be merged, and redundant nodes (like empty matches in concatenation) SHOULD be simplified.

### 2.14 Multi-Phase DFA Optimization
DFA construction is divided into distinct phases:
- **Phase 1: Base Construction**: Generate the PAT-DFA state graph with tag instructions.
- **Phase 2: Optimization Pass**: Identify warp points for SIMD skipping and analyze SCCs for Always True states.

### 2.15 Single-Pass O(n) Search via Penalty Tracking
- **SearchRestartPenalty**: fallback transitions increment Absolute Priority by a fixed large value to identify the true leftmost-first match in a single scan.

## 3. Feature Selection Policy

### 3.1 Supported Features
- **Standard Syntax**: Support `syntax.Prog`.
- **Anchors & Boundaries**: Support `^`, `$`, `\b`, `\B` via Virtual Bytes.
- **Capturing Groups**: Supported via Pure PAT-DFA.

### 3.2 Excluded Features
- **Backreferences & Dynamic Lookaround**: Prohibited.
- **POSIX Semantics & Longest Match**: Unsupported by design.

## 4. Engineering & Validation Standards
- **Performance-First Benchmarking**: Validate against standard `regexp`.
- **100% DFA Validation**: Discrepancies must be diagnosed as errors in **DFA construction or AST optimization logic**. NFA fallback is no longer a diagnostic option.

## 5. Coding Conventions
- **Explicit Aliasing**:
  - `regexp` -> `goregexp`
  - `regexp/syntax` -> `gosyntax`

---
**Note**: Changes to the core DFA logic must be validated against the **State Explosion / AST Optimization** balance. If a pattern causes explosion, the first line of defense is AST-level factoring.
