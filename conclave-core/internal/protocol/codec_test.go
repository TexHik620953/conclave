package protocol

import (
	"bytes"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	c := Codec{}
	f := Frame{Type: TypeToolCall, ID: "m1", SessionID: "s1", Seq: 7}
	if err := EncodeData(&f, ToolCall{CallID: "c1", Name: "read_file", Args: []byte(`{"path":"a"}`)}); err != nil {
		t.Fatal(err)
	}
	b, err := c.Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.V != Version || got.Type != TypeToolCall || got.ID != "m1" || got.SessionID != "s1" || got.Seq != 7 {
		t.Fatalf("unexpected frame: %+v", got)
	}
	var tc ToolCall
	if err := DecodeData(got, &tc); err != nil {
		t.Fatal(err)
	}
	if tc.CallID != "c1" || tc.Name != "read_file" {
		t.Fatalf("unexpected tool call: %+v", tc)
	}
}

func TestDecodeRejectsFutureVersion(t *testing.T) {
	c := Codec{}
	if _, err := c.Decode([]byte(`{"v":99,"type":"ping"}`)); err == nil {
		t.Fatal("expected version rejection")
	}
}

func TestDecodeRequiresType(t *testing.T) {
	c := Codec{}
	if _, err := c.Decode([]byte(`{"v":1}`)); err == nil {
		t.Fatal("expected missing type error")
	}
}

func TestLengthPrefixedStream(t *testing.T) {
	c := Codec{}
	var buf bytes.Buffer
	frames := []Frame{
		{Type: TypePing, ID: "1"},
		{Type: TypePong, ID: "1", ReplyTo: "1"},
		{Type: TypeWelcome, Data: []byte(`{"protocol":1}`)},
	}
	for _, f := range frames {
		if err := WriteFrame(&buf, c, f); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range frames {
		got, err := ReadFrame(&buf, c)
		if err != nil {
			t.Fatal(err)
		}
		if got.Type != want.Type || got.ID != want.ID {
			t.Fatalf("got %+v want %+v", got, want)
		}
	}
}

func TestTrackerResume(t *testing.T) {
	tr := NewTracker()
	tr.Observe(Frame{SessionID: "s1", Seq: 3})
	tr.Observe(Frame{SessionID: "s1", Seq: 5})
	tr.Observe(Frame{SessionID: "s1", Seq: 4}) // out of order, ignored
	tr.Observe(Frame{SessionID: "s2", Seq: 1})
	if got := tr.Last("s1"); got != 5 {
		t.Fatalf("Last(s1) = %d, want 5", got)
	}
	f := tr.Resume("s1")
	if f.Type != TypeResume || f.SessionID != "s1" {
		t.Fatalf("unexpected resume frame: %+v", f)
	}
	var rr ResumeRequest
	if err := DecodeData(f, &rr); err != nil {
		t.Fatal(err)
	}
	if rr.FromSeq != 5 {
		t.Fatalf("FromSeq = %d, want 5", rr.FromSeq)
	}
}
