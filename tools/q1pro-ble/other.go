//go:build !linux

package q1ble

import (
	"context"
	"errors"
)

func (*Controller) Run(context.Context) {}
func runPeripheral(context.Context, *session) error {
	return errors.New("provisioning over Bluetooth requires Linux")
}
