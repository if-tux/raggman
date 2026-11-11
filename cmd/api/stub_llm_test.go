package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
)

// stubLLM stands in for an OpenAI-compatible API (Ollama's /v1 by default).
func stubLLM(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/embeddings":
		stubEmbeddings(w, r)
	case "/v1/chat/completions":
		stubChat(w, r)
	default:
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
	}
}

// stubVector is a bag-of-words embedding: each word adds to one dimension, so
// cosine similarity follows the words a Question and a Chunk share.
func stubVector(text string) []float32 {
	vec := make([]float32, environment.EmbeddingDims)
	vec[0] = 0.01 // never the zero vector, whose cosine distance is undefined
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, word := range words {
		h := fnv.New32a()
		h.Write([]byte(word))
		vec[1+h.Sum32()%uint32(environment.EmbeddingDims-1)]++
	}
	return vec
}

func stubEmbeddings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "unexpected request", http.StatusBadRequest)
		return
	}
	if req.Model != "qwen3-embedding:0.6b" {
		http.Error(w, "unknown model "+req.Model, http.StatusNotFound)
		return
	}
	// Markers in the text let tests simulate an outage or a slow Ollama.
	for _, text := range req.Input {
		if strings.Contains(text, "embedding-outage") {
			http.Error(w, "model unavailable", http.StatusServiceUnavailable)
			return
		}
		if strings.Contains(text, "slow-embedding") {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}

	type item struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	}
	var resp struct {
		Data []item `json:"data"`
	}
	for i, text := range req.Input {
		resp.Data = append(resp.Data, item{Index: i, Embedding: stubVector(text)})
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// stubCandidates is what the stub model knows how to find in a passage. It
// gets a person's rendering wrong on purpose: Raggman must keep the source
// spelling whatever the model says.
var stubCandidates = []map[string]string{
	{"source": "Mara", "type": "person", "rendering": "Mára"},
	{"source": "Port Varen", "type": "place", "rendering": "Porto Varen"},
	{"source": "glimmerstone", "type": "term", "rendering": "pedra-lume"},
}

// extractionCalls counts Termbase extraction calls per passage, so a passage
// can fail a set number of times.
var extractionCalls sync.Map

// stubExtraction answers a Termbase extraction prompt with the known
// candidates the passage holds, plus one it does not: a model inventing a
// name. Markers in the passage simulate an outage or unreadable replies.
func stubExtraction(w http.ResponseWriter, system, passage string) {
	if !strings.Contains(system, "Brazilian Portuguese (pt-BR)") {
		http.Error(w, "extraction prompt does not name the target language", http.StatusBadRequest)
		return
	}
	if strings.Contains(passage, "⟦") {
		http.Error(w, "passage still carries inline markers", http.StatusBadRequest)
		return
	}
	if strings.Contains(passage, "extraction-outage") {
		http.Error(w, "model overloaded", http.StatusServiceUnavailable)
		return
	}
	counter, _ := extractionCalls.LoadOrStore(passage, new(atomic.Int64))
	calls := counter.(*atomic.Int64).Add(1)

	terms := []map[string]string{{"source": "Zorblat", "type": "person", "rendering": "Zorblat"}}
	for _, c := range stubCandidates {
		if strings.Contains(passage, c["source"]) {
			terms = append(terms, c)
		}
	}
	list, _ := json.Marshal(map[string]any{"terms": terms})
	reply := "```json\n" + string(list) + "\n```"
	if strings.Contains(passage, "always-unreadable") || (strings.Contains(passage, "unreadable-twice") && calls <= 2) {
		reply = "Sure! Here are the names I found: Mara."
	}

	w.Header().Set("Content-Type", "text/event-stream")
	chunk, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]string{"content": reply}}}})
	fmt.Fprintf(w, "data: %s\n\n", chunk)
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// translationRequest is one call the stub translator received.
type translationRequest struct{ system, user string }

var (
	translationRequestsMu sync.Mutex
	translationRequests   []translationRequest
	// translationCalls counts calls per set of blocks, so a set can be
	// answered badly a set number of times.
	translationCalls sync.Map
)

// translationRequestsWith returns the stub translator's calls whose blocks to
// translate contain text.
func translationRequestsWith(text string) []translationRequest {
	translationRequestsMu.Lock()
	defer translationRequestsMu.Unlock()
	var found []translationRequest
	for _, req := range translationRequests {
		if _, blocks, _ := strings.Cut(req.user, "Translate these blocks:"); strings.Contains(blocks, text) {
			found = append(found, req)
		}
	}
	return found
}

var stubBlock = regexp.MustCompile(`(?s)<block (\d+)>\n(.*?)\n</block>`)

// stubTranslation "translates" each numbered block by putting "pt:" before
// it, which keeps names and inline markers as they were. Markers in the
// blocks simulate an outage, a slow model, or a reply that drops a block.
func stubTranslation(w http.ResponseWriter, r *http.Request, system, user string) {
	translationRequestsMu.Lock()
	translationRequests = append(translationRequests, translationRequest{system: system, user: user})
	translationRequestsMu.Unlock()

	_, blocks, ok := strings.Cut(user, "Translate these blocks:")
	if !ok {
		http.Error(w, "translation prompt carries no blocks", http.StatusBadRequest)
		return
	}
	if strings.Contains(blocks, "translation-outage") {
		http.Error(w, "model overloaded", http.StatusServiceUnavailable)
		return
	}
	if strings.Contains(blocks, "slow-translation") {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
	counter, _ := translationCalls.LoadOrStore(blocks, new(atomic.Int64))
	calls := counter.(*atomic.Int64).Add(1)

	found := stubBlock.FindAllStringSubmatch(blocks, -1)
	if strings.Contains(blocks, "always-miscount") || (strings.Contains(blocks, "miscount-twice") && calls <= 2) {
		found = found[:len(found)-1] // a model merging the last Block away
	}
	var reply strings.Builder
	for _, b := range found {
		fmt.Fprintf(&reply, "<block %s>\npt:%s\n</block>\n", b[1], b[2])
	}

	w.Header().Set("Content-Type", "text/event-stream")
	chunk, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]string{"content": reply.String()}}}})
	fmt.Fprintf(w, "data: %s\n\n", chunk)
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// chatCalls counts chat completions, so tests can prove the model was skipped.
var chatCalls atomic.Int64

// stubAnswer is what the stub chat model streams: it always cites source 1.
var stubAnswer = []string{"The Corpus says ", "Raggman answers Questions ", "[1]", "."}

func stubChat(w http.ResponseWriter, r *http.Request) {
	chatCalls.Add(1)
	var req struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
		ResponseFormat  struct {
			Type string `json:"type"`
		} `json:"response_format"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Stream bool `json:"stream"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || !req.Stream || len(req.Messages) < 2 {
		http.Error(w, "expected a streamed chat with system and user messages", http.StatusBadRequest)
		return
	}
	if req.Model != "qwen3:8b" {
		http.Error(w, "unknown model "+req.Model, http.StatusNotFound)
		return
	}
	if strings.Contains(req.Messages[0].Content, "literary translator") {
		stubTranslation(w, r, req.Messages[0].Content, req.Messages[len(req.Messages)-1].Content)
		return
	}
	if req.ResponseFormat.Type == "json_object" {
		stubExtraction(w, req.Messages[0].Content, req.Messages[len(req.Messages)-1].Content)
		return
	}
	if !strings.Contains(req.Messages[0].Content, "[1]") {
		http.Error(w, "system prompt carries no numbered sources", http.StatusBadRequest)
		return
	}
	if strings.Contains(req.Messages[len(req.Messages)-1].Content, "chat-outage") {
		http.Error(w, "model overloaded", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	// Like qwen3 on Ollama: reasoning arrives first, in its own field, unless
	// the request asks to skip it.
	if req.ReasoningEffort != "none" {
		chunk, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]string{"content": "", "reasoning": "Let me check the sources."}}},
		})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	for _, token := range stubAnswer {
		chunk, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]string{"content": token}}},
		})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		w.(http.Flusher).Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}
