package kvm

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jetkvm/kvm/internal/q1tailscale"
)

var q1Tailscale = q1tailscale.New("/userdata/jetkvm/tailscale", "/run/q1-tailscale")

func rpcSetQ1TailscaleEnabled(enabled bool) error {
	if !isQ1Pro() {
		return errors.New("managed Tailscale is only available on Q1 Pro")
	}
	return q1Tailscale.SetEnabled(enabled, config.TailscaleControlURL)
}

func rpcBeginQ1TailscaleLogin() error {
	if !isQ1Pro() {
		return errors.New("managed Tailscale is only available on Q1 Pro")
	}
	return q1Tailscale.Login()
}

func handleQ1TailscaleInstall(c *gin.Context) {
	if !isQ1Pro() {
		c.Status(http.StatusNotFound)
		return
	}
	if c.Request.ContentLength != -1 && c.Request.ContentLength != q1tailscale.ArchiveBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "incorrect Tailscale archive size"})
		return
	}
	if err := q1Tailscale.Install(http.MaxBytesReader(c.Writer, c.Request.Body, q1tailscale.ArchiveBytes)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
