package chat

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Self-hosted models (the In-House LLM) sometimes write a tool call as text
// instead of a structured tool_call — e.g.
//
//	<suggest_replies> <reply>Yes</reply> <reply>No</reply> </suggest_replies>
//
// even though the server's tool calling works. Such text would show the user
// raw tags and lose the action. For those adapters, tags named after an
// offered tool are hidden from the streamed text and turned back into tool
// calls: suggest_replies from its <reply> items, any other tool when its body
// is a JSON object of arguments. A tag that can't be converted is still
// hidden.
//
// Gemma-family models also leak their reasoning channel as text when the
// server runs without a reasoning parser: "<|channel>thought …<channel|>".
// Those blocks are hidden the same way and never reach the answer.

// Reasoning-channel delimiters (Gemma 4 chat template).
const (
	channelOpen  = "<|channel>"
	channelClose = "<channel|>"
)

// tagSpec is one kind of block the filter withholds.
type tagSpec struct {
	open   string // literal start, e.g. "<suggest_replies" or "<|channel>"
	close  string
	needGT bool // open is a tag name: the tag ends at the next '>'
}

// textToolNudge is appended to the system prompt of a text-tool adapter.
const textToolNudge = "\n\nTool use: call tools only through the function-calling interface. " +
	"Never write a tool name, its arguments or XML-style tags such as <suggest_replies> in your reply text."

// toolTagFilter wraps a delta callback and withholds any <tool>…</tool> block
// whose name is an offered tool, including while it is still streaming.
type toolTagFilter struct {
	specs   []tagSpec
	out     func(string)
	pending string
	closing string // non-empty while inside a block: the closing tag awaited
}

func newToolTagFilter(tools []ToolDef, out func(string)) *toolTagFilter {
	specs := []tagSpec{{open: channelOpen, close: channelClose}}
	for _, t := range tools {
		specs = append(specs, tagSpec{open: "<" + t.Name, close: "</" + t.Name + ">", needGT: true})
	}
	return &toolTagFilter{specs: specs, out: out}
}

func (f *toolTagFilter) emit(s string) {
	if s != "" && f.out != nil {
		f.out(s)
	}
}

// Write takes one streamed delta.
func (f *toolTagFilter) Write(delta string) {
	f.pending += delta
	for {
		if f.closing != "" {
			i := strings.Index(f.pending, f.closing)
			if i < 0 {
				// Keep only what could still be the start of the closing tag.
				if n := len(f.pending) - len(f.closing); n > 0 {
					f.pending = f.pending[n:]
				}
				return
			}
			f.pending = f.pending[i+len(f.closing):]
			f.closing = ""
			continue
		}
		lt := strings.IndexByte(f.pending, '<')
		if lt < 0 {
			f.emit(f.pending)
			f.pending = ""
			return
		}
		f.emit(f.pending[:lt])
		f.pending = f.pending[lt:]
		spec, consumed, state := f.matchOpen(f.pending)
		switch state {
		case tagPartial:
			return // wait for more text
		case tagNone:
			f.emit("<")
			f.pending = f.pending[1:]
		case tagOpen:
			f.pending = f.pending[consumed:]
			f.closing = spec.close
		}
	}
}

// Flush ends the stream: an unfinished block stays hidden, a held-back "<…"
// that never became a tool tag is released.
func (f *toolTagFilter) Flush() {
	if f.closing == "" {
		f.emit(f.pending)
	}
	f.pending, f.closing = "", ""
}

type tagState int

const (
	tagNone tagState = iota
	tagPartial
	tagOpen
)

// matchOpen reports whether s (starting with "<") opens a withheld block:
// complete (consumed = length of the opening tag), possibly once more text
// arrives, or not.
func (f *toolTagFilter) matchOpen(s string) (tagSpec, int, tagState) {
	partial := false
	for _, sp := range f.specs {
		if len(s) < len(sp.open) {
			if strings.HasPrefix(sp.open, s) {
				partial = true
			}
			continue
		}
		if !strings.HasPrefix(s, sp.open) {
			continue
		}
		if !sp.needGT {
			return sp, len(sp.open), tagOpen
		}
		if len(s) == len(sp.open) {
			partial = true
			continue
		}
		c := s[len(sp.open)]
		if c != '>' && c != ' ' && c != '\n' && c != '\t' {
			continue // a longer name, e.g. <suggest_repliesX
		}
		end := strings.IndexByte(s, '>')
		if end < 0 {
			partial = true
			continue
		}
		return sp, end + 1, tagOpen
	}
	if partial {
		return tagSpec{}, 0, tagPartial
	}
	return tagSpec{}, 0, tagNone
}

var (
	replyItem           = regexp.MustCompile(`(?s)<reply>(.*?)</reply>`)
	channelBlock        = regexp.MustCompile(`(?s)` + regexp.QuoteMeta(channelOpen) + `.*?` + regexp.QuoteMeta(channelClose))
	channelUnterminated = regexp.MustCompile(`(?s)` + regexp.QuoteMeta(channelOpen) + `.*$`)
)

// extractTextToolCalls removes offered-tool tags from text and returns the
// calls they convert to. A call's ID is synthetic; the loop only echoes it.
func extractTextToolCalls(text string, tools []ToolDef) (string, []ToolCall) {
	text = channelBlock.ReplaceAllString(text, "")
	text = channelUnterminated.ReplaceAllString(text, "")
	var calls []ToolCall
	for _, t := range tools {
		name := regexp.QuoteMeta(t.Name)
		block := regexp.MustCompile(`(?s)<` + name + `(?:\s[^>]*)?>(.*?)</` + name + `>`)
		text = block.ReplaceAllStringFunc(text, func(m string) string {
			body := block.FindStringSubmatch(m)[1]
			if args, ok := textToolArgs(t.Name, body); ok {
				calls = append(calls, ToolCall{
					ID:   fmt.Sprintf("text-tool-%d", len(calls)+1),
					Name: t.Name,
					Args: args,
				})
			}
			return ""
		})
		// An unterminated block at the end (the turn was cut off).
		unterminated := regexp.MustCompile(`(?s)<` + name + `(?:\s[^>]*)?>.*$`)
		text = unterminated.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(text), calls
}

func textToolArgs(tool, body string) (json.RawMessage, bool) {
	body = strings.TrimSpace(body)
	if tool == repliesToolName {
		var replies []string
		for _, m := range replyItem.FindAllStringSubmatch(body, -1) {
			if r := strings.TrimSpace(m[1]); r != "" {
				replies = append(replies, r)
			}
		}
		if len(replies) > 0 {
			b, err := json.Marshal(map[string][]string{"replies": replies})
			return b, err == nil
		}
	}
	if strings.HasPrefix(body, "{") && json.Valid([]byte(body)) {
		return json.RawMessage(body), true
	}
	return nil, false
}
