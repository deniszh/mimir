// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-license: Apache-2.0

// Package zstd is a wrapper for using github.com/valyala/gozstd
// with gRPC.
package zstd_cgo

import (
	"errors"
	"io"
	"sync"

	gozstd "github.com/valyala/gozstd"
	"google.golang.org/grpc/encoding"
)

const (
	// Name is the name of the S2 compressor.
	Name             = "zstd"
	compressionLevel = 3
)

type compressor struct {
	name             string
	poolCompressor   sync.Pool
	poolDecompressor sync.Pool
}

type writer struct {
	*gozstd.Writer
	pool *sync.Pool
}

type reader struct {
	*gozstd.Reader
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
		w := gozstd.NewWriterLevel(io.Discard, compressionLevel)
		return &writer{Writer: w, pool: &c.poolCompressor}
	}
	return c
}

func (c *compressor) Compress(w io.Writer) (io.WriteCloser, error) {
	s := c.poolCompressor.Get().(*writer)
	s.Writer.Reset(w, nil, compressionLevel)
	return s, nil
}

func (c *compressor) Decompress(r io.Reader) (io.Reader, error) {
	s, inPool := c.poolDecompressor.Get().(*reader)
	if !inPool {
		newR := gozstd.NewReader(r)
		return &reader{Reader: newR, pool: &c.poolDecompressor}, nil
	}
	s.Reset(r, nil)
	return s, nil
}

func (c *compressor) Name() string {
	return c.name
}

func (s *writer) Close() error {
	err := s.Writer.Close()
	s.pool.Put(s)
	return err
}

func (s *reader) Read(p []byte) (n int, err error) {
	n, err = s.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		s.pool.Put(s)
	}
	return n, err
}
