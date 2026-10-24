package app

import (
	"context"
	"io"
)

func contextError(ctx context.Context) error {
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}

type cancelReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader cancelReader) Read(buffer []byte) (int, error) {
	if err := contextError(reader.ctx); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
