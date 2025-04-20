// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/mostynb/go-grpc-compression/blob/f7e92b39057ca421a6485f650243a3e804036498/internal/s2/s2.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: Copyright 2022 Mostyn Bramley-Moore.

// Package minlz is an experimental wrapper for using
// https://github.com/minio/minlz stream compression with gRPC.
package minlz

import (
	"errors"
	"io"
	"sync"

	"github.com/minio/minlz"
	"google.golang.org/grpc/encoding"
)

const (
	// Name is the name of the S2 compressor.
	Name = "minlz"
)

type compressor struct {
	name             string
	poolCompressor   sync.Pool
	poolDecompressor sync.Pool
}

type writer struct {
	*minlz.Writer
	pool *sync.Pool
}

type reader struct {
	*minlz.Reader
	pool *sync.Pool
}

func init() {
	encoding.RegisterCompressor(newCompressor())
}

func newCompressor() *compressor {
	opts := []minlz.WriterOption{
		minlz.WriterLevel(minlz.LevelBalanced),
		minlz.WriterBlockSize(1 << 21),
	}
	c := &compressor{
		name: Name,
	}
	c.poolCompressor.New = func() interface{} {
		enc := minlz.NewWriter(io.Discard, opts...)
		return &writer{Writer: enc, pool: &c.poolCompressor}
	}
	return c
}

func (c *compressor) Compress(w io.Writer) (io.WriteCloser, error) {
	s := c.poolCompressor.Get().(*writer)
	s.Writer.Reset(w)
	return s, nil
}

func (c *compressor) Decompress(r io.Reader) (io.Reader, error) {
	s, inPool := c.poolDecompressor.Get().(*reader)
	if !inPool {
		dec := minlz.NewReader(r)
		return &reader{Reader: dec, pool: &c.poolDecompressor}, nil
	}
	s.Reset(r)
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
