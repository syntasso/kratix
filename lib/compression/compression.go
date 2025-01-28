package compression

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
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
	compressedBytes := buf.Bytes()
	encodedBytes := make([]byte, base64.StdEncoding.EncodedLen(len(compressedBytes)))
	base64.StdEncoding.Encode(encodedBytes, compressedBytes)
	return encodedBytes, nil
}

func DecompressContent(compressedBytes []byte) ([]byte, error) {
	decodedBytes := make([]byte, base64.StdEncoding.DecodedLen(len(compressedBytes)))
	n, err := base64.StdEncoding.Decode(decodedBytes, compressedBytes)
	if err != nil {
		return nil, err
	}

	zr, err := gzip.NewReader(bytes.NewReader(decodedBytes[:n]))
	if err != nil {
		return nil, err
	}
	defer zr.Close() //nolint:errcheck

	decompressed, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}

	return decompressed, nil
}

func InCompressedContents(compressedContent string, content []byte) (bool, error) {
	decompressedContent, err := DecompressContent([]byte(compressedContent))
	if err != nil {
		return false, fmt.Errorf("unable to decompress contents: %w", err)
	}
	return bytes.Contains(decompressedContent, content), nil
}
