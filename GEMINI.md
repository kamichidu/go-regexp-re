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
- **Flattened Memory Layout**: Transition tables MUST be stored as a single, contiguous `int32` array. Access must use `table[state * stride + byte]` to eliminate pointer chasing and maximize L1/L2 cache hit rates.
- **Unified Transition Table**: To minimize memory footprint, states for Search (with restart closure) and Match (without restart) MUST be unified into a single physical transition table. This improves L3 cache hit rates and halves the memory requirements for compiled regular expressions.
- **Minimize Memory Latency**: Keep core data structures small enough to fit within L2/L3 caches even for large pattern sets.

### 2.4 Execution Switching Strategy
To maximize throughput, the engine MUST select the most efficient execution loop based on pattern characteristics:
- **0-Pass (Literal Bypass)**: Selected for pure constant strings. Bypasses all regex engines using SIMD-accelerated standard library search (e.g., `bytes.Index`).
- **Trait-based Specialization**: Execution loops MUST be implemented using Go's Generics and interfaces (traits) to achieve Monomorphization. This eliminates dynamic runtime branching for anchors and context checks within hot loops, minimizing Instruction Per Byte (IPB).
- **Fast Path (Pure DFA)**: Automatically selected for patterns without anchors. It utilizes a minimalist execution loop with zero boundary/context checks to approach raw memory bandwidth speeds.
- **Extended Path (Virtual Byte Insertion)**: Selected for patterns with anchors (e.g., `^`, `$`, `\b`). It employs "Virtual Bytes" (indices 256+) injected at character boundaries to process empty-width assertions within the DFA's $O(n)$ framework.
- **Submatch Path (Path-Guided 2-Pass DFA)**: Selected when submatches are requested. It utilizes a high-speed DFA scan to identify match boundaries, followed by a guided DFA second pass for efficient submatch extraction.

### 2.5 Submatch Extraction Architecture (Path-Guided 2nd Pass DFA)
The engine adopts a **Path-Guided 2nd Pass DFA** architecture as its definitive strategy for submatch extraction. This architecture prioritizes DFA throughput even during submatch resolution, ensuring $O(n)$ predictability and minimal CPU overhead.

- **Intentional Exclusion of TDFA**: Full Tagged DFA (TDFA) with 1-pass resolution is explicitly excluded to avoid catastrophic state explosion.
- **Phase 1: Boundary Discovery & Winner Identification**:
  - A specialized DFA scan identifies match boundaries `[start, end]`.
  - The engine tracks **Absolute Priority** (cumulative transition increments + state match priority) to identify the "winning" NFA path according to leftmost-first semantics.
- **Phase 2: Path-Guided DFA Rescan (Primary)**:
  - If the winning path corresponds to the highest priority (Priority 0), the engine rescans the `[start, end]` range using the same DFA table.
  - Submatch tags are collected directly from DFA transition edges (`PreTags` and `PostTags`), eliminating NFA thread management overhead.
- **Phase 2: Targeted NFA Rescan (Fallback)**:
  - If a lower priority path won (common in complex greedy loops, `targetPriority > 0`), the engine falls back to an optimized NFA rescan ONLY within the identified `[start, end]` bounds.
  - **Eliminate Rune Decoding**: The NFA fallback MUST operate directly on raw bytes to maintain performance consistency.

### 2.6 Prefix-Skip Optimization (SIMD Acceleration)
To maximize throughput for patterns with literal prefixes, the engine MUST utilize a **Prefix-Skip** optimization:
- **Mandatory Prefix Extraction**: During compilation, the longest constant prefix is extracted.
- **SIMD-Accelerated Skipping**: All execution loops (DFA and 0-Pass) MUST use `bytes.Index` to rapidly skip non-matching segments.
- **Pre-calculated Prefix State**: The DFA state reached after matching the prefix (`prefixState`) is pre-calculated to allow immediate resumption of DFA execution.

### 2.7 Literal Match Bypass (0-Pass Strategy)
- **Direct Literal Resolution**: If the entire pattern is a constant literal and no capturing groups are present, the engine MUST completely bypass both DFA and NFA stages and use `bytes.Index` directly.

### 2.8 Pure Go (No CGO)
- **Zero Overhead**: CGO is strictly prohibited to avoid context-switching overhead and maintain Go's native portability and build simplicity.

### 2.9 Priority Normalization & Static Priority Resolution
To achieve Go-compatible leftmost-first matching without state explosion:
- **Priority Normalization**: During DFA construction, NFA path priorities within each state MUST be normalized (subtracting the minimum priority) to prevent infinite state generation in patterns like `a*?`.
- **Absolute Priority Tracking**: The values subtracted during normalization MUST be stored as `transPriorityIncrement` on transitions. The execution engine MUST accumulate these increments to reconstruct the **Absolute Priority** of any match, ensuring the correct leftmost-first alternative is selected across different match lengths.
- **Static Priority Resolution (Pruning)**: During `epsilonClosure` (DFA state construction), any NFA path whose priority is numerically greater (lower priority) than a confirmed match at the same position MUST be pruned. This "shadowed path" removal is critical to preventing state explosion in complex alternations.

### 2.10 Early Exit Optimization (IsBestMatch)
- **Deterministic Finality**: If a DFA state contains a match whose priority is equal to the minimum priority of all active NFA paths in that state (`IsBestMatch == true`), the engine MUST stop scanning for the current start position. This guarantees that no better priority match can be found by continuing, providing a critical performance boost for non-greedy patterns and early-exit scenarios.

### 2.11 State Explosion Protection (Resource Limits)
To ensure system stability and prevent Out-Of-Memory (OOM) conditions during compilation, the engine enforces a strict resource limit on DFA construction.
- **Memory-Based Threshold**: The DFA transition table is limited to a maximum estimated size (default 64MB, approx. 32,000 states).
- **Graceful Failure**: If a pattern is too complex or highly ambiguous (leading to state explosion), `Compile` MUST NOT crash the process. Instead, it MUST return the error `regexp: pattern too large or ambiguous`.
- **Cancellable Compilation**: Compilation supports `context.Context` via `CompileContext` to allow callers to abort resource-intensive builds.

### 2.12 DFA Minimization (Moore's Algorithm)
- **Equivalence-Based Merging**: After the initial transition table construction, the DFA MUST be minimized using Moore's algorithm (Partition Refinement).
- **Equivalence Criteria**: Two states are equivalent if and only if they share identical:
  1. Acceptance properties (Accepting, MatchPriority, MatchTags, IsBestMatch).
  2. Transition targets (mapped to equivalent groups).
  3. Priority increments AND transition tags for every possible byte.

### 2.13 Syntax-Level Factoring & Trie Optimization
Before NFA/DFA compilation, the syntax tree (especially `OpAlternate`) MUST be optimized to reduce redundancy and mitigate state explosion.
- **Prefix/Suffix Factoring**: Identical AST nodes at the beginning or end of alternative branches MUST be extracted (e.g., `a*c|b*c` -> `(?:a*|b*)c`). This unifies the exploration of common trailing or leading structures.
- **Literal Trie Optimization**: Sequences of literals within an alternation MUST be merged into a Trie-like structure (e.g., `apple|applejuice` -> `apple(?:juice|)`).
- **Semantics Preservation**: These optimizations MUST preserve the original leftmost-first priority order, ensuring compatibility with Go's standard library matching behavior.

### 2.14 Structural AST Normalization
To maximize deterministic efficiency, the AST MUST be normalized before DFA construction:
- **Literal Aggregation**: Consecutive single-character nodes MUST be merged into single `OpLiteral` nodes to minimize DFA state count.
- **Concat Flattening**: Nested `OpConcat` structures MUST be flattened to expand the scope of literal aggregation and factoring.
- **Anchor Hoisting**: Common anchors at the start or end of alternations MUST be hoisted out (e.g., `^a|^b` -> `^(?:a|b)`) to fix positioning constraints as early as possible.

### 2.15 Multi-Phase DFA Optimization
DFA construction is divided into two distinct phases to balance correctness and performance:
- **Phase 1: Base Construction**: Generate the deterministic state graph, ensuring leftmost-first semantics and absolute priority tracking.
- **Phase 2: Optimization Pass**: Analyze the constructed graph to identify high-performance execution hints:
  - **Warp Point Detection**: Identify states where only a single byte leads to progress (common in literals). These are marked as candidates for SIMD-accelerated skipping using `bytes.Index`.
  - **SCC Analysis**: Identify Strongly Connected Components where acceptance is guaranteed (Always True) to enable early loop exit.

### 2.16 Zero-Overhead Hot Loops
To maximize scan speed, `execLoop` and other hot loops MUST be free of `runtime.panicIndex` overhead:
- **Explicit BCE (Bounds Check Elimination)**: Hot loops MUST use explicit local slice variables and index checks to provide the compiler with enough hints to prove that every array access is safe.
- **Assembly Verification**: Critical matching loops MUST be periodically verified using `go tool compile -S` to ensure that `panicIndex` calls have been successfully eliminated from the hot path.

### 2.17 Single-Pass O(n) Search via Penalty Tracking
To strictly guarantee $O(n)$ time complexity, all search operations (finding a match anywhere in the string) MUST be performed in a single pass:
- **Search-Complete DFA**: The Search-enabled states of the transition table MUST be complete for every possible byte. If no pattern-specific transition exists, the DFA MUST fallback to the `SearchState` (the pattern start) or a relevant resumption state.
- **SearchRestartPenalty**: Every fallback transition that effectively skips a byte or restarts the search MUST increment the **Absolute Priority** by a fixed, large value (`SearchRestartPenalty`).
- **Absolute Priority Convergence**: The execution engine MUST track the match with the lowest Absolute Priority (restart penalties + NFA inner priority) during the single scan. This convergence identifies the true **leftmost-first** match without requiring multiple passes or backtracking, ensuring strictly linear performance regardless of anchor placement or pattern complexity.

## 3. Feature Selection Policy (Performance over Features)

### 3.1 Supported Features
- **Standard Syntax Compatibility**: Accept `syntax.Prog` instruction sequences from the standard Go parser.
- **Anchors & Boundaries**: Support `^`, `$`, `\b`, `\B` and multiline anchors via the **Virtual Byte Insertion** mechanism.
- **Capturing Groups**: Support extraction via the **Path-Guided 2-Pass DFA Strategy**.
- **Fixed-Length Lookahead/Lookbehind**: Support assertions that can be statically integrated into the DFA transition graph during compilation.

### 3.2 Excluded Features
- **Backreferences**: Strictly excluded to maintain $O(n)$ complexity and prevent exponential "catastrophic backtracking."
- **Dynamic Lookaround**: Complex or recursive assertions that require significant backtracking are restricted.
- **POSIX Semantics**: Standard Go `CompilePOSIX` and POSIX-style leftmost-longest matching are explicitly unsupported. These are **not provided in the API** to ensure compile-time detection of unsupported patterns and prevent accidental performance degradation.
- **Longest Match**: The `Longest()` method is not provided. The engine's matching priority is fixed at compile-time to maintain $O(n)$ and cache-locality mandates.

### 3.3 Interface Compatibility Policy
- **Interface Consistency**: We aim to provide a compatible interface for the most commonly used features (Find, Replace, Split, etc.) while adhering to our performance-first philosophy.

## 4. Engineering & Validation Standards
- **Performance-First Benchmarking**: Any change must be validated against the standard `regexp` package. Significant throughput regressions are unacceptable.
- **Scalability for Large Pattern Sets**: Ensure the engine maintains $O(n)$ performance even when merging tens of thousands of patterns.
- **SIMD Utilization**: Proactively use fast-skipping logic (e.g., `bytes.Index`) for pattern prefix matching before engaging the DFA. Aim for 5x to 100x higher throughput.
- **Submatch Isolation Diagnostics**: When submatch discrepancies occur, use isolation tests to determine if the error lies in Phase 1 (DFA boundary detection) or Phase 2 (DFA/NFA submatch extraction).

## 5. Coding Conventions
- **Explicit Aliasing for Standard Regexp Packages**: To avoid confusion between this engine and the standard library, always use explicit aliases when importing Go's standard `regexp` packages:
  - `regexp` must be imported as `goregexp`.
  - `regexp/syntax` must be imported as `gosyntax`.

---
**Note**: If a user request contradicts these principles, you MUST highlight the conflict and explain the potential performance impact before proceeding.
