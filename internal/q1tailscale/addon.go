// Package q1tailscale manages the optional userspace daemon on a Q1 Pro.
package q1tailscale

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	Version      = "1.104.7"
	ArchiveName  = "tailscale_" + Version + "_arm.tgz"
	ArchiveBytes = 37205609
	DaemonBytes  = 37986984
	archiveSHA   = "1c753fef45d497491081db37552b7a2a42c7932292743bf6936cb1a42b6bceb0"
	daemonSHA    = "7b6e407353575cb4dcd1d95da7a1b197c9e6c0597b9570007d7564d2929a7ae4"
)

func availableBytes(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("Tailscale directory must be private: %s", path)
	}
	return nil
}

func (s *Service) binary() string { return filepath.Join(s.dir, "tailscaled") }

func (s *Service) installed() bool {
	file, err := os.Lstat(s.binary())
	return err == nil && file.Mode().IsRegular() && file.Size() == DaemonBytes
}

func (s *Service) verifyBinary() error {
	if !s.installed() {
		return errors.New("install the Q1 ARM Tailscale archive first")
	}
	f, err := os.Open(s.binary())
	if err != nil {
		return err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return err
	}
	if hex.EncodeToString(sum.Sum(nil)) != daemonSHA {
		return errors.New("installed Tailscale daemon failed its checksum")
	}
	return nil
}

// Install streams the official ARM archive. Only the verified daemon is published on UBIFS.
func (s *Service) Install(source io.Reader) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := privateDir(s.dir); err != nil {
		return err
	}
	if _, err := os.Lstat(s.binary()); err == nil {
		return errors.New("Tailscale daemon already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	free, err := availableBytes(s.dir)
	if err != nil {
		return err
	}
	if free < DaemonBytes {
		return fmt.Errorf("Tailscale requires %d available bytes; device has %d", DaemonBytes, free)
	}
	tmp, err := os.CreateTemp(s.dir, ".install-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	limit := &io.LimitedReader{R: source, N: ArchiveBytes + 1}
	archiveHash, binaryHash := sha256.New(), sha256.New()
	compressed := io.TeeReader(limit, archiveHash)
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return err
	}
	defer gz.Close()
	tarBytes := &io.LimitedReader{R: gz, N: 70006272 + 1}
	tr := tar.NewReader(tarBytes)
	const name = "tailscale_" + Version + "_arm/tailscaled"
	var found bool
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Name == name {
			if found || header.Typeflag != tar.TypeReg || header.Size != DaemonBytes {
				return errors.New("unexpected ARM daemon entry")
			}
			if _, err := io.CopyN(io.MultiWriter(tmp, binaryHash), tr, DaemonBytes); err != nil {
				return err
			}
			found = true
		}
	}
	if _, err := io.Copy(io.Discard, tarBytes); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, compressed); err != nil {
		return err
	}
	if !found || tarBytes.N != 1 || limit.N != 1 || hex.EncodeToString(archiveHash.Sum(nil)) != archiveSHA ||
		hex.EncodeToString(binaryHash.Sum(nil)) != daemonSHA {
		return errors.New("archive does not match the official Tailscale " + Version + " ARM release")
	}
	if err := tmp.Chmod(0700); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), s.binary()); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
