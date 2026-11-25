package mt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openloc/internal/ai"
)

func TestAperteraPodTranslate(t *testing.T) {
	var got []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.URL.Path != "/v2/" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		got = append(got, map[string]string{
			"credential": r.PostForm.Get("credential"), "from": r.PostForm.Get("from"),
			"to": r.PostForm.Get("to"), "input": r.PostForm.Get("input"),
			"ytqa": r.PostForm.Get("ytqa"), "ymts": r.PostForm.Get("ymts"),
		})
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"translation":{"status":"OK","error_msg":null,"translation":{"target_text":"T:` + r.PostForm.Get("input") + `"}}}`))
	}))
	defer srv.Close()

	e := AperteraPod{APIKey: " k\n", BaseURL: srv.URL}
	res, err := e.Translate(context.Background(), TranslateRequest{SourceLang: "en", TargetLang: "fr-ca", Texts: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Text != "T:a" || res[1].Text != "T:b" {
		t.Fatalf("results = %+v", res)
	}
	if g := got[0]; g["credential"] != "k" || g["from"] != "eng" || g["to"] != "fra" || g["ytqa"] != "true" || g["ymts"] != "true" {
		t.Errorf("request form = %+v", g)
	}
}

func TestAperteraPodErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := AperteraPod{APIKey: "k", BaseURL: srv.URL}.Translate(context.Background(),
		TranslateRequest{SourceLang: "en", TargetLang: "fr", Texts: []string{"a"}})
	pe, ok := err.(*ai.ProviderError)
	if !ok || pe.Kind != ai.ProviderErrAuth {
		t.Fatalf("err = %#v", err)
	}
	if _, err := (AperteraPod{APIKey: "k"}).Translate(context.Background(),
		TranslateRequest{SourceLang: "en", TargetLang: "xx", Texts: []string{"a"}}); err == nil {
		t.Error("unknown language should fail")
	}
	if _, err := NewFromDB("apertera-pod", "", "", nil); err == nil {
		t.Error("missing key should fail")
	}
}

func TestAperteraPodBase(t *testing.T) {
	for in, want := range map[string]string{
		"":                                     ai.AperteraPodDefaultURL,
		"https://ai.alexatranslations.com":     "https://ai.alexatranslations.com",
		"https://ai.alexatranslations.com/v2/": "https://ai.alexatranslations.com",
		"https://ai.alexatranslations.com/v2/translate/": "https://ai.alexatranslations.com",
		"http://gw.local/pod/v2":                         "http://gw.local/pod",
	} {
		if got := ai.AperteraPodBase(in); got != want {
			t.Errorf("AperteraPodBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAperteraPodBodyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"translation":{"status":"ERROR","error_msg":"bad pair"}}`))
	}))
	defer srv.Close()
	_, err := AperteraPod{APIKey: "k", BaseURL: srv.URL}.Translate(context.Background(),
		TranslateRequest{SourceLang: "en", TargetLang: "fr", Texts: []string{"a"}})
	if err == nil || !strings.Contains(err.Error(), "bad pair") {
		t.Fatalf("err = %v", err)
	}
}

func TestAperteraPodMultiLine(t *testing.T) {
	var inputs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		in := r.PostForm.Get("input")
		if strings.ContainsAny(in, "\r\n") {
			w.WriteHeader(http.StatusForbidden) // what the live service does
			return
		}
		inputs = append(inputs, in)
		w.Write([]byte(`{"translation":{"status":"OK","translation":{"target_text":"T:` + in + `"}}}`))
	}))
	defer srv.Close()
	res, err := AperteraPod{APIKey: "k", BaseURL: srv.URL}.Translate(context.Background(),
		TranslateRequest{SourceLang: "en", TargetLang: "fr", Texts: []string{"a\nb\r\n\nc"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "T:a\nT:b\r\n\nT:c"; res[0].Text != want {
		t.Errorf("got %q, want %q", res[0].Text, want)
	}
	if len(inputs) != 3 {
		t.Errorf("sent %d requests, want 3 (blank line not sent): %v", len(inputs), inputs)
	}
}

func TestAperteraPodConcurrentOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		in := r.PostForm.Get("input")
		if in == "t3" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"translation":{"status":"OK","translation":{"target_text":"T:` + in + `"}}}`))
	}))
	defer srv.Close()
	e := AperteraPod{APIKey: "k", BaseURL: srv.URL}
	var texts []string
	for i := 0; i < 20; i++ {
		if i != 3 {
			texts = append(texts, "t"+strings.Repeat("x", i))
		}
	}
	res, err := e.Translate(context.Background(), TranslateRequest{SourceLang: "en", TargetLang: "fr", Texts: texts})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range res {
		if r.Text != "T:"+texts[i] {
			t.Fatalf("result %d = %q, order broken", i, r.Text)
		}
	}
	// A failing text fails the whole call.
	if _, err := e.Translate(context.Background(), TranslateRequest{SourceLang: "en", TargetLang: "fr", Texts: []string{"a", "t3", "b"}}); err == nil {
		t.Error("expected error when one text fails")
	}
}
