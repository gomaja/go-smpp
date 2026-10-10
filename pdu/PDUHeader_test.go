package pdu

import (
	"math"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSequenceWrap(t *testing.T) {
	counter := int32(0x7ffffffe)
	require.EqualValues(t, 0x7fffffff, nextSequenceNumber(&counter))
	require.EqualValues(t, 1, nextSequenceNumber(&counter))
	require.EqualValues(t, 2, nextSequenceNumber(&counter))
}

func TestSequenceConcurrentWrap(t *testing.T) {
	counter := int32(math.MaxInt32 - 512)
	const calls = 2048
	values := make(chan int32, calls)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range calls / 32 {
				values <- nextSequenceNumber(&counter)
			}
		}()
	}
	wg.Wait()
	close(values)
	seen := make(map[int32]bool, calls)
	for v := range values {
		require.Greater(t, v, int32(0))
		require.False(t, seen[v], "duplicate sequence %d around wrap", v)
		seen[v] = true
	}
	require.Len(t, seen, calls)
}

func TestNextSeq(t *testing.T) {
	var v int32 = math.MaxInt32
	require.EqualValues(t, 1, nextSequenceNumber(&v))
}
