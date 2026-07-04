package limiter

import (
	"strconv"
	"testing"
	"time"
)

// generous limits keep the hot path allowed; the sliding window log stays
// populated so prune+append cost is realistic, not an empty-slice best case

func BenchmarkTokenBucketAllow(b *testing.B) {
	tb := NewTokenBucket(Config{Limit: 1 << 30, Window: time.Minute})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tb.Allow("k")
	}
}

func BenchmarkSlidingWindowAllow(b *testing.B) {
	sw := NewSlidingWindow(Config{Limit: 1000, Window: time.Millisecond})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sw.Allow("k")
	}
}

func BenchmarkTokenBucketAllowDenied(b *testing.B) {
	tb := NewTokenBucket(Config{Limit: 1, Window: time.Hour})
	tb.Allow("k")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tb.Allow("k")
	}
}

func BenchmarkSlidingWindowAllowDenied(b *testing.B) {
	sw := NewSlidingWindow(Config{Limit: 1000, Window: time.Hour})
	for i := 0; i < 1000; i++ {
		sw.Allow("k")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sw.Allow("k")
	}
}

func BenchmarkTokenBucketParallel(b *testing.B) {
	tb := NewTokenBucket(Config{Limit: 1 << 30, Window: time.Minute})
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			tb.Allow("k" + strconv.Itoa(i%64))
			i++
		}
	})
}

func BenchmarkSlidingWindowParallel(b *testing.B) {
	sw := NewSlidingWindow(Config{Limit: 1000, Window: time.Millisecond})
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			sw.Allow("k" + strconv.Itoa(i%64))
			i++
		}
	})
}
