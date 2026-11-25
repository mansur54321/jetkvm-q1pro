// Package q1wifi configures the Q1 Pro's Wi-Fi station through wpa_supplicant.
package q1wifi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Each request uses its own reply address so a delayed reply cannot be read
// as the response to the next command.
func openControl(socket string) (*net.UnixConn, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	local := &net.UnixAddr{Name: "@jetkvm-wifi-" + hex.EncodeToString(nonce[:]), Net: "unixgram"}
	return net.DialUnix("unixgram", local, &net.UnixAddr{Name: socket, Net: "unixgram"})
}

var errScanBusy = errors.New("a scan is already in progress")

func control(socket, command string) (string, error) {
	conn, err := openControl(socket)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return exchange(conn, command)
}

func exchange(conn *net.UnixConn, command string) (string, error) {
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return "", err
	}
	if _, err := conn.Write([]byte(command)); err != nil {
		return "", err
	}
	buf := make([]byte, 65536)
	n, err := conn.Read(buf)
	if err != nil {
		return "", err
	}
	value := strings.TrimRight(string(buf[:n]), "\n")
	if command == "SCAN" && value == "FAIL-BUSY" {
		return "", errScanBusy
	}
	if strings.HasPrefix(value, "FAIL") || value == "UNKNOWN COMMAND" {
		// A SET_NETWORK command may contain network credentials.
		return "", fmt.Errorf("wpa_supplicant rejected %s", strings.Fields(command)[0])
	}
	return value, nil
}

// wpa_supplicant prints SSIDs using printf_encode from src/utils/common.c.
func decodeSSID(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			return "", false
		}
		switch s[i] {
		case '\\', '"':
			b.WriteByte(s[i])
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'e':
			b.WriteByte(0x1b)
		case 'x':
			if i+2 >= len(s) {
				return "", false
			}
			v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err != nil {
				return "", false
			}
			b.WriteByte(byte(v))
			i += 2
		default:
			return "", false
		}
	}
	return b.String(), true
}

func fields(s string) map[string]string {
	result := make(map[string]string)
	for line := range strings.SplitSeq(s, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			result[k] = v
		}
	}
	return result
}
