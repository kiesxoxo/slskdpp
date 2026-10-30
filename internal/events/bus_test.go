package events

import "testing"

func TestBus(t *testing.T) {
	b := NewBus()
	all := b.Subscribe(8, nil)
	only7 := b.Subscribe(8, func(e Event) bool { return e.Search == 7 })

	b.Publish(Event{Type: TypeServerState})
	b.Publish(Event{Type: TypeSearchHit, Search: 7})
	b.Publish(Event{Type: TypeSearchHit, Search: 8})

	var ids []uint64
	for range 3 {
		ids = append(ids, (<-all.C).ID)
	}
	if ids[0] != 1 || ids[1] != 2 || ids[2] != 3 {
		t.Errorf("ids %v, want 1 2 3", ids)
	}
	if e := <-only7.C; e.Search != 7 {
		t.Errorf("filtered sub got %+v", e)
	}
	select {
	case e := <-only7.C:
		t.Errorf("filtered sub got extra %+v", e)
	default:
	}

	b.Unsubscribe(all)
	b.Unsubscribe(all) // idempotent
	if _, ok := <-all.C; ok {
		t.Error("C not closed after Unsubscribe")
	}
}

func TestBusDropsSlowSubscriber(t *testing.T) {
	b := NewBus()
	slow := b.Subscribe(1, nil)
	b.Publish(Event{Type: "a"})
	b.Publish(Event{Type: "b"}) // overflows
	if e := <-slow.C; e.Type != "a" {
		t.Errorf("first event %+v", e)
	}
	if _, ok := <-slow.C; ok {
		t.Error("slow subscriber not dropped")
	}
	b.Unsubscribe(slow) // safe after the bus dropped it
}
