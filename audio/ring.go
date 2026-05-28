package audio

import "sync/atomic"

// ringBuffer is a lock-free single-producer / single-consumer ring buffer
// for float32 audio samples. The producer writes, the consumer reads.
// Thread-safe with exactly one goroutine on each side.
type ringBuffer struct {
	buf     []float32
	cap     int64
	written atomic.Int64 // monotonic write cursor (producer only)
	rdPos   atomic.Int64 // monotonic read cursor  (consumer only)
}

func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{
		buf: make([]float32, capacity),
		cap: int64(capacity),
	}
}

func (r *ringBuffer) available() int {
	return int(r.written.Load() - r.rdPos.Load())
}

func (r *ringBuffer) freeSpace() int {
	return int(r.cap) - r.available()
}

// readPos returns the total number of samples consumed (used for position tracking).
func (r *ringBuffer) readPos() int64 {
	return r.rdPos.Load()
}

// write copies as many samples from src as fit in the buffer.
// Returns the number of samples actually written.
func (r *ringBuffer) write(src []float32) int {
	free := r.freeSpace()
	n := len(src)
	if n > free {
		n = free
	}
	if n == 0 {
		return 0
	}
	start := r.written.Load() % r.cap
	end := start + int64(n)
	if end <= r.cap {
		copy(r.buf[start:end], src[:n])
	} else {
		first := int(r.cap - start)
		copy(r.buf[start:], src[:first])
		copy(r.buf[:int64(n)-int64(first)], src[first:n])
	}
	r.written.Add(int64(n))
	return n
}

// readSamples copies up to n samples from the ring into dst.
// Returns the number of samples actually read.
func (r *ringBuffer) readSamples(dst []float32, n int) int {
	avail := r.available()
	if n > avail {
		n = avail
	}
	if n == 0 {
		return 0
	}
	start := r.rdPos.Load() % r.cap
	end := start + int64(n)
	if end <= r.cap {
		copy(dst[:n], r.buf[start:end])
	} else {
		first := int(r.cap - start)
		copy(dst[:first], r.buf[start:])
		copy(dst[first:n], r.buf[:int64(n)-int64(first)])
	}
	r.rdPos.Add(int64(n))
	return n
}
