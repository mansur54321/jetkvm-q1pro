package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNewBackupConcurrentNoReplace(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "q1.img")
	type result struct {
		digest [32]byte
		err    error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, value := range []byte{0xaa, 0x55} {
		data := bytes.Repeat([]byte{value}, 131072)
		go func() {
			<-start
			h, err := newBackup(dest, bytes.NewReader(data), int64(len(data)))
			results <- result{h, err}
		}()
	}
	close(start)
	var winner [32]byte
	var successes int
	for range 2 {
		res := <-results
		if res.err == nil {
			winner = res.digest
			successes++
		} else if !errors.Is(res.err, os.ErrExist) {
			t.Fatal(res.err)
		}
	}
	data, err := os.ReadFile(dest)
	if err != nil || successes != 1 || len(data) != 131072 || sha256.Sum256(data) != winner {
		t.Fatalf("published backup did not match the one successful writer: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary backup remained: %v %v", entries, err)
	}
}

func TestShortBackupPublishesNothing(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "short.img")
	if _, err := newBackup(dest, bytes.NewReader([]byte("truncated")), 131072); err == nil {
		t.Fatal("incomplete backup succeeded")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("incomplete backup left files: %v %v", files, err)
	}
	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(existing, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := newBackup(dest, bytes.NewReader([]byte("image")), 5); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing symlink: %v", err)
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "original" {
		t.Fatalf("overwrote existing backup: %v", err)
	}
}

func TestQ1SquashStatfsMagic(t *testing.T) {
	for _, kind := range []uint32{0x4f4f4653, 0x73717368} {
		if !q1SquashType(kind) {
			t.Fatalf("rejected SquashFS magic %#x", kind)
		}
	}
	for _, kind := range []uint32{0x01021994, 0x24051905, 0xef53, 0} {
		if q1SquashType(kind) {
			t.Fatalf("accepted unrelated filesystem %#x", kind)
		}
	}
}
