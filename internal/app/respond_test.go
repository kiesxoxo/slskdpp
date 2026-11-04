package app

import (
	"fmt"
	"testing"
)

func TestResponderLimits(t *testing.T) {
	r := NewResponder(nil, nil, nil)
	for range maxRepliesPerUser {
		if !r.begin("bob") {
			t.Fatal("bob refused below his limit")
		}
	}
	if r.begin("bob") {
		t.Error("bob got more than his share")
	}
	// Other names fill the rest, however many there are.
	for i := 0; r.total < maxReplies; i++ {
		if !r.begin(fmt.Sprint("user", i)) {
			t.Fatalf("refused at %d of %d", r.total, maxReplies)
		}
	}
	if r.begin("carol") {
		t.Error("carol got a place beyond the total")
	}
	r.end("bob")
	if !r.begin("bob") {
		t.Error("bob refused after a reply ended")
	}
}
