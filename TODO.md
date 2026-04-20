# TDFA Pipeline Stabilization TODO

## 1. Naked Indexing Isolation (Pass 1) [DONE]
- [x] `internal/ir/dfa.go`: Fixed bit layout and public constants.
  - Layout: `[31: Tagged] [30: Anchor] [29: Warp] [28-22: AnchorMask] [21-0: StateIndex]`
  - `StateIDMask = 0x003FFFFF`
- [x] `regexp.go`: Enforced flag stripping before recording to `mc.history[i]`.
- [x] **Validation (White-box)**: Verified purity in `repro_test.go`.

## 2. Streamlined Reconstruction (Pass 2 & 3) [DONE]
- [x] `regexp.go`: Removed redundant masking in Pass 2 and Pass 3.
- [x] **Constraints**: Successfully utilized naked history without memory panics.

## 3. Precision Refinement [DONE]
- [x] `internal/ir/dfa.go`: Migrated tags to transitions (`RecapEntry`) to support Naked State Identity.
- [x] Resolved offset errors in `a(b)c` and multi-byte patterns by refining tag application timing (`i+step`).
- [x] **Validation**: Confirmed all cases pass in `pass3_test.go`.

## 4. Logic Integrity [DONE]
- [x] **Greedy Loop Correction**: Refined `IsBestMatch` to prevent premature termination of greedy repetitions.
- [x] **Pass 2 Priority Synchronization**: Implemented `BasePriority` accumulation to maintain correct priority chains.
- [x] **Validation**: Confirmed greedy and leftmost-first behavior in `priority_test.go`.

## 5. Final Validation [IN PROGRESS]
- [ ] Resolve remaining standard library compatibility issues in `compat_test.go` (Find/ReplaceAll semantics).
- [ ] Execute `benchmark-full.sh` to verify performance gains (Target: 5x+ vs standard library).
