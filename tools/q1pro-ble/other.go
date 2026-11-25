//go:build !linux

package q1ble

import (
	"context"
	"errors"
)

func (*Controller) Run(context.Context) {}
func runPeripheral(context.Context, *session) error {
	return errors.New("Bluetooth provisioning requires Linux")
}
