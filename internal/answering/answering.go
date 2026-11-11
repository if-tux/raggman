// Package answering turns a Question into an Answer drawn only from the
// Corpus: it retrieves the closest Chunks and has the chat model answer from
// them, with a Citation for each Chunk it uses.
package answering

import (
	"context"
	"fmt"
	"strings"

	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/llm"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/sqlc"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

type Service struct {
	queries       *sqlc.Queries
	embedder      *llm.Client
	chat          *llm.Client
	chatModel     string
	minSimilarity float64
	topK          int
}

func New(pool *pgxpool.Pool, env *environment.Environment) *Service {
	chat := llm.New(env.LLMBaseURL, env.LLMAPIKey)
	// No API key means local Ollama, the only target known to accept it.
	chat.SkipReasoning = !env.LLMThinking && env.LLMAPIKey == ""
	return &Service{
		queries:       sqlc.New(pool),
		embedder:      llm.New(env.EmbeddingsBaseURL(), ""),
		chat:          chat,
		chatModel:     env.LLMModel,
		topK:          env.TopK,
		minSimilarity: env.MinSimilarity,
	}
}

// Answer streams the Answer to question through stream and returns the
// Citations behind the markers the Answer actually uses. When no Chunk in
// the Corpus is relevant enough, the Answer is domain.NotFoundAnswer and the
// chat model is not called.
func (s *Service) Answer(ctx context.Context, question string, stream llm.Stream) ([]domain.Citation, error) {
	sources, err := s.retrieve(ctx, question)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, stream.Token(domain.NotFoundAnswer)
	}

	var answer strings.Builder
	messages := []llm.Message{
		{Role: "system", Content: systemPrompt(sources)},
		// Repeating the language rule next to the Question keeps small models
		// from drifting to the language of the sources.
		{Role: "user", Content: question + "\n\n(Answer in the same language as this Question.)"},
	}
	err = s.chat.ChatStream(ctx, s.chatModel, messages, llm.Stream{
		Thinking: stream.Thinking,
		Token: func(token string) error {
			answer.WriteString(token)
			return stream.Token(token)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("generating answer: %w", err)
	}

	var citations []domain.Citation
	for _, marker := range domain.CitedMarkers(answer.String(), len(sources)) {
		src := sources[marker-1]
		citations = append(citations, domain.Citation{
			Marker:     marker,
			ChunkID:    src.ID,
			DocumentID: src.DocumentID,
			Filename:   src.Filename,
			Page:       int(src.PageNumber),
		})
	}
	return citations, nil
}

// retrieve returns the closest Chunks from Ready Documents, best first,
// dropping any below the minimum similarity.
func (s *Service) retrieve(ctx context.Context, question string) ([]sqlc.SearchCorpusRow, error) {
	vectors, err := s.embedder.Embed(ctx, environment.EmbeddingModel, []string{question})
	if err != nil {
		return nil, fmt.Errorf("embedding question: %w", err)
	}
	rows, err := s.queries.SearchCorpus(ctx, sqlc.SearchCorpusParams{
		Embedding: pgvector.NewVector(vectors[0]),
		TopK:      int32(s.topK),
	})
	if err != nil {
		return nil, fmt.Errorf("searching corpus: %w", err)
	}
	var relevant []sqlc.SearchCorpusRow
	for _, row := range rows {
		if row.Similarity >= s.minSimilarity {
			relevant = append(relevant, row)
		}
	}
	return relevant, nil
}

func systemPrompt(sources []sqlc.SearchCorpusRow) string {
	var b strings.Builder
	b.WriteString(`You answer Questions using only the numbered sources below, taken from the user's documents.

Rules:
- Use only facts stated in the sources. Never use outside knowledge.
- After every claim, cite its source number in square brackets, like [1] or [2].
- If the sources do not answer the Question, say you could not find it in the documents, and cite nothing.
- Write in the same language as the Question, even when the sources are in another language.

Sources:
`)
	for i, src := range sources {
		fmt.Fprintf(&b, "\n[%d] %s, page %d\n%s\n", i+1, src.Filename, src.PageNumber, src.Content)
	}
	return b.String()
}
