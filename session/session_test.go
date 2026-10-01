package session

import (
	"testing"

	"github.com/tobyjackson/pig/ai"
)

func TestTreeBranchAndReload(t *testing.T) {
	root := t.TempDir()
	s := New(root, "/work/dir")
	u1 := s.AppendMessage(ai.UserMessage("one"))
	s.AppendMessage(ai.Message{Role: "assistant", Content: []ai.Content{ai.Text("reply one")}, StopReason: "stop"})
	s.AppendMessage(ai.UserMessage("two"))
	if len(s.BuildContext().Messages) != 3 {
		t.Fatal("expected 3 messages")
	}
	if err := s.Branch(u1); err != nil {
		t.Fatal(err)
	}
	s.AppendMessage(ai.Message{Role: "assistant", Content: []ai.Content{ai.Text("other reply")}, StopReason: "stop"})
	ctx := s.BuildContext()
	if len(ctx.Messages) != 2 || ctx.Messages[1].TextContent() != "other reply" {
		t.Fatalf("branch wrong: %+v", ctx.Messages)
	}
	if len(s.Entries()) != 4 {
		t.Fatal("old branch should stay in the file")
	}
	re, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if re.ID() != s.ID() || len(re.Entries()) != 4 || re.LeafID() != s.LeafID() {
		t.Fatalf("reload mismatch: %d entries", len(re.Entries()))
	}
}

func TestCompactionContext(t *testing.T) {
	s := New("", "/x")
	s.AppendMessage(ai.UserMessage("old 1"))
	s.AppendMessage(ai.Message{Role: "assistant", Content: []ai.Content{ai.Text("r1")}})
	keep := s.AppendMessage(ai.UserMessage("recent"))
	s.AppendMessage(ai.Message{Role: "assistant", Content: []ai.Content{ai.Text("r2")}})
	s.AppendCompaction("SUMMARY", keep, 1000, nil)
	s.AppendMessage(ai.UserMessage("after"))
	ctx := s.BuildContext()
	if ctx.Summary != "SUMMARY" {
		t.Fatal("summary missing")
	}
	if len(ctx.Messages) != 3 || ctx.Messages[0].TextContent() != "recent" {
		t.Fatalf("kept wrong: %d", len(ctx.Messages))
	}
	if s.Path() != "" {
		t.Fatal("ephemeral session must have no path")
	}
}

func TestListAndFind(t *testing.T) {
	root := t.TempDir()
	s := New(root, "/proj")
	s.AppendMessage(ai.UserMessage("hello there"))
	s.AppendName("my session")
	list := List(root, "/proj")
	if len(list) != 1 || list[0].Name != "my session" || list[0].FirstUser != "hello there" {
		t.Fatalf("got %+v", list)
	}
	if p, ok := FindByID(root, "/proj", s.ID()[:6]); !ok || p != s.Path() {
		t.Fatal("partial id lookup failed")
	}
}

// The TUI reads the store on its goroutine while the agent's OnMessage hook
// appends to it, which used to fault on the shared map. Run with -race.
func TestConcurrentAppendAndRead(t *testing.T) {
	s := New("", "/x")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			s.AppendMessage(ai.UserMessage("hi"))
		}
	}()
	for i := 0; i < 500; i++ {
		s.Name()
		s.BranchEntries()
		s.Entries()
	}
	<-done
}

func TestClone(t *testing.T) {
	root := t.TempDir()
	s := New(root, "/p")
	a := s.AppendMessage(ai.UserMessage("a"))
	s.AppendMessage(ai.UserMessage("b"))
	c := s.Clone(root, a)
	if len(c.Messages()) != 1 || c.Header.ParentSession != s.Path() {
		t.Fatalf("clone wrong: %d", len(c.Messages()))
	}
}
