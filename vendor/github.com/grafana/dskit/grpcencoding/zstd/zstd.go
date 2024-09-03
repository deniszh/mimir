// Copyright 2020 Mostyn Bramley-Moore.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package zstd is a wrapper for using github.com/klauspost/compress/zstd
// with gRPC.
package zstd

import (
	"bytes"
	"io"
	"runtime"
	"sync"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/grpc/encoding"
)

const Name = "zstd"

var encoderOptions = []zstd.EOption{
	// The default zstd window size is 8MB, which is much larger than the
	// typical RPC message and wastes a bunch of memory.
	zstd.WithWindowSize(512 * 1024),
	zstd.WithEncoderLevel(zstd.SpeedDefault),
}

var decoderOptions = []zstd.DOption{
	// If the decoder concurrency level is not 1, we would need to call
	// Close() to avoid leaking resources when the object is released
	// from compressor.decoderPool.
	zstd.WithDecoderConcurrency(1),
}

// We will set a finalizer on these objects, so when the go-grpc code is
// finished with them, they will be added back to compressor.decoderPool.
type decoderWrapper struct {
	*zstd.Decoder
}

type compressor struct {
	encoder     *zstd.Encoder
	decoderPool sync.Pool // To hold *zstd.Decoder's.
}

func init() {
	encoding.RegisterCompressor(newCompressor())
}

func newCompressor() *compressor {
	enc, _ := zstd.NewWriter(nil, encoderOptions...)
	c := &compressor{
		encoder: enc,
	}
	return c
}

func (c *compressor) Compress(w io.Writer) (io.WriteCloser, error) {
	return &zstdWriteCloser{
		enc:    c.encoder,
		writer: w,
	}, nil
}

type zstdWriteCloser struct {
	enc    *zstd.Encoder
	writer io.Writer    // Compressed data will be written here.
	buf    bytes.Buffer // Buffer uncompressed data here, compress on Close.
}

func (z *zstdWriteCloser) Write(p []byte) (int, error) {
	return z.buf.Write(p)
}

func (z *zstdWriteCloser) Close() error {
	compressed := z.enc.EncodeAll(z.buf.Bytes(), nil)
	_, err := io.Copy(z.writer, bytes.NewReader(compressed))
	return err
}

func (c *compressor) Decompress(r io.Reader) (io.Reader, error) {
	var err error
	var found bool
	var decoder *zstd.Decoder

	// Note: avoid the use of zstd.Decoder.DecodeAll here, since
	// malicious payloads could DoS us with a decompression bomb.

	decoder, found = c.decoderPool.Get().(*zstd.Decoder)
	if !found {
		decoder, err = zstd.NewReader(r, decoderOptions...)
		if err != nil {
			return nil, err
		}
	} else {
		err = decoder.Reset(r)
		if err != nil {
			c.decoderPool.Put(decoder)
			return nil, err
		}
	}

	wrapper := &decoderWrapper{Decoder: decoder}
	runtime.SetFinalizer(wrapper, func(dw *decoderWrapper) {
		err := dw.Reset(nil)
		if err == nil {
			c.decoderPool.Put(dw.Decoder)
		}
	})

	return wrapper, nil
}

func (c *compressor) Name() string {
	return Name
}
