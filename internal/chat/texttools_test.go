package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var textTestTools = []ToolDef{{Name: "suggest_replies"}, {Name: "navigate_to"}}

// The tags never reach the user, however the stream splits them.
func TestToolTagFilterHidesTagsAcrossDeltas(t *testing.T) {
	full := "你好！有什么可以帮你的吗？\n\n<suggest_replies> <reply>带我参观一下</reply> <reply>检查逾期任务</reply> </suggest_replies>\n a < b"
	for _, size := range []int{1, 2, 3, 7, 1000} {
		var got strings.Builder
		f := newToolTagFilter(textTestTools, func(s string) { got.WriteString(s) })
		for i := 0; i < len(full); i += size {
			f.Write(full[i:min(i+size, len(full))])
		}
		f.Flush()
		want := "你好！有什么可以帮你的吗？\n\n\n a < b"
		if got.String() != want {
			t.Errorf("chunk %d: got %q, want %q", size, got.String(), want)
		}
	}
}

func TestToolTagFilterKeepsOtherTags(t *testing.T) {
	var got strings.Builder
	f := newToolTagFilter(textTestTools, func(s string) { got.WriteString(s) })
	for _, d := range []string{"use <b>bold</b> and <sugg", "estion> here <navigate_toX>"} {
		f.Write(d)
	}
	f.Flush()
	if want := "use <b>bold</b> and <suggestion> here <navigate_toX>"; got.String() != want {
		t.Errorf("got %q, want %q", got.String(), want)
	}
}

func TestToolTagFilterHidesUnfinishedBlock(t *testing.T) {
	var got strings.Builder
	f := newToolTagFilter(textTestTools, func(s string) { got.WriteString(s) })
	f.Write("Hi <suggest_replies><reply>Ye")
	f.Flush()
	if got.String() != "Hi " {
		t.Errorf("got %q", got.String())
	}
}

// Gemma's reasoning channel leaks as text without a reasoning parser.
func TestToolTagFilterHidesReasoningChannel(t *testing.T) {
	full := "<|channel>thought\nThe user greets me.<channel|>你好！<|channel>thought <channel|>"
	for _, size := range []int{1, 3, 1000} {
		var got strings.Builder
		f := newToolTagFilter(textTestTools, func(s string) { got.WriteString(s) })
		for i := 0; i < len(full); i += size {
			f.Write(full[i:min(i+size, len(full))])
		}
		f.Flush()
		if got.String() != "你好！" {
			t.Errorf("chunk %d: got %q", size, got.String())
		}
	}
	clean, _ := extractTextToolCalls(full+"<|channel>thought cut", textTestTools)
	if clean != "你好！" {
		t.Errorf("extract: got %q", clean)
	}
}

func TestExtractTextToolCalls(t *testing.T) {
	text := "Pick one.\n<suggest_replies> <reply>Yes</reply> <reply> No </reply> </suggest_replies>\n" +
		`<navigate_to>{"page":"projects"}</navigate_to><navigate_to>not json</navigate_to>`
	clean, calls := extractTextToolCalls(text, textTestTools)
	if clean != "Pick one." {
		t.Errorf("clean = %q", clean)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want 2", calls)
	}
	if calls[0].Name != "suggest_replies" || string(calls[0].Args) != `{"replies":["Yes","No"]}` {
		t.Errorf("replies call = %s %s", calls[0].Name, calls[0].Args)
	}
	if calls[1].Name != "navigate_to" || string(calls[1].Args) != `{"page":"projects"}` {
		t.Errorf("navigate call = %s %s", calls[1].Name, calls[1].Args)
	}
	if calls[0].ID == calls[1].ID {
		t.Error("synthetic ids must differ")
	}
}

// End to end through the adapter: the streamed text has no tags, the turn
// carries the converted call, and the system prompt carries the nudge.
func TestInHouseAdapterConvertsTextToolCalls(t *testing.T) {
	var gotSystem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotSystem = body.Messages[0].Content
		w.Header().Set("Content-Type", "text/event-stream")
		for _, d := range []string{"Hello!", " <suggest_", "replies><reply>Tour</reply></suggest_replies>"} {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": d}}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		}
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	var streamed strings.Builder
	llm := NewInHouse("k", srv.URL, "/slm-models/gemma-4-31b/")
	turn, err := llm.Stream(context.Background(), Request{System: "sys", Tools: textTestTools,
		Messages: []Message{{Role: "user", Text: "hi"}}}, func(s string) { streamed.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(streamed.String(), "<") {
		t.Errorf("streamed %q, want no tags", streamed.String())
	}
	if turn.Text != "Hello!" || turn.StopReason != StopToolUse || len(turn.ToolCalls) != 1 ||
		string(turn.ToolCalls[0].Args) != `{"replies":["Tour"]}` {
		t.Errorf("turn = %+v", turn)
	}
	if !strings.HasPrefix(gotSystem, "sys") || !strings.Contains(gotSystem, "function-calling") {
		t.Errorf("system = %q, want the nudge appended", gotSystem)
	}
}
