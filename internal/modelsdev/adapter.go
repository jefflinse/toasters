package modelsdev

import (
	"context"

	"github.com/jefflinse/toasters/internal/service"
)

// Compile-time assertion that catalogAdapter satisfies service.CatalogSource.
var _ service.CatalogSource = (*catalogAdapter)(nil)

// catalogAdapter adapts a modelsdev.Client to the service.CatalogSource interface.
type catalogAdapter struct {
	client *Client
}

// NewCatalogSource wraps a Client as a service.CatalogSource.
func NewCatalogSource(c *Client) service.CatalogSource {
	return &catalogAdapter{client: c}
}

func (a *catalogAdapter) ProvidersSorted(ctx context.Context) ([]service.CatalogSourceProvider, error) {
	provs, err := a.client.ProvidersSorted(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]service.CatalogSourceProvider, 0, len(provs))
	for _, p := range provs {
		sp := service.CatalogSourceProvider{
			ID:   p.ID,
			Name: p.Name,
			API:  p.API,
			Doc:  p.Doc,
			Env:  p.Env,
		}
		for _, m := range p.ModelsSorted() {
			sp.Models = append(sp.Models, service.CatalogSourceModel{
				ID:               m.ID,
				Name:             m.Name,
				Family:           m.Family,
				ToolCall:         m.ToolCall,
				Reasoning:        m.Reasoning,
				StructuredOutput: m.StructuredOutput,
				OpenWeights:      m.OpenWeights,
				ContextLimit:     m.Limit.Context,
				OutputLimit:      m.Limit.Output,
				InputCost:        m.Cost.Input,
				OutputCost:       m.Cost.Output,
			})
		}
		result = append(result, sp)
	}
	return result, nil
}
