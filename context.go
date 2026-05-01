package regexp

import (
	"sync"
)

const (
	histWarpMarker  uint32 = 0x80000000
	histLengthMask  uint32 = 0x7FF00000
	histStateMask   uint32 = 0x000FFFFF
	histLengthShift        = 20
	histMaxLength          = 2047
)

type matchContext struct {
	historyBuf     [1024]uint32
	history        []uint32
	pathHistoryBuf [1024]int32
	pathHistory    []int32
	regsBuf        [32]int
	regs           []int
	absBase        int // Absolute position of the start of the current scan

	// Discovery results (Pass 1)
	matchStart int
	matchEnd   int
	matchPrio  int32
}

func (mc *matchContext) prepare(n int, numSubexp int, absBase int) {
	mc.absBase = absBase
	mc.matchStart = -1
	mc.matchEnd = -1
	mc.matchPrio = -1

	required := n + 2
	if required > len(mc.historyBuf) {
		if cap(mc.history) < required {
			mc.history = make([]uint32, 0, required)
		} else {
			mc.history = mc.history[:0]
		}
		if cap(mc.pathHistory) < required {
			mc.pathHistory = make([]int32, required)
		} else {
			mc.pathHistory = mc.pathHistory[:required]
		}
	} else {
		mc.history = mc.historyBuf[:0]
		mc.pathHistory = mc.pathHistoryBuf[:required]
	}

	requiredRegs := (numSubexp + 1) * 2
	if requiredRegs <= len(mc.regsBuf) {
		mc.regs = mc.regsBuf[:requiredRegs]
	} else {
		if cap(mc.regs) < requiredRegs {
			mc.regs = make([]int, requiredRegs)
		} else {
			mc.regs = mc.regs[:requiredRegs]
		}
	}
	for i := range mc.regs {
		mc.regs[i] = -1
	}
}

func (mc *matchContext) clearHistory() {
	mc.history = mc.history[:0]
}

func (mc *matchContext) resetForRecording(start, end int) {
	mc.history = mc.history[:0]
	// pathHistory needs to cover [0, match_length] where match_length = end - start.
	// But current implementation uses absolute indices for pathHistory.
	// Let's clear only the necessary portion.
	needed := end + 1
	if needed > len(mc.pathHistory) {
		mc.pathHistory = make([]int32, needed)
	}
	for i := start; i <= end; i++ {
		mc.pathHistory[i] = -1
	}
}

func (mc *matchContext) appendRaw(sidx uint32) {
	mc.history = append(mc.history, sidx&histStateMask)
}

func (mc *matchContext) appendWarp(sidx uint32, n int) {
	sidx &= histStateMask
	if len(mc.history) > 0 {
		last := mc.history[len(mc.history)-1]
		if (last&histWarpMarker) != 0 && (last&histStateMask) == sidx {
			lenVal := (last & histLengthMask) >> histLengthShift
			if int(lenVal)+n <= histMaxLength {
				mc.history[len(mc.history)-1] = histWarpMarker | ((lenVal + uint32(n)) << histLengthShift) | sidx
				return
			}
		}
	}
	mc.history = append(mc.history, histWarpMarker|((uint32(n))<<histLengthShift)|sidx)
}

var matchContextPool = sync.Pool{New: func() any { return &matchContext{} }}
