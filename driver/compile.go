package driver

import (
	"context"

	"github.com/mccanne/zq/ast"
	"github.com/mccanne/zq/proc"
	"github.com/mccanne/zq/zng/resolver"
	"go.uber.org/zap"
)

func Compile(program ast.Proc, input proc.Proc) (*proc.MuxOutput, error) {
	ctx := &proc.Context{
		Context:     context.Background(),
		TypeContext: resolver.NewContext(),
		Logger:      zap.NewNop(),
		Warnings:    make(chan string, 5),
	}
	leaves, err := proc.CompileProc(nil, program, ctx, input)
	if err != nil {
		return nil, err
	}
	return proc.NewMuxOutput(ctx, leaves), nil
}
