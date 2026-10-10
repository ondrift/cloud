// Package evm stands in for the fxevm testnode's evm element: the handlers the
// fixture Driftfile names, and nothing else.
package evm

import "context"

func PostRPC(body map[string]any) (map[string]any, error) { return body, nil }

func PostReset(body map[string]any) (map[string]any, error) { return body, nil }

func Sequencer(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
