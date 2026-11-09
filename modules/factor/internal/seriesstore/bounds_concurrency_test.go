package seriesstore

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 多个读者同时第一次读取：每个读者都拿到全部标的（不会拿到正在扫描中的空缓存或一部分）。
func TestSubjectBoundsConcurrentFirstReaders(t *testing.T) {
	for round := 0; round < 20; round++ {
		s := openTest(t, t.TempDir())
		subjects := []string{"A", "B", "C", "D", "E", "F", "G", "H"}
		for i, subject := range subjects {
			write(t, s, subject, i, i+1, rowsOf(map[int]float64{i: float64(100 + i)}), false)
		}
		results := make(chan int, 8)
		start := make(chan struct{})
		for r := 0; r < 8; r++ {
			go func() {
				<-start
				got, err := s.SubjectBounds(testSrc)
				if err != nil {
					results <- -1
					return
				}
				results <- len(got)
			}()
		}
		close(start)
		for r := 0; r < 8; r++ {
			require.Equal(t, len(subjects), <-results, "round %d", round)
		}
	}
}

// 读写交错：两个读者与一个写入者并发，写入完成之后的读取一定是新边界（旧边界不会覆盖新边界）。
func TestSubjectBoundsReadersAndWriterInterleave(t *testing.T) {
	s := openTest(t, t.TempDir())
	write(t, s, "A", 0, 1, rowsOf(map[int]float64{0: 100}), false)
	_, err := s.SubjectBounds(testSrc)
	require.NoError(t, err)
	stop := make(chan struct{})
	readers := make(chan struct{}, 2)
	for r := 0; r < 2; r++ {
		go func() {
			defer func() { readers <- struct{}{} }()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = s.SubjectBounds(testSrc)
			}
		}()
	}
	for m := 1; m < 60; m++ {
		write(t, s, "A", m, m+1, rowsOf(map[int]float64{m: float64(100 + m)}), false)
	}
	close(stop)
	<-readers
	<-readers
	got, err := s.SubjectBounds(testSrc)
	require.NoError(t, err)
	require.Equal(t, at(59), got["A"].Latest)
	direct, _, err := s.Bounds(testSrc, "A")
	require.NoError(t, err)
	require.Equal(t, direct, got["A"])
}
