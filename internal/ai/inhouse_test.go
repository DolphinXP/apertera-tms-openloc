package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Shaped like vLLM's /v1/models: ids are whatever the server was started
// with, one of them looking exactly like an OpenAI id.
const inHouseModelsJSON = `{"object":"list","data":[
  {"id":"Qwen/Qwen3-32B","object":"model","created":1763600000,"owned_by":"vllm"},
  {"id":"gpt-oss-120b","object":"model","created":1763500000,"owned_by":"vllm"}
]}`

func TestInHouseIsAnOpenAICompatibleLLM(t *testing.T) {
	if !ValidProvider(ProviderInHouse) {
		t.Fatal("inhouse must be a known provider")
	}
	if !SpeaksOpenAI(ProviderInHouse) || !CanMachineTranslate(ProviderInHouse) || !CanPredict(ProviderInHouse) || !HasModelList(ProviderInHouse) {
		t.Error("inhouse must drive the OpenAI request path, translate, predict and list models")
	}
	if IsMTEngineProvider(ProviderInHouse) {
		t.Error("inhouse is an LLM, not an MT engine")
	}
	if got := DisplayName(ProviderInHouse); got != "In-House LLM" {
		t.Errorf("DisplayName = %q", got)
	}
	if got := defaultBaseURL(ProviderInHouse); got != InHouseBaseURL {
		t.Errorf("defaultBaseURL = %q, want %q", got, InHouseBaseURL)
	}
}

// The endpoint is given as ".../v1"; callers append /v1/..., so the saved
// base must not keep it.
func TestInHouseBaseURLDropsV1(t *testing.T) {
	for _, in := range []string{"https://beta.slm.at-ai.systems/v1", "https://beta.slm.at-ai.systems/v1/", " https://beta.slm.at-ai.systems "} {
		if got := NormalizeBaseURL(ProviderInHouse, in); got != "https://beta.slm.at-ai.systems" {
			t.Errorf("NormalizeBaseURL(%q) = %q", in, got)
		}
	}
}

// The company server has no TLS: plain http is accepted for inhouse, outside
// development too, and still refused for every other vendor.
func TestInHouseAllowsPlainHTTP(t *testing.T) {
	t.Setenv("ENV", "production")
	if msg := ValidateBaseURLFor(ProviderInHouse, "http://184.107.246.129:8000"); msg != "" {
		t.Errorf("inhouse http: got %q, want valid", msg)
	}
	if ValidateBaseURLFor(ProviderOpenAI, "http://184.107.246.129:8000") == "" {
		t.Error("openai http: got valid, want rejected")
	}
	if ValidateBaseURL("http://184.107.246.129:8000") == "" {
		t.Error("provider-less check: got valid, want rejected")
	}
	if msg := ValidateBaseURLFor(ProviderInHouse, "http://184.107.246.129:8000?x=1"); msg == "" {
		t.Error("query string: got valid, want rejected")
	}
}

func TestHTTPTesterInHouseChecksModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" && r.Header.Get("Authorization") == "Bearer good" {
			_, _ = w.Write([]byte(inHouseModelsJSON))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	base := NormalizeBaseURL(ProviderInHouse, srv.URL+"/v1")
	tester := NewHTTPTester()
	if err := tester.Test(context.Background(), ProviderInHouse, "good", base); err != nil {
		t.Fatalf("good key: got %v, want nil", err)
	}
	if err := tester.Test(context.Background(), ProviderInHouse, "bad", base); err == nil {
		t.Error("bad key: got nil, want an error")
	}
}

func TestListInHouseModels(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(inHouseModelsJSON))
	}))
	defer srv.Close()

	got, err := NewHTTPModelLister().List(context.Background(), ProviderInHouse, "k", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/models" || gotAuth != "Bearer k" {
		t.Errorf("request = %s with %q, want /v1/models with a bearer token", gotPath, gotAuth)
	}
	// Every served id is listed, whatever its shape.
	models := classifyAll(ProviderInHouse, got)
	if g := ids(models); strings.Join(g, ",") != "Qwen/Qwen3-32B,gpt-oss-120b" {
		t.Fatalf("classified = %v", g)
	}
	if p := ParamsFor(ProviderInHouse, models[0].ID); len(p) == 0 {
		t.Error("an inhouse model must offer the sampling parameters")
	}
}
