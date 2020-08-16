package statspub

import (
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nickpoorman/nats-requeue/internal/queue"
	"github.com/nickpoorman/nats-requeue/internal/ticker"
	"github.com/rs/zerolog/log"
)

const (
	DefaultStatsPublisherInterval = 5 * time.Second
)

// Options can be used to set custom options for a StatsPublisher.
type Options struct {
	// On this interval, the queue will be scanned for messages
	// that are ready to be published.
	pubInterval time.Duration
}

func GetDefaultOptions() Options {
	return Options{
		pubInterval: DefaultStatsPublisherInterval,
	}
}

// Option is a function on the options for a StatsPublisher.
type Option func(*Options) error

// On this interval, the stats will be published.
func StatsPublishInterval(interval time.Duration) Option {
	return func(o *Options) error {
		o.pubInterval = interval
		return nil
	}
}

type StatsPublisher struct {
	qManager   *queue.Manager
	nc         *nats.Conn
	instanceId string

	opts Options

	quit chan struct{}
	done chan struct{}
}

func NewStatsPublisher(nc *nats.Conn, qManager *queue.Manager, instanceId string, options ...Option) (*StatsPublisher, error) {
	opts := GetDefaultOptions()
	for _, opt := range options {
		if opt != nil {
			if err := opt(&opts); err != nil {
				return nil, err
			}
		}
	}

	rq := &StatsPublisher{
		qManager:   qManager,
		nc:         nc,
		instanceId: instanceId,
		opts:       opts,
		quit:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	go rq.initBackgroundTasks()

	return rq, nil
}

func (sp *StatsPublisher) initBackgroundTasks() {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		wg.Wait()
		close(sp.done)
	}()

	// republish loop
	go func() {
		defer wg.Done()
		t := ticker.New(sp.opts.pubInterval)
		go func() {
			<-sp.quit
			t.Stop()
		}()
		t.Loop(func() bool {
			sp.publish()
			return true
		})
	}()
}

func (sp *StatsPublisher) Close() {
	close(sp.quit)
	<-sp.done
}

func (sp *StatsPublisher) publish() {
	log.Debug().Msg("StatsPublisher: publish: triggered.")

	// stats := make(map[string]interface{})

	// // TODO: Collect the stats from the queue manager
	// queues := sp.qManager.Queues()
	// for _, q := range queues {
	// 	sm := q.Stats.ToMap()
	// 	// stats[]

	// }

	// TODO: Emit the stats on a topic
}