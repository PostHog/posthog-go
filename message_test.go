package posthog

import (
	"reflect"
	"testing"
)

func TestMessageQueuePushMaxBatchSize(t *testing.T) {
	m0, _ := makeMessage(Capture{
		DistinctId: "1",
		Event:      "A",
	}, maxMessageBytes)

	m1, _ := makeMessage(Capture{
		DistinctId: "2",
		Event:      "A",
	}, maxMessageBytes)

	q := messageQueue{
		maxBatchSize:  2,
		maxBatchBytes: maxBatchBytes,
	}

	if msgs := q.push(m0); msgs != nil {
		t.Error("unexpected message batch returned after pushing only one message")
	}

	if msgs := q.push(m1); !reflect.DeepEqual(msgs, []message{m0, m1}) {
		t.Error("invalid message batch returned after pushing two messages:", msgs)
	}
}

func TestMessageQueuePushMaxBatchBytes(t *testing.T) {
	m0, _ := makeMessage(Capture{
		DistinctId: "1",
		Event:      "A",
	}, maxMessageBytes)

	m1, _ := makeMessage(Capture{
		DistinctId: "2",
		Event:      "A",
	}, maxMessageBytes)

	q := messageQueue{
		maxBatchSize:  100,
		maxBatchBytes: len(m0.json) + 1,
	}

	if msgs := q.push(m0); msgs != nil {
		t.Error("unexpected message batch returned after pushing only one message")
	}

	if msgs := q.push(m1); !reflect.DeepEqual(msgs, []message{m0}) {
		t.Error("invalid message batch returned after pushing two messages:", msgs)
	}

	if !reflect.DeepEqual(q.pending, []message{m1}) {
		t.Error("invalid state of the message queue after pushing two messages:", q.pending)
	}
}

func TestMakeMessageTooBig(t *testing.T) {
	if _, err := makeMessage(Capture{DistinctId: "1"}, 1); err != ErrMessageTooBig {
		t.Error("invalid error returned when creating a message bigger than the limit:", err)
	}
}
