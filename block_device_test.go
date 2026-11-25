package kvm

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/psanford/httpreadat"
)

func TestHTTPMountWithoutNBDKeepsStateEmpty(t *testing.T) {
	if _, err := os.Stat(nbdDevicePath); !errors.Is(err, os.ErrNotExist) {
		t.Skip("requires a system without nbd0")
	}
	image := bytes.Repeat([]byte{0x42}, 2048)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "test.iso", time.Time{}, bytes.NewReader(image))
	}))
	defer s.Close()
	for range 2 {
		if err := rpcMountWithHTTP(s.URL, CDROM); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("mount error = %v, want missing block device", err)
		}
		if currentVirtualMediaState != nil || nbdDevice != nil {
			t.Fatal("unsuccessful HTTP mount published media state")
		}
	}
}

func TestQ1WithoutNBDOffersStorageDownload(t *testing.T) {
	if _, err := os.Stat(nbdDevicePath); !errors.Is(err, os.ErrNotExist) {
		t.Skip("requires a system without nbd0")
	}
	useQ1Pro(t)
	caps, err := rpcGetDeviceCapabilities()
	if err != nil || !slices.Contains(caps, "storage_download_only") {
		t.Fatalf("capabilities: %v, %v", caps, err)
	}
	info, err := rpcCheckMountUrl("unused URL")
	if err != nil || info.Usable || info.Reason == "" {
		t.Fatalf("URL response: %+v %v", info, err)
	}
}

func TestRemoteNBDReadsLastSector(t *testing.T) {
	if os.Getenv("JETKVM_TEST_DISPOSABLE_NBD") != "1" {
		t.Skip("requires an unused nbd0 inside a disposable Linux guest")
	}
	useQ1Pro(t)
	caps, err := rpcGetDeviceCapabilities()
	if err != nil || slices.Contains(caps, "storage_download_only") {
		t.Fatalf("NBD was not discovered: %v %v", caps, err)
	}
	image := make([]byte, 6*1024)
	for i := range image {
		image[i] = byte(i/512 + 1)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "test.iso", time.Time{}, bytes.NewReader(image))
	}))
	defer s.Close()
	for range 2 {
		device := NewNBDDevice()
		device.backend = remoteImageBackend{ReaderAt: httpreadat.New(s.URL), size: int64(len(image))}
		if err := device.Start(); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(nbdDevicePath)
		if err != nil {
			device.Close()
			t.Fatal(err)
		}
		data, err := io.ReadAll(f)
		f.Close()
		device.Close()
		if err != nil || !bytes.Equal(data, image) {
			t.Fatalf("block device returned %d of %d bytes, err = %v", len(data), len(image), err)
		}
	}
}
