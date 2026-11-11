//go:build wireinject

package main

import (
	"context"

	"github.com/BrunoPereiradosSantos/raggman/internal/answering"
	"github.com/BrunoPereiradosSantos/raggman/internal/api"
	"github.com/BrunoPereiradosSantos/raggman/internal/books"
	"github.com/BrunoPereiradosSantos/raggman/internal/documents"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/logger"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/postgres"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/telemetry"
	"github.com/BrunoPereiradosSantos/raggman/internal/ingestion"
	"github.com/BrunoPereiradosSantos/raggman/internal/queue"
	"github.com/BrunoPereiradosSantos/raggman/internal/translation"
	"github.com/google/wire"
)

// jobGroups registers the Handler of every Job type the workers run.
// Translation work gets workers of its own, one at a time: it runs for hours
// on a local model, and must not keep Ingestion waiting.
//
// Translation Jobs have no Handler yet, so they wait in the queue.
func jobGroups(ingest *ingestion.Handler, extraction *translation.Extraction) []queue.Group {
	return []queue.Group{
		{Workers: 2, Handlers: queue.Handlers{documents.JobTypeIngestion: ingest}},
		{Workers: 1, Handlers: queue.Handlers{translation.JobTypeExtraction: extraction}},
	}
}

func InitializeApp(ctx context.Context) (*App, func(), error) {
	wire.Build(
		environment.Load,
		wire.FieldsOf(new(*environment.Environment), "DatabaseURL"),
		logger.InitAndProvide,
		postgres.NewDatabasePool,
		telemetry.NewTracerProvider,
		documents.NewService,
		books.NewService,
		answering.New,
		api.NewHandler,
		translation.NewService,
		translation.NewExtraction,
		ingestion.New,
		jobGroups,
		queue.New,
		wire.Struct(new(App), "*"),
	)
	return nil, nil, nil
}
