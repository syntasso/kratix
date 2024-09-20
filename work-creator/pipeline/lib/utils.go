package utils

import (
	"bytes"
	"compress/gzip"
	"io"
)

func CompressContent(content []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(content)
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func DecompressContent(compressedContent []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(compressedContent))
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	decompressed, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	return decompressed, nil
}
