//go:build !linux

package kvm

import "errors"

func (d *NBDDevice) openConnections() error {
	return errors.New("NBD requires Linux")
}

func (d *NBDDevice) connect() error {
	return errors.New("NBD requires Linux")
}

func (d *NBDDevice) Close() {}
