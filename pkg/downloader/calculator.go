package downloader

import "sync/atomic"

type Chunk struct {
	Index int
	Start int64
	End   int64
}

type AdaptiveTracker struct {
	Index       int
	StartByte   int64
	CurrentPtr  int64
	EndBoundary int64
	Claimed     int32
}

func (at *AdaptiveTracker) GetStart() int64 {
	return atomic.LoadInt64(&at.StartByte)
}

func (at *AdaptiveTracker) GetCurrent() int64 {
	return atomic.LoadInt64(&at.CurrentPtr)
}

func (at *AdaptiveTracker) GetEnd() int64 {
	return atomic.LoadInt64(&at.EndBoundary)
}

func (at *AdaptiveTracker) TryClaim() bool {
	return atomic.CompareAndSwapInt32(&at.Claimed, 0, 1)
}

func (at *AdaptiveTracker) Release() {
	atomic.StoreInt32(&at.Claimed, 0)
}

func (at *AdaptiveTracker) IsCompleted() bool {
	end := at.GetEnd()
	if end <= 0 {
		return false
	}
	return at.GetCurrent() > end
}

// ClaimNextUnfinishedTracker atomically claims an unfinished tracker.
// If preferredIndex is valid and unfinished, it attempts to claim that first.
func ClaimNextUnfinishedTracker(trackers []*AdaptiveTracker, preferredIndex int) *AdaptiveTracker {
	if preferredIndex >= 0 && preferredIndex < len(trackers) {
		tr := trackers[preferredIndex]
		if !tr.IsCompleted() && tr.TryClaim() {
			if !tr.IsCompleted() {
				return tr
			}
			tr.Release()
		}
	}

	for _, tr := range trackers {
		if tr.IsCompleted() {
			continue
		}
		if tr.TryClaim() {
			if !tr.IsCompleted() {
				return tr
			}
			tr.Release()
		}
	}
	return nil
}

// AllTrackersCompleted returns true if every tracker in the slice has finished.
func AllTrackersCompleted(trackers []*AdaptiveTracker) bool {
	for _, tr := range trackers {
		if !tr.IsCompleted() {
			return false
		}
	}
	return true
}

func CalculateChunks(fileSize int64, numThreads int) []Chunk {
	// Only return an open-ended chunk when the total size is unknown.
	if fileSize <= 0 {
		return []Chunk{
			{
				Index: 0,
				Start: 0,
				End:   fileSize, // 0 or -1 represents open stream
			},
		}
	}

	if numThreads <= 1 {
		numThreads = 1
	}

	var chunks []Chunk
	chunkSize := fileSize / int64(numThreads)

	for i := 0; i < numThreads; i++ {
		startByte := int64(i) * chunkSize
		var endByte int64

		if i == numThreads-1 {
			endByte = fileSize - 1
		} else {
			endByte = startByte + chunkSize - 1
		}

		chunks = append(chunks, Chunk{
			Index: i,
			Start: startByte,
			End:   endByte,
		})
	}

	return chunks
}

func StealWork(trackers []*AdaptiveTracker, dynamicMinChunk int64) (int64, int64, *AdaptiveTracker) {
	var targetTracker *AdaptiveTracker
	var maxRemaining int64 = 0

	for _, tr := range trackers {
		current := atomic.LoadInt64(&tr.CurrentPtr)
		end := atomic.LoadInt64(&tr.EndBoundary)
		if end <= 0 {
			continue // Do not steal from open-ended dynamic streams
		}
		remaining := end - current

		if remaining > maxRemaining {
			maxRemaining = remaining
			targetTracker = tr
		}
	}

	if maxRemaining < dynamicMinChunk*2 {
		return 0, 0, nil
	}

	for {
		current := atomic.LoadInt64(&targetTracker.CurrentPtr)
		end := atomic.LoadInt64(&targetTracker.EndBoundary)
		remaining := end - current

		if remaining < dynamicMinChunk*2 {
			return 0, 0, nil
		}

		midpoint := current + (remaining / 2)

		if atomic.CompareAndSwapInt64(&targetTracker.EndBoundary, end, midpoint) {
			return midpoint + 1, end, targetTracker
		}
	}
}
