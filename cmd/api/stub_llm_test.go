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
	// translationCalls counts how often each block was seen, per way of
	// getting it wrong, so a block can be answered badly a set number of times.
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

var (
	stubBlock = regexp.MustCompile(`(?s)<block (\d+)>\n(.*?)\n</block>`)
	stubTerm  = regexp.MustCompile(`(?m)^- (.+) => (.+)$`)
	stubMark  = regexp.MustCompile(`⟦/?\d+/?⟧`)
)

// misbehaves reports whether the stub should get a block wrong in the given
// way. The block's own text asks for it: "lose-marker-once" is wrong the
// first time the block is seen, "-twice" the first two times, "-always"
// every time, "-second" only the second time, and "-later" every time but
// the first. So a block can fail one rung of the retry ladder, two, or all,
// fail a rung after passing one, or fail in a different way further up.
func misbehaves(text, way string) bool {
	counter, _ := translationCalls.LoadOrStore(way+"|"+text, new(atomic.Int64))
	calls := counter.(*atomic.Int64).Add(1)
	switch {
	case strings.Contains(text, way+"-always"):
		return true
	case strings.Contains(text, way+"-twice"):
		return calls <= 2
	case strings.Contains(text, way+"-once"):
		return calls <= 1
	case strings.Contains(text, way+"-second"):
		return calls == 2
	case strings.Contains(text, way+"-later"):
		return calls >= 2
	}
	return false
}

// stubTranslation "translates" each numbered block by putting "pt:" before
// it and writing the Names and Terms of the request as they are given, which
// is what a model doing its job does. It keeps inline markers. It always
// turns "Annabel" into "Ana": a model renaming someone the Termbase lacks.
//
// Words in a block make the stub get that block wrong (see misbehaves):
// wrong-name leaves Names and Terms as in the source, skip-term leaves only
// the Terms, lose-marker drops the markers, echo-source returns the source,
// too-short returns almost nothing but the markers, and drop-last, in the last block of a
// request, leaves that block out of the reply. Other words simulate an
// outage or a slow model.
func stubTranslation(w http.ResponseWriter, r *http.Request, system, user string) {
	translationRequestsMu.Lock()
	translationRequests = append(translationRequests, translationRequest{system: system, user: user})
	translationRequestsMu.Unlock()

	lists, blocks, ok := strings.Cut(user, "Translate these blocks:")
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
	lists, _, _ = strings.Cut(lists, "What comes just before")
	nameList, termList, _ := strings.Cut(lists, "Terms (")

	found := stubBlock.FindAllStringSubmatch(blocks, -1)
	if last := found[len(found)-1][2]; misbehaves(last, "drop-last") {
		found = found[:len(found)-1] // a model merging the last Block away
	}
	var reply strings.Builder
	for _, b := range found {
		source := b[2]
		out := source
		if !misbehaves(source, "wrong-name") {
			for _, name := range stubTerm.FindAllStringSubmatch(nameList, -1) {
				out = strings.ReplaceAll(out, name[1], name[2])
			}
			if !misbehaves(source, "skip-term") {
				for _, term := range stubTerm.FindAllStringSubmatch(termList, -1) {
					out = strings.ReplaceAll(out, term[1], term[2])
				}
			}
		}
		out = "pt:" + strings.ReplaceAll(out, "Annabel", "Ana")
		if misbehaves(source, "lose-marker") {
			out = stubMark.ReplaceAllString(out, "")
		}
		if misbehaves(source, "echo-source") {
			out = source
		}
		if misbehaves(source, "too-short") {
			out = "pt:" + strings.Join(stubMark.FindAllString(source, -1), "x")
		}
		fmt.Fprintf(&reply, "<block %s>\n%s\n</block>\n", b[1], out)
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
