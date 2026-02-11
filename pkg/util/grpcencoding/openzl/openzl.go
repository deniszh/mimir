// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-license: Apache-2.0

// Package zstd is a wrapper for using github.com/boris-chu/go-openzl
// with gRPC.
package openzl

import (
	"errors"
	"io"
	"sync"

	"github.com/boris-chu/go-openzl"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/grpc/encoding"
)

const (
	// Name is the name of the S2 compressor.
	Name = "openzl"
)

type compressor struct {
	name             string
	poolCompressor   sync.Pool
	poolDecompressor sync.Pool
}

type writer struct {
	*openzl.Compressor
	pool *sync.Pool
}

type reader struct {
	*openzl.Decompressor
	pool *sync.Pool
}

func init() {
	encoding.RegisterCompressor(newCompressor())
}

func newCompressor() *compressor {
	c := &compressor{
		name: Name,
	}
	c.poolCompressor.New = func() interface{} {
		w, err := openzl.NewWriter(io.Discard)
		if err != nil {
			return nil
		}
		return &writer{Compressor: w, pool: &c.poolCompressor}
	}
	return c
}

func (c *compressor) Compress(w io.Writer) (io.WriteCloser, error) {
	z := c.poolCompressor.Get().(*writer)
	return z, nil
}

func (c *compressor) Decompress(r io.Reader) (io.Reader, error) {
	z, inPool := c.poolDecompressor.Get().(*reader)
	if !inPool {
		newR, err := zstd.NewReader(r)
		if err != nil {
			return nil, err
		}
		return &reader{Decompressor: newR, pool: &c.poolDecompressor}, nil
	}
	return z, nil
}

func (c *compressor) Name() string {
	return c.name
}

func (zw *writer) Close() error {
	err := zw.Compressor.Close()
	zw.pool.Put(zw)
	return err
}

func (zr *reader) Read(p []byte) (n int, err error) {
	n, err = zr.Decompressor.Read(p)
	if errors.Is(err, io.EOF) {
		zr.pool.Put(zr)
	}
	return n, err
}
