//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeBundleTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func bundleTestKey(t *testing.T, dir string) (ed25519.PrivateKey, string, string) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	privFile, pubFile := filepath.Join(dir, "private.pem"), filepath.Join(dir, "public.pem")
	writeBundleTestFile(t, privFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}))
	writeBundleTestFile(t, pubFile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return key, privFile, pubFile
}

func bundleTestImage() []byte {
	b := make([]byte, 512)
	copy(b, "SFOO")
	binary.LittleEndian.PutUint32(b[12:], 262144)
	binary.LittleEndian.PutUint16(b[20:], 18)
	binary.LittleEndian.PutUint16(b[26:], 4)
	binary.LittleEndian.PutUint16(b[28:], 4)
	binary.LittleEndian.PutUint64(b[40:], 481)
	return b
}

func bundleTestMetadata(image []byte) map[string]any {
	return map[string]any{
		"board": "oraybox,q1pro", "format": 1, "kernel": "5.10.160-android", "partition": "rootfs",
		"stock_base": "1.0.0_87160a", "ubi_volume": "rootfs", "volume_id": 0, "app": "0.1.0-test",
		"size": len(image), "sha256": fmt.Sprintf("%x", sha256.Sum256(image)),
	}
}

func bundleTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type bundleTestEntry struct {
	name string
	data []byte
}

func signedBundleTestEntries(key ed25519.PrivateKey, meta, image []byte) []bundleTestEntry {
	return []bundleTestEntry{{"update.json", meta}, {"update.ed25519", ed25519.Sign(key, meta)}, {"rootfs.sfoo", image}}
}

func bundleTestTar(t *testing.T, members []bundleTestEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, m := range members {
		if err := w.WriteHeader(&tar.Header{Name: m.name, Mode: 0644, Size: int64(len(m.data)), Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(m.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestVerifyBundleSignedMetadata(t *testing.T) {
	dir := t.TempDir()
	key, priv, pub := bundleTestKey(t, dir)
	image := bundleTestImage()
	dst := filepath.Join(dir, "update.tar")
	for _, tc := range []struct {
		name string
		set  func(map[string]any)
	}{
		{"format", func(m map[string]any) { m["format"] = 2 }},
		{"board", func(m map[string]any) { m["board"] = "oraybox,q1" }},
		{"kernel", func(m map[string]any) { m["kernel"] = "5.10.160" }},
		{"base", func(m map[string]any) { m["stock_base"] = "another" }},
		{"partition", func(m map[string]any) { m["partition"] = "boot" }},
		{"volume", func(m map[string]any) { m["ubi_volume"] = "rootfs_data" }},
		{"id", func(m map[string]any) { m["volume_id"] = 1 }},
		{"missing-zero-id", func(m map[string]any) { delete(m, "volume_id") }},
		{"null-id", func(m map[string]any) { m["volume_id"] = nil }},
		{"size", func(m map[string]any) { m["size"] = len(image) - 1 }},
		{"huge", func(m map[string]any) { m["size"] = bundleImageLimit + 1 }},
		{"digest", func(m map[string]any) { m["sha256"] = fmt.Sprintf("%064d", 0) }},
		{"missing-version", func(m map[string]any) { delete(m, "app") }},
		{"extra", func(m map[string]any) { m["volume_ID"] = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := bundleTestMetadata(image)
			tc.set(m)
			writeBundleTestFile(t, dst, bundleTestTar(t, signedBundleTestEntries(key, bundleTestJSON(t, m), image)))
			if _, err := verifyBundle(dst, pub); err == nil {
				t.Fatal("accepted incompatible signed manifest")
			}
		})
	}
	meta := append([]byte(" \n"), bundleTestJSON(t, bundleTestMetadata(image))...)
	entries := signedBundleTestEntries(key, meta, image)
	writeBundleTestFile(t, dst, bundleTestTar(t, entries))
	if bundle, err := verifyBundle(dst, pub); err != nil || bundle.app != "0.1.0-test" || bundle.digest != sha256.Sum256(image) || !bytes.Equal(bundle.image, image) {
		t.Fatalf("failed to verify original signed bytes: %v", err)
	}
	_, _, other := bundleTestKey(t, t.TempDir())
	for _, keyPath := range []string{priv, other} {
		if _, err := verifyBundle(dst, keyPath); err == nil {
			t.Fatal("accepted private or untrusted key")
		}
	}
	entries[0].data = bytes.TrimSpace(meta)
	writeBundleTestFile(t, dst, bundleTestTar(t, entries))
	if _, err := verifyBundle(dst, pub); err == nil {
		t.Fatal("accepted changed manifest whitespace without signature")
	}
	for _, bad := range [][]byte{
		bytes.Replace(meta, []byte(`"volume_id":0`), []byte(`"volume_id":1,"volume_id":0`), 1),
		append(bytes.Clone(meta), []byte(" {}")...),
	} {
		writeBundleTestFile(t, dst, bundleTestTar(t, signedBundleTestEntries(key, bad, image)))
		if _, err := verifyBundle(dst, pub); err == nil {
			t.Fatal("accepted ambiguous signed JSON")
		}
	}
}

func TestVerifyBundleRejectsMalformedArchiveAndImage(t *testing.T) {
	dir := t.TempDir()
	key, _, pub := bundleTestKey(t, dir)
	image := bundleTestImage()
	dst := filepath.Join(dir, "package")
	verified := func(b []byte) bool {
		writeBundleTestFile(t, dst, b)
		_, err := verifyBundle(dst, pub)
		return err == nil
	}
	for name, offset := range map[string]int{"magic": 0, "block size": 12, "block log": 20, "XZ": 26, "major": 28, "minor": 30, "used bytes exceed length": 41} {
		t.Run(name, func(t *testing.T) {
			b := bytes.Clone(image)
			b[offset] ^= 0xff
			if verified(bundleTestTar(t, signedBundleTestEntries(key, bundleTestJSON(t, bundleTestMetadata(b)), b))) {
				t.Fatal("accepted signed image with bad geometry")
			}
		})
	}
	meta := bundleTestJSON(t, bundleTestMetadata(image))
	entries := signedBundleTestEntries(key, meta, image)
	good := bundleTestTar(t, entries)
	for name, b := range map[string][]byte{
		"missing footer": good[:len(good)-512], "partial block": good[:len(good)-1],
		"second tar": append(bytes.Clone(good), good...), "trailing garbage": append(bytes.Clone(good), 1),
		"missing member": bundleTestTar(t, entries[:2]), "reordered": bundleTestTar(t, []bundleTestEntry{entries[2], entries[0], entries[1]}),
		"extra member":           bundleTestTar(t, append(entries, bundleTestEntry{"extra", nil})),
		"nonzero member padding": func() []byte { b := bytes.Clone(good); b[512+len(meta)] = 1; return b }(),
	} {
		t.Run(name, func(t *testing.T) {
			if verified(b) {
				t.Fatal("accepted malformed archive")
			}
		})
	}
	entries[2].data = bytes.Clone(image)
	entries[2].data[123] ^= 1
	if verified(bundleTestTar(t, entries)) {
		t.Fatal("accepted changed payload")
	}
	entries[2].name = "../rootfs.sfoo"
	if verified(bundleTestTar(t, entries)) {
		t.Fatal("accepted unexpected member name")
	}
	if err := os.Truncate(dst, bundleArchiveLimit+1); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBundle(dst, pub); err == nil {
		t.Fatal("accepted oversized archive")
	}
}

func TestBundleMemberRejectsLinksAndExtendedHeaders(t *testing.T) {
	for name, header := range map[string]*tar.Header{
		"hard link":                      {Typeflag: tar.TypeLink, Linkname: "elsewhere", Format: tar.FormatUSTAR},
		"symlink":                        {Typeflag: tar.TypeSymlink, Linkname: "elsewhere", Format: tar.FormatUSTAR},
		"directory":                      {Typeflag: tar.TypeDir, Format: tar.FormatUSTAR},
		"PAX preceding a regular member": {Typeflag: tar.TypeReg, Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": "extra header"}},
		"GNU":                            {Typeflag: tar.TypeReg, Format: tar.FormatGNU},
	} {
		t.Run(name, func(t *testing.T) {
			var data bytes.Buffer
			tw := tar.NewWriter(&data)
			header.Name = "rootfs.sfoo"
			if err := tw.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := readBundleMember(&data, "rootfs.sfoo", 0, bundleImageLimit); err == nil {
				t.Fatal("accepted non-USTAR regular member")
			}
		})
	}
}

func TestBundlePublicKeySingleTrustedEd25519(t *testing.T) {
	dir := t.TempDir()
	_, _, pub := bundleTestKey(t, dir)
	good, err := os.ReadFile(pub)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(other.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{
		"wrong public algorithm": pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}),
		"multiple keys":          append(bytes.Clone(good), good...),
		"unparsed prefix":        append([]byte("unexpected prefix\n"), good...),
		"malformed first key":    append([]byte("-----BEGIN PUBLIC KEY-----\nnot PEM\n"), good...),
		"unparsed suffix":        append(bytes.Clone(good), []byte("suffix")...),
	} {
		t.Run(name, func(t *testing.T) {
			writeBundleTestFile(t, pub, b)
			if _, err := bundlePublicKey(pub); err == nil {
				t.Fatal("accepted malformed key file")
			}
		})
	}
}

func TestVerifyPackagePyPack(t *testing.T) {
	for _, exe := range []string{"python3", "openssl"} {
		if _, err := exec.LookPath(exe); err != nil {
			t.Skipf("%s unavailable", exe)
		}
	}
	dir := t.TempDir()
	_, priv, pub := bundleTestKey(t, dir)
	b := bundleTestImage()
	writeBundleTestFile(t, filepath.Join(dir, "q1pro-rootfs.sfoo"), b)
	writeBundleTestFile(t, filepath.Join(dir, "manifest.json"), bundleTestJSON(t, map[string]any{
		"sku": "oraybox-q1pro", "stock": "1.0.0_87160a", "kernel": "5.10.160-android", "volume0_capacity": bundleImageLimit,
		"image_bytes": len(b), "hashes": map[string]string{"q1pro-rootfs.sfoo": fmt.Sprintf("%x", sha256.Sum256(b))}, "app": map[string]string{"version": "0.1.0-test"},
	}))
	dst := filepath.Join(dir, "package.tar")
	cmd := exec.Command("python3", "../q1pro-image/package.py", "pack", dir, priv, dst)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pack: %v: %s", err, output)
	}
	got, err := verifyBundle(dst, pub)
	if err != nil || !bytes.Equal(got.image, b) || got.digest != sha256.Sum256(b) || got.app != "0.1.0-test" {
		t.Fatalf("Python pack/Go verify mismatch: %v", err)
	}
}
