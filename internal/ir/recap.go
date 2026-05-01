package ir

type TransitionUpdate struct {
	BasePriority            int32
	PreUpdates, PostUpdates []PathTagUpdate
}

type PathTagUpdate struct {
	RelativePriority, NextPriority int32
	IsMatch                        bool
	PreTags, PostTags              uint64
}

type RecapEntry struct {
	InputPriority, NextPriority int32
	IsMatch                     bool
	PreTags, PostTags           uint64
	WarpTags                    []WarpTagBundle
}

type WarpTagBundle struct {
	Offset int
	Tags   uint64
}

type GroupRecapTable struct{ Transitions [][]RecapEntry }
