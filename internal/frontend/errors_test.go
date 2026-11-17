package frontend

import (
	"context"
	"errors"
	"testing"

	"routelatch/internal/contract"
)

func TestDialFailureAttribution(t *testing.T) {
	poolFailure := &contract.Failure{Kind: contract.PoolUnavailable, Path: contract.PathPool, Message: "pool unavailable"}
	cases := []struct {
		name   string
		err    error
		parent context.Context
		path   contract.Path
		kind   contract.ErrorKind
	}{
		{
			name:   "typed failure is preserved",
			err:    poolFailure,
			parent: context.Background(),
			path:   contract.PathPool,
			kind:   contract.PoolUnavailable,
		},
		{
			name:   "pool deadline attributed to the pool",
			err:    context.DeadlineExceeded,
			parent: context.Background(),
			path:   contract.PathPool,
			kind:   contract.Timeout,
		},
		{
			name:   "direct cancel attributed to direct",
			err:    context.Canceled,
			parent: context.Background(),
			path:   contract.PathDirect,
			kind:   contract.Canceled,
		},
		{
			name: "parent cancel wins over a bare error",
			err:  errors.New("raw"),
			parent: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			path: contract.PathDirect,
			kind: contract.Canceled,
		},
		{
			name:   "untyped error is internal",
			err:    errors.New("raw"),
			parent: context.Background(),
			path:   contract.PathDirect,
			kind:   contract.Internal,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := dialFailure(tc.err, tc.parent, tc.path)
			if f.Kind != tc.kind || f.Path != tc.path {
				t.Fatalf("failure = %+v, want kind %s path %s", f, tc.kind, tc.path)
			}
		})
	}
}
