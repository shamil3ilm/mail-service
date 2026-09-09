package events

import (
	"sync"
	"testing"
	"time"
)

func TestPublishFanout(t *testing.T) {
	b := NewMemory(nil)
	defer b.Close()

	a, unA := b.Subscribe()
	c, unC := b.Subscribe()
	defer unA()
	defer unC()

	b.Publish(Event{Type: MessageReceived, MessageID: "m1"})

	for _, ch := range []<-chan Event{a, c} {
		select {
		case evt := <-ch:
			if evt.MessageID != "m1" {
				t.Fatalf("got %+v", evt)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("no event received")
		}
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	b := NewMemory(nil)
	defer b.Close()

	ch, unsub := b.Subscribe()
	unsub()

	b.Publish(Event{Type: MessageReceived, MessageID: "x"})

	// Channel is closed by unsubscribe — receive should return zero value.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected closed channel to yield !ok")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("channel not closed after unsubscribe")
	}
}

func TestSlowSubscriberDropsInsteadOfBlocking(t *testing.T) {
	b := NewMemory(nil)
	defer b.Close()

	_, unsub := b.Subscribe()
	defer unsub()

	// Flood past the buffer. If Publish were blocking, this would deadlock.
	done := make(chan struct{})
	go func() {
		for i := 0; i < subBufferSize*4; i++ {
			b.Publish(Event{Type: MessageReceived})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publisher blocked — subscriber back-pressured ingestion")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	b := NewMemory(nil)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); b.Close() }()
	}
	wg.Wait()
	b.Publish(Event{Type: MessageReceived}) // no panic after close
}
