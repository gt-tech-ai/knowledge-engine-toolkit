package connector

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every operation of base through the client tier as the client named
// name.
func Decorate(
	base interfaces.ConnectorSource,
	name string,
	deps decorate.Deps,
) interfaces.ConnectorSource {
	return &decorated{base: base, Base: client.NewBase(name, deps)}
}

// decorated runs each ConnectorSource operation through the client tier.
type decorated struct {
	// base is the wrapped source.
	base interfaces.ConnectorSource

	// Base runs the operations.
	client.Base
}

var _ interfaces.ConnectorSource = (*decorated)(nil)

// TestConnection probes the source as the operation "test_connection".
func (d *decorated) TestConnection(ctx context.Context) (int, error) {
	return d.Exec(ctx, "test_connection", d.base.TestConnection)
}

// page is one ListPage result carried through Exec.
type page struct {
	// next is the continuation token ("" when exhausted).
	next string

	// objects is the page.
	objects []interfaces.StorageObject
}

// ListPage lists one page of the source as the operation "list_page".
func (d *decorated) ListPage(
	ctx context.Context,
	continuationToken string,
	limit int,
) ([]interfaces.StorageObject, string, error) {
	res, err := d.Exec(ctx, "list_page", func(ctx context.Context) (page, error) {
		objs, next, err := d.base.ListPage(ctx, continuationToken, limit)
		return page{objects: objs, next: next}, err
	})
	return res.objects, res.next, err
}
