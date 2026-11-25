//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"
)

const (
	bundleImageLimit   = 206 * (131072 - 4096)
	bundleArchiveLimit = bundleImageLimit + 32768
)

type verifiedBundle struct {
	image  []byte
	digest [sha256.Size]byte
	app    string
}

// The command accepts ordinary files. O_NONBLOCK keeps a replacement FIFO from
// hanging open between the initial stat and the check on the opened descriptor.
func openBundleFile(path string, limit int64) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("%s: expected a regular file of at most %d bytes", path, limit)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Size() > limit {
		f.Close()
		return nil, fmt.Errorf("%s: file changed while opening", path)
	}
	return f, nil
}

func bundlePublicKey(path string) (ed25519.PublicKey, error) {
	const limit = 4096
	f, err := openBundleFile(path, limit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("public-key file exceeds size limit")
	}
	data = bytes.TrimSpace(data)
	if !bytes.HasPrefix(data, []byte("-----BEGIN PUBLIC KEY-----")) || bytes.Count(data, []byte("-----BEGIN ")) != 1 {
		return nil, errors.New("expected a PEM public key")
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PUBLIC KEY" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("expected a single PEM public key")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}
	public, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("expected an Ed25519 public key")
	}
	return public, nil
}

// Reading each USTAR header on its own prevents tar.Reader from silently
// consuming PAX or GNU helper records before one of the three signed-package files.
func readBundleMember(r io.Reader, name string, min, max int64) ([]byte, error) {
	var block [512]byte
	if _, err := io.ReadFull(r, block[:]); err != nil {
		return nil, fmt.Errorf("%s header: %w", name, err)
	}
	header, err := tar.NewReader(bytes.NewReader(block[:])).Next()
	if err != nil {
		return nil, fmt.Errorf("%s tar header: %w", name, err)
	}
	if header.Format != tar.FormatUSTAR || header.Typeflag != tar.TypeReg || header.Name != name ||
		header.Linkname != "" || header.Size < min || header.Size > max {
		return nil, fmt.Errorf("expected regular USTAR member %s (%d..%d bytes)", name, min, max)
	}
	data := make([]byte, int(header.Size))
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("%s payload: %w", name, err)
	}
	padding := block[:(512-header.Size%512)%512]
	if _, err := io.ReadFull(r, padding); err != nil || len(bytes.Trim(padding, "\x00")) != 0 {
		return nil, fmt.Errorf("invalid tar padding after %s", name)
	}
	return data, nil
}

func finishBundleTar(r *io.LimitedReader) error {
	var zero, block [512]byte
	for blocks := 0; ; blocks++ {
		_, err := io.ReadFull(r, block[:])
		if err == io.EOF && blocks >= 2 && r.N > 0 {
			return nil
		}
		if err != nil || block != zero {
			return errors.New("expected only complete zero blocks at end of package")
		}
	}
}

type bundleManifest struct {
	size   int64
	sha256 string
	app    string
}

func parseBundleManifest(data []byte) (bundleManifest, error) {
	var m bundleManifest
	var board, kernel, base, partition, volume string
	var format int
	volumeID := -1
	fields := map[string]any{
		"format": &format, "board": &board, "kernel": &kernel, "stock_base": &base, "partition": &partition,
		"ubi_volume": &volume, "volume_id": &volumeID, "size": &m.size, "sha256": &m.sha256, "app": &m.app,
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if key, err := decoder.Token(); err != nil || key != json.Delim('{') || !utf8.Valid(data) {
		return m, errors.New("expected JSON object in signed manifest")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return m, err
		}
		key, ok := token.(string)
		target := fields[key]
		if !ok || target == nil {
			return m, fmt.Errorf("unknown or duplicate manifest field %q", key)
		}
		if err := decoder.Decode(target); err != nil {
			return m, fmt.Errorf("manifest field %s: %w", key, err)
		}
		delete(fields, key)
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') || len(fields) != 0 {
		return m, errors.New("incomplete signed manifest")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return m, errors.New("trailing data after signed manifest")
	}
	if format != 1 || board != "oraybox,q1pro" || kernel != "5.10.160-android" || base != "1.0.0_87160a" ||
		partition != "rootfs" || volume != "rootfs" || volumeID != 0 {
		return m, errors.New("manifest targets an incompatible format, board, kernel, stock base, or UBI volume")
	}
	if m.size <= 96 || m.size > bundleImageLimit || len(m.sha256) != 2*sha256.Size || strings.TrimSpace(m.app) == "" {
		return m, errors.New("invalid signed image size, SHA-256 or application version")
	}
	return m, nil
}

func verifyBundle(packagePath, keyPath string) (*verifiedBundle, error) {
	key, err := bundlePublicKey(keyPath)
	if err != nil {
		return nil, err
	}
	file, err := openBundleFile(packagePath, bundleArchiveLimit)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	r := &io.LimitedReader{R: file, N: bundleArchiveLimit + 1}
	metadata, err := readBundleMember(r, "update.json", 1, 4096)
	if err != nil {
		return nil, err
	}
	signature, err := readBundleMember(r, "update.ed25519", ed25519.SignatureSize, ed25519.SignatureSize)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(key, metadata, signature) {
		return nil, errors.New("Ed25519 signature on update.json did not verify")
	}
	m, err := parseBundleManifest(metadata)
	if err != nil {
		return nil, err
	}
	image, err := readBundleMember(r, "rootfs.sfoo", m.size, m.size)
	if err != nil {
		return nil, err
	}
	if err := finishBundleTar(r); err != nil {
		return nil, err
	}
	// SFOO rotates four SquashFS v4 fields: block_log is at 20 and compression at 26.
	le := binary.LittleEndian
	used := le.Uint64(image[40:48])
	if string(image[:4]) != "SFOO" || le.Uint32(image[12:16]) != 262144 || le.Uint16(image[20:22]) != 18 ||
		le.Uint16(image[26:28]) != 4 || le.Uint16(image[28:30]) != 4 || le.Uint16(image[30:32]) != 0 ||
		used <= 96 || used > uint64(len(image)) {
		return nil, errors.New("invalid Q1 Pro SFOO/XZ geometry")
	}
	sum := sha256.Sum256(image)
	if m.sha256 != hex.EncodeToString(sum[:]) {
		return nil, errors.New("image SHA-256 differs from signed manifest")
	}
	return &verifiedBundle{image: image, digest: sum, app: m.app}, nil
}
