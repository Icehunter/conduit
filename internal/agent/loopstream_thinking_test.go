package agent

import "testing"

func TestDrainStream_ThinkingEmitsEventThinking(t *testing.T) {
	sse := sseMessage("end_turn", sseThinkingBlock(0, "pondering"), sseTextBlock("answer"))
	_, ev, err := drainOf(t, sse)
	if err != nil {
		t.Fatal(err)
	}
	th := ev.ofType(EventThinking)
	if len(th) != 1 || th[0].Text != "pondering" {
		t.Fatalf("EventThinking = %+v, want one with text %q", th, "pondering")
	}
	tx := ev.ofType(EventText)
	if len(tx) != 1 || tx[0].Text != "answer" {
		t.Fatalf("EventText = %+v, want only the answer", tx)
	}
}
