// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-license: Apache-2.0

// Package zstd is a wrapper for using github.com/klauspost/compress/zstd
// with gRPC.
package bzip2

import (
	"errors"
	"io"
	"sync"

	"github.com/dsnet/compress/bzip2"
	"google.golang.org/grpc/encoding"
)

const (
	// Name is the name of the S2 compressor.
	Name = "bzip2"
)

var encoderOptions = bzip2.WriterConfig{
	// The default bzip2 compression level is 6
	Level: 6,
}
var decoderOptions = bzip2.ReaderConfig{}

type compressor struct {
	name             string
	poolCompressor   sync.Pool
	poolDecompressor sync.Pool
}

type writer struct {
	*bzip2.Writer
	pool *sync.Pool
}

type reader struct {
	*bzip2.Reader
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
		w, err := bzip2.NewWriter(io.Discard, &encoderOptions)
		if err != nil {
			return nil
		}
		return &writer{Writer: w, pool: &c.poolCompressor}
	}
	return c
}

func (c *compressor) Compress(w io.Writer) (io.WriteCloser, error) {
	z := c.poolCompressor.Get().(*writer)
	err := z.Writer.Reset(w)
	return z, err
}

func (c *compressor) Decompress(r io.Reader) (io.Reader, error) {
	z, inPool := c.poolDecompressor.Get().(*reader)
	if !inPool {
		newR, err := bzip2.NewReader(r, &decoderOptions)
		if err != nil {
			return nil, err
		}
		return &reader{Reader: newR, pool: &c.poolDecompressor}, nil
	}
	err := z.Reset(r)
	return z, err
}

func (c *compressor) Name() string {
	return c.name
}

func (zw *writer) Close() error {
	err := zw.Writer.Close()
	zw.pool.Put(zw)
	return err
}

func (zr *reader) Read(p []byte) (n int, err error) {
	n, err = zr.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		zr.pool.Put(zr)
	}
	return n, err
}
