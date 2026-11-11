//go:build wireinject

package main

import (
	"context"

	"github.com/BrunoPereiradosSantos/raggman/internal/api"
	"github.com/BrunoPereiradosSantos/raggman/internal/documents"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/logger"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/postgres"
	"github.com/google/wire"
)

func InitializeApp(ctx context.Context) (*App, func(), error) {
	wire.Build(
		environment.Load,
		wire.FieldsOf(new(*environment.Environment), "DatabaseURL"),
		logger.InitAndProvide,
		postgres.NewDatabasePool,
		documents.NewService,
		api.NewHandler,
		wire.Struct(new(App), "*"),
	)
	return nil, nil, nil
}
