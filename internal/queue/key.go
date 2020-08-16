package queue

import (
	"bytes"
	"fmt"

	"github.com/nickpoorman/nats-requeue/internal/debug"
	"github.com/nickpoorman/nats-requeue/internal/key"
)

// --------------------------------------------------------------------------

// All messages are stored under the _q namespace.
// Queues each have their own name under the _m and _s buckets, e.g., _q._s.high.
// Buckets are used to group properties. For example all messages are written
// to the _m bucket and all state properties are written to the _s bucket.
//
// Some examples:
// _q._m.high.aWgEPTl1tmebfsQzFP4bxwgy80V
// _q._s.high.checkpoint
// _q._s.medium.checkpoint
// _q._s.low.checkpoint
// _q._s.low.other_state_property

const (
	sep                = "."
	sepBytes           = 1
	QueuesNamespace    = "_q"
	namespaceKeyBytes  = 2
	bucketKeyBytes     = 2
	MessagesBucket     = "_m"
	StateBucket        = "_s"
	CheckpointProperty = "checkpoint"
)

type QueueKey struct {
	Namespace string
	Bucket    string
	Name      string

	Property string
	Key      key.Key
}

func NewQueueKeyForMessage(queue string, key key.Key) QueueKey {
	return QueueKey{
		Namespace: QueuesNamespace,
		Bucket:    MessagesBucket,
		Name:      queue,
		Key:       key,
	}
}

func NewQueueKeyForState(queue, property string) QueueKey {
	return QueueKey{
		Namespace: QueuesNamespace,
		Bucket:    StateBucket,
		Name:      queue,
		Property:  property,
	}
}

func ParseQueueKey(k []byte) QueueKey {
	spl := bytes.Split(k, []byte(sep))
	debug.Assert(len(spl) == 4, fmt.Errorf("invalid QueueKey: %v", k))
	debug.Assert(len(spl[3]) == key.Size, fmt.Errorf("invalid QueueKey.Key size: Expected=%d Got=%d QueueKey=%v", key.Size, len(spl[3]), spl[3]))
	return QueueKey{
		Namespace: string(spl[0]),
		Bucket:    string(spl[1]),
		Name:      string(spl[2]),
		Key:       spl[3],
	}
}

func (q QueueKey) IsKey() bool {
	return q.Key != nil
}

func (q QueueKey) Bytes() []byte {
	ns := []byte(q.Namespace)
	bk := []byte(q.Bucket)
	na := []byte(q.Name)
	sp := []byte(sep)
	var p []byte
	if q.IsKey() {
		p = q.Key
	} else {
		p = []byte(q.Property)
	}
	qk := make([]byte, len(ns)+len(sp)+len(bk)+len(sp)+len(na)+len(sp)+len(p))
	off := copy(qk, ns)
	off += copy(qk[off:], sp)
	off += copy(qk[off:], bk)
	off += copy(qk[off:], sp)
	off += copy(qk[off:], na)
	off += copy(qk[off:], sp)
	copy(qk[off:], p)
	return qk
}

func (q QueueKey) BucketPath() string {
	return fmt.Sprintf("%s.%s", q.Namespace, q.Bucket)
}

func (q QueueKey) NamePath() string {
	return fmt.Sprintf("%s.%s.%s", q.Namespace, q.Bucket, q.Name)
}

func (q QueueKey) PropertyPath() string {
	return fmt.Sprintf("%s.%s.%s.%s", q.Namespace, q.Bucket, q.Name, q.PropertyString())
}

func (q QueueKey) PropertyString() string {
	if q.IsKey() {
		return q.Key.Print()
	}
	return string(q.Property)
}

func (q QueueKey) String() string {
	return q.PropertyPath()
}

// PrefixOf a common prefix between two keys (common leading bytes) which is
// then used as a prefix for Badger to narrow down SSTables to traverse.
func PrefixOf(seek, until []byte) []byte {
	var prefix []byte

	// Calculate the minimum length
	length := len(seek)
	if len(until) < length {
		length = len(until)
	}

	// Iterate through the bytes and append common ones
	for i := 0; i < length; i++ {
		if seek[i] != until[i] {
			break
		}
		prefix = append(prefix, seek[i])
	}
	return prefix
}

// FirstMessage returns the smallest possible key given the queue.
func FirstMessage(queue string) QueueKey {
	return NewQueueKeyForMessage(queue, key.Min)
}

// LastMessage returns the largest possible key given the queue.
func LastMessage(queue string) QueueKey {
	return NewQueueKeyForMessage(queue, key.Max)
}

// ---------------------------------------------------------------------------

// // RawMessageQueueKey represents a lexicographically sorted key
// type RawMessageQueueKey []byte

// func ksuidLen() int {
// 	return len(ksuid.Max)
// }

// func New(prefix []byte) (RawMessageQueueKey, error) {
// 	return NewWithTime(prefix, time.Now())
// }

// func NewWithTime(prefix []byte, t time.Time) (RawMessageQueueKey, error) {
// 	k, err := ksuid.NewRandomWithTime(t)
// 	if err != nil {
// 		return RawMessageQueueKey{}, err
// 	}
// 	return FromParts(prefix, k), nil
// }

// func FromParts(prefix []byte, k ksuid.KSUID) RawMessageQueueKey {
// 	pl := len(prefix)
// 	key := make([]byte, 0, pl+ksuidLen())
// 	key = append(key, prefix...)
// 	copy(key[pl:], k.Bytes())
// 	return key
// }

// func FromBytes(key []byte) RawMessageQueueKey {
// 	return RawMessageQueueKey(key)
// }

// // First returns the smallest possible key given the prefix.
// func First(prefix []byte) RawMessageQueueKey {
// 	k := make([]byte, len(prefix)+ksuidLen())
// 	copy(k, []byte(prefix))
// 	return k
// }

// // Last returns the largest possible key given the prefix.
// func Last(prefix []byte) RawMessageQueueKey {
// 	return FromParts(prefix, ksuid.Max)
// }

// func (k RawMessageQueueKey) Bytes() []byte {
// 	return k[:]
// }

// func (k RawMessageQueueKey) Prefix() []byte {
// 	// KSUID is at the end, so prefix is everything before that.
// 	return k[:len(k)-ksuidLen()]
// }

// func (k RawMessageQueueKey) KSUID() ksuid.KSUID {
// 	// KSUID is at the end after the prefix.
// 	id, err := ksuid.FromBytes(k[len(k)-ksuidLen():])
// 	if err != nil {
// 		panic(err)
// 	}
// 	return id
// }

// // Next returns the next RawMessageQueueKey after k. The prefix remains the same.
// func (k RawMessageQueueKey) Next() RawMessageQueueKey {
// 	return FromParts(k.Prefix(), k.KSUID().Next())
// }

// // Next returns the previous RawMessageQueueKey before k. The prefix remains the same.
// func (k RawMessageQueueKey) Prev() RawMessageQueueKey {
// 	return FromParts(k.Prefix(), k.KSUID().Prev())
// }

// func (k RawMessageQueueKey) Clone() RawMessageQueueKey {
// 	k2 := make([]byte, len(k))
// 	copy(k2[:], k[:])
// 	return k2
// }

// func (k RawMessageQueueKey) String() string {
// 	return string(k.Bytes())
// }
