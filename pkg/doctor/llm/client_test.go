package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSendsToolsAndParsesToolCalls(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
          "choices":[{"message":{
            "role":"assistant",
            "content":null,
            "tool_calls":[{"id":"call_1","type":"function","function":{"name":"ceph_status","arguments":"{}"}}]
          }}],
          "usage":{"prompt_tokens":100,"completion_tokens":20}
        }`))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "test-key", Model: "gpt-4o"})
	reply, err := c.Chat(context.Background(),
		[]Message{{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "hi"}},
		[]ToolSpec{{Name: "ceph_status", Description: "d", Schema: json.RawMessage(`{"type":"object"}`)}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d", len(reply.ToolCalls))
	}
	if reply.ToolCalls[0].Name != "ceph_status" {
		t.Errorf("name = %q", reply.ToolCalls[0].Name)
	}
	if reply.ToolCalls[0].ID != "call_1" {
		t.Errorf("id = %q", reply.ToolCalls[0].ID)
	}
	if reply.Usage.PromptTokens != 100 {
		t.Errorf("usage = %+v", reply.Usage)
	}

	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools not sent: %+v", gotBody["tools"])
	}
	if gotBody["model"] != "gpt-4o" {
		t.Errorf("model = %v", gotBody["model"])
	}
}

func TestClientParsesTextReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"结论是X"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	reply, err := c.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Content != "结论是X" {
		t.Errorf("content = %q", reply.Content)
	}
	if len(reply.ToolCalls) != 0 {
		t.Errorf("expected no tool calls")
	}
}

func TestClientReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxRetries: 0})
	_, err := c.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestClientRetriesOn500(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(500)
			w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxRetries: 3})
	c.retryDelay = 0
	reply, err := c.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Content != "ok" {
		t.Errorf("content = %q", reply.Content)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestToolResultMessageShape(t *testing.T) {
	m := ToolResultMessage("call_1", `{"summary":"ok"}`)
	if m.Role != RoleTool {
		t.Errorf("role = %q", m.Role)
	}
	if m.ToolCallID != "call_1" {
		t.Errorf("tool_call_id = %q", m.ToolCallID)
	}
}

func TestClientDoesNotRetryOn400(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxRetries: 3})
	c.retryDelay = 0
	if _, err := c.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil); err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Errorf("400 must not retry, attempts = %d", attempts)
	}
}

func TestClientSendsAssistantToolCallsAndToolResults(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	msgs := []Message{
		{Role: RoleUser, Content: "go"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "ceph_status", Args: "{}"}}},
		ToolResultMessage("call_1", `{"summary":"ok"}`),
	}
	if _, err := c.Chat(context.Background(), msgs, nil); err != nil {
		t.Fatal(err)
	}
	wireMsgs := gotBody["messages"].([]any)
	if len(wireMsgs) != 3 {
		t.Fatalf("messages = %d", len(wireMsgs))
	}
	asst := wireMsgs[1].(map[string]any)
	tcs, ok := asst["tool_calls"].([]any)
	if !ok || len(tcs) != 1 {
		t.Fatalf("assistant tool_calls missing: %+v", asst)
	}
	tc := tcs[0].(map[string]any)
	fn := tc["function"].(map[string]any)
	if fn["name"] != "ceph_status" || fn["arguments"] != "{}" {
		t.Errorf("function = %+v", fn)
	}
	if tc["type"] != "function" {
		t.Errorf("type = %v", tc["type"])
	}
	toolMsg := wireMsgs[2].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_1" {
		t.Errorf("tool msg = %+v", toolMsg)
	}
}

func TestClientOmitsToolsWhenNone(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	if _, err := c.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, present := gotBody["tools"]; present {
		t.Errorf("tools should be omitted when empty, got %v", gotBody["tools"])
	}
}

func TestClientRespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Chat(ctx, []Message{{Role: RoleUser, Content: "hi"}}, nil); err == nil {
		t.Fatal("expected context error")
	}
}
