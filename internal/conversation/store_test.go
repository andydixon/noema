package conversation

import (
	"context"
	"testing"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/ids"
)

func TestStoreScopingAndReplies(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	ms := minds.NewStore(pool)
	a, _ := ms.Create(ctx, minds.CreateParams{Slug: "a"})
	b, _ := ms.Create(ctx, minds.CreateParams{Slug: "b"})
	s := NewStore(pool)
	c, err := s.Create(ctx, a.ID, "", "web", "andy", "noema")
	if err != nil {
		t.Fatal(err)
	}
	um, err := s.AddMessage(ctx, pool, Message{ConversationID: c.ID, MindID: a.ID, Role: "user", Actor: "andy", Content: "Is atlas slow?"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(ctx, pool, Message{ConversationID: c.ID, MindID: b.ID, Role: "user", Content: "leak?"}); err == nil {
		t.Log("cross-mind insert silently ignored (no row)")
	}
	msgsB, _ := s.Messages(ctx, b.ID, c.ID, 10)
	if len(msgsB) != 0 {
		t.Fatal("cross-mind message visible")
	}
	if _, err := s.Get(ctx, b.ID, c.ID); err == nil {
		t.Fatal("cross-mind conversation read")
	}
	rm, _ := s.AddMessage(ctx, pool, Message{ConversationID: c.ID, MindID: a.ID, Role: "mind", Actor: "a", Content: "Somewhat.", ReplyTo: um.ID, LLMCallID: ids.New()})
	got, err := s.Reply(ctx, a.ID, um.ID)
	if err != nil || got.ID != rm.ID {
		t.Fatalf("reply lookup: %v %+v", err, got)
	}
	cv, _ := s.Get(ctx, a.ID, c.ID)
	if cv.Title != "Is atlas slow?" {
		t.Fatalf("title not derived: %q", cv.Title)
	}
	msgs, _ := s.Messages(ctx, a.ID, c.ID, 10)
	if len(msgs) != 2 || msgs[0].Role != "user" {
		t.Fatalf("ordering wrong: %+v", msgs)
	}
}
