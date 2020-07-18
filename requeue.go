package requeue

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nickpoorman/nats-requeue/flatbuf"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/segmentio/ksuid"

	badger "github.com/dgraph-io/badger/v2"
	"github.com/dgraph-io/badger/v2/y"
)

const (
	// DefaultNatsServers is the default nats server URLs (separated by comma).
	DefaultNatsServers = nats.DefaultURL

	// DefaultNatsClientName is the default name to assign to the NATS client
	// connection.
	DefaultNatsClientName = "requeue-nats"

	// DefaultNatsRetryOnFailure is true by default so that requeue will attempt
	// to automatically reconnect to nats on a failure.
	DefaultNatsRetryOnFailure = true

	// DefaultNatsSubject is the deafult subject requeue will subscribe to for
	// messages. By default `requeue.>` will match
	// `requeue.foo`, `requeue.foo.bar`, and `requeue.foo.bar.baz`.
	// ">" matches any length of the tail of a subject, and can only be the last token
	// E.g. 'foo.>' will match 'foo.bar', 'foo.bar.baz', 'foo.foo.bar.bax.22'.
	DefaultNatsSubject = "requeue.>"

	// DefaultNatsQueueName is the default queue to subscribe to. Messages from
	// the queue will be distributed amongst the the subscribers of the queue.
	DefaultNatsQueueName = "requeue-workers"

	keySeperator byte = '.'

	DefaultNumConcurrentBatchTransactions = 4
)

func Connect(options ...Option) (*Conn, error) {
	opts := GetDefaultOptions()
	for _, opt := range options {
		if opt != nil {
			if err := opt(&opts); err != nil {
				return nil, err
			}
		}
	}
	return opts.Connect()
}

// Option is a function on the options to connect a Service.
type Option func(*Options) error

// ConnectContext sets the context to be used for connect.
func ConnectContext(ctx context.Context) Option {
	return func(o *Options) error {
		o.ctx = ctx
		return nil
	}
}

// NATSServers is the nats server URLs (separated by comma).
func NATSServers(natsServers string) Option {
	return func(o *Options) error {
		o.NatsServers = natsServers
		return nil
	}
}

// NATSSubject is the subject requeue will subscribe to for
// messages. By default `requeue.>` will match
// `requeue.foo`, `requeue.foo.bar`, and `requeue.foo.bar.baz`.
// ">" matches any length of the tail of a subject, and can only be the last token
// E.g. 'foo.>' will match 'foo.bar', 'foo.bar.baz', 'foo.foo.bar.bax.22'.
func NATSSubject(natsSubject string) Option {
	return func(o *Options) error {
		o.NatsSubject = natsSubject
		return nil
	}
}

// NatsQueueName is the queue to subscribe to. Messages from the queue will be
// distributed amongst the the subscribers of the queue.
func NATSQueueName(natsQueueName string) Option {
	return func(o *Options) error {
		o.NatsQueueName = natsQueueName
		return nil
	}
}

// NATSOptions are options that will be provided to NATS upon establishing a
// connection.
func NATSOptions(natsOptions []nats.Option) Option {
	return func(o *Options) error {
		o.NatsOptions = natsOptions
		return nil
	}
}

// NATSConnectionError is a callback when the connection is unable to be
// established.
func NATSConnectionError(connErrCb func(*Conn, error)) Option {
	return func(o *Options) error {
		o.NatsConnErrCB = connErrCb
		return nil
	}
}

// BadgerDataPath sets the context to be used for connect.
func BadgerDataPath(path string) Option {
	return func(o *Options) error {
		o.BadgerDataPath = path
		return nil
	}
}

// BadgerWriteErr sets the callback to be triggered when there is an error
// writing a message to Badger.
func BadgerWriteMsgErr(cb func(*nats.Msg, error)) Option {
	return func(o *Options) error {
		o.BadgerWriteMsgErr = cb
		return nil
	}
}

// Options can be used to create a customized Service connections.
type Options struct {
	ctx context.Context

	// Nats
	NatsServers   string
	NatsSubject   string
	NatsQueueName string
	NatsOptions   []nats.Option
	NatsConnErrCB func(*Conn, error)

	// Badger
	BadgerDataPath    string
	BadgerWriteMsgErr func(*nats.Msg, error)
}

func GetDefaultOptions() Options {
	return Options{
		ctx:           context.Background(),
		NatsServers:   DefaultNatsServers,
		NatsSubject:   DefaultNatsSubject,
		NatsQueueName: DefaultNatsQueueName,
		NatsOptions: []nats.Option{
			nats.Name(DefaultNatsClientName),
			nats.RetryOnFailedConnect(DefaultNatsRetryOnFailure),
		},
	}
}

// Connect will attempt to connect to a NATS server with multiple options
// and setup connections to the disk database.
func (o Options) Connect() (*Conn, error) {
	rc := NewConn(o)

	if err := rc.initBadger(); err != nil {
		rc.Close()
		return nil, err
	}

	if err := rc.initNATS(); err != nil {
		rc.Close()
		return nil, err
	}

	// Start consumers to process messages
	if err := rc.initNatsConsumers(); err != nil {
		rc.Close()
		return nil, err
	}

	go func() {
		// Context closed.
		<-o.ctx.Done()
		rc.Close()
	}()

	// Setup the interrupt handler to drain so we don't miss
	// requests when scaling down.
	c := make(chan os.Signal, 1)
	signal.Notify(c,
		os.Interrupt,
		syscall.SIGTERM, // AWS sometimes improperly uses SIGTERM.
	)

	go func() {
		<-c
		// Got interrupt. Close things down.
		rc.Close()
	}()

	return rc, nil
}

type closers struct {
	nats          *y.Closer
	natsConsumers *y.Closer
	badger        *y.Closer
}

type Conn struct {
	Opts Options

	mu sync.RWMutex

	// Nats
	nc        *nats.Conn
	sub       *nats.Subscription
	natsMsgCh chan *nats.Msg

	// Badger
	badgerDB *badger.DB

	closeOnce sync.Once
	closed    chan struct{}
	closers   closers
}

func NewConn(o Options) *Conn {
	return &Conn{
		Opts:      o,
		natsMsgCh: make(chan *nats.Msg),
		closed:    make(chan struct{}),
		closers: closers{
			nats:          y.NewCloser(0),
			natsConsumers: y.NewCloser(0),
			badger:        y.NewCloser(0),
		},
	}
}

func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		log.Info().Msg("requeue: closing...")
		// Stop nats
		c.closers.nats.SignalAndWait()
		// Stop processing nats messages
		c.closers.natsConsumers.SignalAndWait()
		// Stop badger
		c.closers.badger.SignalAndWait()
		log.Info().Msg("requeue: closed")
		close(c.closed)
	})
}

func (c *Conn) HasBeenClosed() <-chan struct{} {
	return c.closed
}

func (c *Conn) NATSDisconnectErrHandler(nc *nats.Conn, err error) {
	log.Err(err).Msgf("nats-replay: Got disconnected!")
}

func (c *Conn) NATSErrorHandler(con *nats.Conn, sub *nats.Subscription, natsErr error) {
	log.Err(natsErr).Msgf("nats-replay: Got err: conn=%s sub=%s err=%v!", con.Opts.Name, sub.Subject, natsErr)

	if natsErr == nats.ErrSlowConsumer {
		pendingMsgs, _, err := sub.Pending()
		if err != nil {
			log.Err(err).Msg("nats-replay: couldn't get pending messages")
			return
		}
		log.Err(err).Msgf("nats-replay: Falling behind with %d pending messages on subject %q.\n",
			pendingMsgs, sub.Subject)
		// Log error, notify operations...
	}
	// check for other errors
}

func (c *Conn) NATSReconnectHandler(nc *nats.Conn) {
	// Note that this will be invoked for the first asynchronous connect.
	log.Info().Msgf("nats-replay: Got reconnected to %s!", nc.ConnectedUrl())
}

func (c *Conn) NATSClosedHandler(nc *nats.Conn) {
	err := nc.LastError()
	log.Err(err).Msg("nats-replay: Connection closed")
	if c.Opts.NatsConnErrCB != nil {
		c.Opts.NatsConnErrCB(c, err)
	}

	// Close anything left open (such as badger).
	c.Close()
}

func (c *Conn) initNATS() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var err error
	o := c.Opts
	rc := c

	// TODO(nickpoorman): We may want to provide our own callbacks for these
	// in case the user wants to hook into them as well.
	o.NatsOptions = append(o.NatsOptions,
		nats.DisconnectErrHandler(rc.NATSDisconnectErrHandler),
		nats.ReconnectHandler(rc.NATSReconnectHandler),
		nats.ClosedHandler(rc.NATSClosedHandler),
		nats.ErrorHandler(rc.NATSErrorHandler),
	)

	// Connect to NATS
	rc.nc, err = nats.Connect(o.NatsServers, o.NatsOptions...)
	if err != nil {
		log.Err(err).Msgf("nats-replay: unable to connec to servers: %s", o.NatsServers)
		// Because we retry our connection, this error would be a configuration error.
		return err
	}

	// Close nats when the closer is signaled.
	rc.closers.nats.AddRunning(1)
	go func() {
		defer rc.closers.nats.Done()
		<-c.closers.nats.HasBeenClosed()

		// Close nats
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.nc != nil {
			log.Debug().Msg("draining nats...")
			if err := c.nc.Drain(); err != nil {
				log.Err(err).Msg("error draining nats")
			}
			log.Debug().Msg("drained nats")

			log.Debug().Msg("closing nats...")
			c.nc.Close()
			log.Debug().Msg("closed nats")
		}
	}()

	sub, err := rc.nc.QueueSubscribe(o.NatsSubject, o.NatsQueueName, func(msg *nats.Msg) {
		// fb := flatbuf.GetRootAsRequeueMessage(msg.Data, 0)
		// log.Debug().Str("msg", string(fb.OriginalPayloadBytes())).Msg("got message")
		c.natsMsgCh <- msg
		// log.Debug().Str("msg", string(fb.OriginalPayloadBytes())).Msg("processed message")
	})

	// Subscribe to the subject using the queue group.
	// sub, err := rc.nc.QueueSubscribeSyncWithChan(o.NatsSubject, o.NatsQueueName, c.natsMsgCh)
	if err != nil {
		log.Err(err).Dict("nats",
			zerolog.Dict().
				Str("subject", o.NatsSubject).
				Str("queue", o.NatsQueueName)).
			Msg("nats-replay: unable to subscribe to queue")
		return err
	}
	// if err := sub.SetPendingLimits(10000, -1); err != nil {
	// 	log.Err(err).Msg("nats-replay: SetPendingLimits")
	// 	// Don't die, we'll just continue with the default limits.
	// }

	rc.sub = sub
	rc.nc.Flush()

	if err := rc.nc.LastError(); err != nil {
		log.Err(err).Msg("nats-replay: LastError")
		return err
	}

	log.Info().
		Dict("nats",
			zerolog.Dict().
				Str("subject", o.NatsSubject).
				Str("queue", o.NatsQueueName)).
		Msgf("Listening on [%s] in queue group [%s]", o.NatsSubject, o.NatsQueueName)

	return nil
}

func (c *Conn) initBadger() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	openOpts := badger.DefaultOptions(c.Opts.BadgerDataPath)
	openOpts.Logger = badgerLogger{}
	// Open the Badger database located in the /tmp/badger directory.
	// It will be created if it doesn't exist.
	db, err := badger.Open(openOpts)
	if err != nil {
		log.Err(err).Msgf("problem opening badger data path: %s", c.Opts.BadgerDataPath)
	}
	c.badgerDB = db

	c.closers.badger.AddRunning(1)
	go func() {
		defer c.closers.badger.Done()
		<-c.closers.badger.HasBeenClosed()
		// Badger cannot stop until nats has.
		// This probably isn't necessary since we already wait for it to close
		// before signaling badger to close, but adding it to be certain.
		<-c.closers.nats.HasBeenClosed()

		log.Debug().Msg("closing badger...")
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.badgerDB != nil {
			c.badgerDB.Close()
		}
		log.Debug().Msg("closed badger")
	}()

	return nil
}

func (c *Conn) initNatsConsumers() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	c.closers.natsConsumers.AddRunning(DefaultNumConcurrentBatchTransactions)

	for i := 0; i < DefaultNumConcurrentBatchTransactions; i++ {
		go c.initNatsConsumer()
	}

	return nil
}

func (c *Conn) initNatsConsumer() {
	c.mu.RLock()
	natsConsumer := c.closers.natsConsumers
	defer natsConsumer.Done()

	wb := newBatchedWriter(c.badgerDB, 15*time.Millisecond)
	defer wb.Close()
	c.mu.RUnlock()

	for {
		select {
		case msg := <-c.natsMsgCh:
			c.processIngressMessage(wb, msg)
		case <-natsConsumer.HasBeenClosed():
			// The consumer has been asked to close.
			// Flushing will be handled by the above defer wb.Close()
			return
		}
	}
}

func (c *Conn) processIngressMessage(wb *batchedWriter, msg *nats.Msg) {
	fb := flatbuf.GetRootAsRequeueMessage(msg.Data, 0)
	// decoded := RequeueMessageFromNATS(msg)
	log.Debug().
		Str("msg", string(fb.OriginalPayloadBytes())).
		Msg("received a message")

	// Build the key
	// TODO(nickpoorman): Write benchmark to see which of these is faster
	key := []byte(fmt.Sprintf("%s.%s", string(flatbuf.GetRootAsRequeueMessage(msg.Data, 0).OriginalSubject()), ksuid.New().String()))
	// TODO(nickpoorman): This may be faster but I need to test it out
	// o := fb.OriginalSubject()
	// k := ksuid.New().Bytes() // can we use unencoded use bytes here or do we need .String()?
	// key := make([]byte, len(o)+len(k)+1)
	// copy(key, o)
	// key[len(o)] = keySeperator
	// copy(key[len(o)+1:], k)
	// log.Debug().Msgf("created key: %s", string(key))

	if err := wb.Set(
		key,
		msg.Data,
		processIngressMessageCallback(msg)); err != nil {
		log.Err(err).Msg("problem calling Set on WriteBatch")
		if c.Opts.BadgerWriteMsgErr != nil {
			c.Opts.BadgerWriteMsgErr(msg, err)
		}
	}
}

// A commit from batchedWriter will trigger a batch of callbacks,
// one for each message.
func processIngressMessageCallback(msg *nats.Msg) func(err error) {
	return func(err error) {
		fb := flatbuf.GetRootAsRequeueMessage(msg.Data, 0)
		if err != nil {
			log.Err(err).
				Str("msg", string(fb.OriginalPayloadBytes())).
				Msgf("problem committing message")
		}
		// ml, bl, err := c.sub.PendingLimits()
		// if err != nil {
		// 	log.Err(err).Msg("PendingLimits")
		// }
		log.Debug().
			Str("msg", string(fb.OriginalPayloadBytes())).
			// Int("pending-limits-msg", ml).
			// Int("pending-limits-size", bl).
			Str("Reply", msg.Reply).
			Str("Subject", msg.Subject).
			Msgf("committed message")

		// Ack the message
		if err := msg.Respond(nil); err != nil {
			log.Err(err).
				Str("msg", string(fb.OriginalPayloadBytes())).
				Msgf("problem sending ACK for message")
		}

	}
}
