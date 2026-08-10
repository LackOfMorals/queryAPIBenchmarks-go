package runner

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// progressInterval is how often the progress line is redrawn.
//
// The original code printed once per request, which meant one write(2) syscall
// per iteration from inside the timed region — and, in the concurrent path, one
// per goroutine with no synchronisation. That serialised workers on the
// terminal and inflated TotalSeconds. Over a ~93ms WAN link it was invisible;
// at sub-millisecond in-VPC latency it would have been a large fraction of the
// measured loop.
//
// A single ticker goroutine reading an atomic counter costs four syscalls per
// second regardless of throughput, and nothing on the request path.
const progressInterval = 250 * time.Millisecond

// progress renders a periodic "[done/total]" line to w, driven off a counter
// the caller increments.
type progress struct {
	w     io.Writer
	name  string
	total int
	done  *atomic.Int64
	stop  chan struct{}
	wg    sync.WaitGroup
}

// startProgress begins rendering. The caller must call Stop exactly once.
func startProgress(w io.Writer, name string, total int, done *atomic.Int64) *progress {
	p := &progress{
		w:     w,
		name:  name,
		total: total,
		done:  done,
		stop:  make(chan struct{}),
	}
	p.render(0)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		t := time.NewTicker(progressInterval)
		defer t.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-t.C:
				p.render(p.done.Load())
			}
		}
	}()

	return p
}

func (p *progress) render(n int64) {
	// Trailing spaces clear any longer line left behind by a previous render.
	fmt.Fprintf(p.w, "\r%s [%d/%d]      ", p.name, n, p.total)
}

// Stop halts rendering and writes a final line terminated with a newline.
func (p *progress) Stop() {
	close(p.stop)
	p.wg.Wait()
	fmt.Fprintf(p.w, "\r%s [%d/%d] done      \n", p.name, p.done.Load(), p.total)
}
