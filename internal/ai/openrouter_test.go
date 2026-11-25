package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Shaped like OpenRouter's /api/v1/models: a `name` rather than Anthropic's
// `display_name`, and an architecture block saying what each model outputs.
const openRouterModelsJSON = `{"data":[
  {"id":"anthropic/claude-sonnet-4.5","name":"Anthropic: Claude Sonnet 4.5","created":1759161676,
   "architecture":{"modality":"text+image->text","input_modalities":["text","image"],"output_modalities":["text"]}},
  {"id":"meta-llama/llama-3.3-70b-instruct","name":"Meta: Llama 3.3 70B Instruct","created":1733506137,
   "architecture":{"modality":"text->text","input_modalities":["text"],"output_modalities":["text"]}},
  {"id":"google/gemini-2.5-flash-image","name":"Google: Gemini 2.5 Flash Image","created":1759870431,
   "architecture":{"modality":"text+image->image","input_modalities":["text","image"],"output_modalities":["image"]}}
]}`

// /v1/models on OpenRouter is public: it answers 200 to any key. The key check
// must use an endpoint that actually authenticates, or every pasted string
// would "connect".
func TestHTTPTesterOpenRouterChecksTheKeyEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path == "/api/v1/key" && r.Header.Get("Authorization") == "Bearer good" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	tester := NewHTTPTester()
	base := NormalizeBaseURL(ProviderOpenRouter, srv.URL+"/api/v1/")
	if err := tester.Test(context.Background(), ProviderOpenRouter, "good", base); err != nil {
		t.Fatalf("good key: got %v, want nil", err)
	}
	if gotPath != "/api/v1/key" {
		t.Errorf("path = %q, want /api/v1/key", gotPath)
	}
	if err := tester.Test(context.Background(), ProviderOpenRouter, "bad", base); err == nil {
		t.Error("bad key: got nil, want an error")
	}
}

func TestListOpenRouterModels(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(openRouterModelsJSON))
	}))
	defer srv.Close()

	got, err := NewHTTPModelLister().List(context.Background(), ProviderOpenRouter, "sk-or-test", srv.URL+"/api")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/models" || gotAuth != "Bearer sk-or-test" {
		t.Errorf("request = %s with %q, want /api/v1/models with a bearer token", gotPath, gotAuth)
	}
	models := classifyAll(ProviderOpenRouter, got)
	// The image generator is dropped; models from any vendor are kept.
	want := []string{"anthropic/claude-sonnet-4.5", "meta-llama/llama-3.3-70b-instruct"}
	if g := ids(models); strings.Join(g, ",") != strings.Join(want, ",") {
		t.Fatalf("classified = %v, want %v", g, want)
	}
	if models[0].Label != "Anthropic: Claude Sonnet 4.5" {
		t.Errorf("label = %q, want OpenRouter's own name", models[0].Label)
	}
	if p := ParamsFor(ProviderOpenRouter, models[1].ID); len(p) == 0 {
		t.Error("an OpenRouter model must offer the normalised parameters")
	}
}

// OpenRouter is an aggregator, so the newest-N display cap would hide most of
// what the key can reach. Other vendors keep the cap.
func TestCatalogDoesNotCapOpenRouter(t *testing.T) {
	var many []RemoteModel
	for i := 0; i < 3*DefaultModelLimit; i++ {
		many = append(many, RemoteModel{ID: fmt.Sprintf("vendor/model-%d", i)})
	}
	c, _ := newTestCatalog(map[string][]RemoteModel{ProviderOpenRouter: many, ProviderClaude: claudeFive}, 2)
	if got := c.Models(context.Background(), 1, ProviderOpenRouter); len(got) != len(many) {
		t.Errorf("OpenRouter models = %d, want all %d", len(got), len(many))
	}
	if got := c.Models(context.Background(), 1, ProviderClaude); len(got) != 2 {
		t.Errorf("Claude models = %d, want the cap of 2", len(got))
	}
}

// The workflow and MT paths gate on these before they call anything. A provider
// missing here is skipped at run time with only a log line.
func TestOpenRouterCanBackAnAIEngine(t *testing.T) {
	if !CanPredict(ProviderOpenRouter) || !CanMachineTranslate(ProviderOpenRouter) {
		t.Fatal("OpenRouter must be able to run QE+APE and AI translation")
	}
	if !ValidProvider(ProviderOpenRouter) || !HasModelList(ProviderOpenRouter) {
		t.Fatal("OpenRouter must be a known provider with a model list")
	}
}

// A translation through OpenRouter goes out in the OpenAI wire format, to the
// connection's endpoint, naming the "<vendor>/<model>" id, with the
// translation temperature default applied.
func TestTranslateBatch_OpenRouter(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-or-test" {
			http.Error(w, "wrong request", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, segs := decodeRequest(t, body)
		objects := make([]string, len(segs))
		for i, s := range segs {
			obj, _ := json.Marshal(map[string]string{"id": s.ID, "target": strings.ToUpper(s.text())})
			objects[i] = string(obj)
		}
		writeOpenAIContent(w, resultsReply(objects...))
	}))
	defer srv.Close()

	out, err := NewHTTPTranslator().TranslateBatch(context.Background(), TranslateConfig{
		Provider:   ProviderOpenRouter,
		Model:      "meta-llama/llama-3.3-70b-instruct",
		APIKey:     "sk-or-test",
		BaseURL:    srv.URL + "/api",
		PromptText: "translate faithfully",
		SourceLang: "en",
		TargetLang: "es",
	}, texts("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Text != "HELLO" {
		t.Fatalf("unexpected translations: %v", out)
	}
	if got["model"] != "meta-llama/llama-3.3-70b-instruct" {
		t.Errorf("model = %v, want the OpenRouter id", got["model"])
	}
	if got["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want the translation default 0", got["temperature"])
	}
	if _, ok := got["response_format"]; ok {
		t.Error("OpenRouter requests must not demand a JSON schema: not every routed model enforces one")
	}
}
