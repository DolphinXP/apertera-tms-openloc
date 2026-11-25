package mt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"

	"openloc/internal/ai"
	"openloc/internal/langcode"
)

// AperteraPod calls Apertera's custom MT service (POST /v2/), a
// form-encoded API that answers {"translation": {"translation": {"target_text": ...}}}.
type AperteraPod struct {
	APIKey string
	// BaseURL reaches the service through a different host, gateway or proxy;
	// empty uses the public endpoint. Users set it on the AI settings page.
	BaseURL string
}

func (a AperteraPod) Name() string { return ai.ProviderAperteraPod }

func (a AperteraPod) resolveBaseURL() string {
	return ai.AperteraPodBase(a.BaseURL)
}

// Translate sends one request per text. The service's reply for several
// `input` fields is not documented (the Postman request only enables one), so a batch is not relied on: a count or ordering surprise
// would silently misalign translations.
func (a AperteraPod) Translate(ctx context.Context, req TranslateRequest) ([]TranslateResult, error) {
	// A pasted key often carries a stray space or newline; the service answers
	// 403 for it, and the settings page only shows the key's last four characters.
	a.APIKey = strings.TrimSpace(a.APIKey)
	if a.APIKey == "" {
		return nil, fmt.Errorf("Apertera POD API key not configured")
	}
	if len(req.Texts) == 0 {
		return nil, nil
	}
	from, err := aperteraPodLang(req.SourceLang)
	if err != nil {
		return nil, err
	}
	to, err := aperteraPodLang(req.TargetLang)
	if err != nil {
		return nil, err
	}

	baseURL := a.resolveBaseURL()
	results := make([]TranslateResult, len(req.Texts))

	// One request per text (see above), run a few at a time: the service takes
	// ~2s per call, so a sequential chunk of 30 would take a minute. Results
	// land by index, so order is kept; the first failure cancels the rest.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		sem      = make(chan struct{}, aperteraPodConcurrency)
	)
	for i, text := range req.Texts {
		sem <- struct{}{}
		if ctx.Err() != nil {
			<-sem
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out, err := a.translateLines(ctx, baseURL, from, to, text)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				mu.Unlock()
				return
			}
			results[i] = TranslateResult{Text: out}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

// aperteraPodConcurrency bounds in-flight requests per Translate call. Set to
// 18 on request; the service was only load-tested here at 4 and 6, so if it
// starts answering 403/429, lower this first.
const aperteraPodConcurrency = 32

// translateLines translates a segment one line at a time. The service answers
// 403 for any input containing a line break (verified against the live API), so
// a multi-line segment would fail its whole chunk. Line breaks, blank lines and
// CRLF endings are put back exactly as they were; a blank line is not sent.
func (a AperteraPod) translateLines(ctx context.Context, baseURL, from, to, text string) (string, error) {
	if !strings.ContainsAny(text, "\r\n") {
		return a.translateOne(ctx, baseURL, from, to, text)
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		body := strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(body) == "" {
			continue
		}
		out, err := a.translateOne(ctx, baseURL, from, to, body)
		if err != nil {
			return "", err
		}
		lines[i] = out + line[len(body):]
	}
	return strings.Join(lines, "\n"), nil
}

func (a AperteraPod) translateOne(ctx context.Context, baseURL, from, to, text string) (string, error) {
	// ytqa and ymts are sent as the service's own Postman collection does.
	form := neturl.Values{
		"credential": {a.APIKey},
		"from":       {from},
		"to":         {to},
		"ytqa":       {"true"},
		"ymts":       {"true"},
		"input":      {text},
	}
	// The credential rides the body, not the URL, so a transport error (which
	// quotes the URL) cannot leak it.
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/v2/", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("apertera-pod: translate endpoint %q is not a valid URL: %w", baseURL, scrubURLError(err))
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := HTTPClient.Do(httpReq)
	if err != nil {
		return "", transportError(ctx, ai.ProviderAperteraPod, scrubURLError(err))
	}
	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return "", fmt.Errorf("apertera-pod: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", vendorError(ai.ProviderAperteraPod, resp.StatusCode, string(respBody))
	}

	// {"translation": {"status": "OK", "error_msg": null,
	//   "translation": {"source_text": "...", "target_text": "..."}}}
	var parsed struct {
		Translation struct {
			Status   string  `json:"status"`
			ErrorMsg *string `json:"error_msg"`
			Result   struct {
				TargetText *string `json:"target_text"`
			} `json:"translation"`
		} `json:"translation"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("apertera-pod: unmarshal response: %w", err)
	}
	t := parsed.Translation
	if t.Status != "OK" {
		detail := "status " + t.Status
		if t.ErrorMsg != nil {
			detail = *t.ErrorMsg
		}
		return "", &ai.ProviderError{Provider: ai.ProviderAperteraPod, Status: resp.StatusCode, Kind: ai.ProviderErrProvider, Detail: detail}
	}
	if t.Result.TargetText == nil {
		return "", fmt.Errorf("apertera-pod: response has no target_text")
	}
	return *t.Result.TargetText, nil
}

// aperteraPodLangs maps a base language code to the ISO 639-2 three-letter
// code the service takes ("eng", "fra", "spa").
var aperteraPodLangs = map[string]string{
	"af": "afr", "am": "amh", "ar": "ara", "az": "aze", "be": "bel", "bg": "bul",
	"bn": "ben", "bs": "bos", "ca": "cat", "cs": "ces", "cy": "cym", "da": "dan",
	"de": "deu", "el": "ell", "en": "eng", "es": "spa", "et": "est", "eu": "eus",
	"fa": "fas", "fi": "fin", "fr": "fra", "ga": "gle", "gl": "glg", "gu": "guj",
	"he": "heb", "hi": "hin", "hr": "hrv", "hu": "hun", "hy": "hye", "id": "ind",
	"is": "isl", "it": "ita", "ja": "jpn", "ka": "kat", "kk": "kaz", "km": "khm",
	"kn": "kan", "ko": "kor", "lo": "lao", "lt": "lit", "lv": "lav", "mk": "mkd",
	"ml": "mal", "mn": "mon", "mr": "mar", "ms": "msa", "mt": "mlt", "my": "mya",
	"nb": "nob", "ne": "nep", "nl": "nld", "no": "nor", "pa": "pan", "pl": "pol",
	"ps": "pus", "pt": "por", "ro": "ron", "ru": "rus", "si": "sin", "sk": "slk",
	"sl": "slv", "so": "som", "sq": "sqi", "sr": "srp", "sv": "swe", "sw": "swa",
	"ta": "tam", "te": "tel", "th": "tha", "tl": "tgl", "tr": "tur", "uk": "ukr",
	"ur": "urd", "uz": "uzb", "vi": "vie", "zh": "zho",
}

// aperteraPodLang converts one of our normalized codes ("fr", "pt-br") to the
// service's code. Regional variants map through their base language: the
// service names languages, not locales.
func aperteraPodLang(code string) (string, error) {
	base := langcode.BaseLang(langcode.Normalize(code))
	// A three-letter code already in the service's vocabulary passes through.
	for _, v := range aperteraPodLangs {
		if v == base {
			return v, nil
		}
	}
	if v, ok := aperteraPodLangs[base]; ok {
		return v, nil
	}
	return "", fmt.Errorf("apertera-pod: language %q is not supported", code)
}
