package eventconsumer

import (
	"context"
	"errors"
	"sync"
	"time"

	publicstoragepb "github.com/mooyang-code/moox/packages/storagepb"
	trpc "trpc.group/trpc-go/trpc-go"
)

type Config struct {
	URLs           []string
	FetchMaxWait   time.Duration
	CredentialFile string
}

const ViewSourceReadyConsumerName = "factor_view_ready_v1"

type ViewReadyExecutor interface {
	Execute(context.Context, string, string, *publicstoragepb.ViewSourcePeriodReady) error
}

type Consumer struct {
	cfg         Config
	executor    ViewReadyExecutor
	openSession func(context.Context) (natsConsumerSession, error)
	retryDelay  time.Duration
	session     natsConsumerSession
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	mu          sync.Mutex
	runErr      error
	ready       bool
}

func New(cfg Config, executor ViewReadyExecutor) *Consumer {
	return &Consumer{cfg: cfg, executor: executor, retryDelay: time.Second, openSession: nil}
}

func (c *Consumer) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if c.executor == nil {
		return errors.New("factor View-ready executor is required")
	}
	if c.openSession == nil {
		c.openSession = c.open
	}
	session, err := c.openSession(ctx)
	if err != nil {
		return err
	}
	c.startSessionLoop(ctx, session)
	return nil
}

func (c *Consumer) startSessionLoop(ctx context.Context, session natsConsumerSession) {
	loopCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.session = session
	c.ready = true
	c.mu.Unlock()
	c.wg.Add(1)
	go c.loop(loopCtx, session)
}

func (c *Consumer) Close() error {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.ready = false
	c.mu.Unlock()
	c.wg.Wait()
	c.mu.Lock()
	runErr := c.runErr
	c.mu.Unlock()
	return runErr
}

func (c *Consumer) Ready() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready
}

func (c *Consumer) loop(ctx context.Context, session natsConsumerSession) {
	defer c.wg.Done()
	for {
		runErr := session.Run(ctx)
		c.detachSession(session)
		closeErr := session.Close()
		if ctx.Err() != nil {
			return
		}
		c.recordError(errors.Join(runErr, closeErr))

		for {
			if !sleepConsumer(ctx, c.retryDelay) {
				return
			}
			next, err := c.openSession(ctx)
			if err != nil {
				c.recordError(err)
				continue
			}
			c.attachSession(next)
			session = next
			break
		}
	}
}

func (c *Consumer) detachSession(session natsConsumerSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == session {
		c.session = nil
		c.ready = false
	}
}

func (c *Consumer) attachSession(session natsConsumerSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = session
	c.ready = true
	c.runErr = nil
}

func sleepConsumer(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Consumer) recordError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.runErr = errors.Join(c.runErr, err)
	c.mu.Unlock()
}
