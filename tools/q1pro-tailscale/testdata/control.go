package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"tailscale.com/derp/derpserver"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/key"
)

func main() {
	ip := "192.0.2.1"
	d := derpserver.New(key.NewNode(), func(string, ...any) {})
	derp := httptest.NewUnstartedServer(derpserver.Handler(d))
	derp.Listener.Close()
	var err error
	derp.Listener, err = net.Listen("tcp", ip+":3340")
	if err != nil {
		panic(err)
	}
	derp.StartTLS()
	control := &testcontrol.Server{ExplicitBaseURL: "http://" + ip + ":3330", RequireAuth: true, MagicDNSDomain: "q1.test", Logf: func(string, ...any) {},
		DERPMap: &tailcfg.DERPMap{Regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{1: {RegionID: 1, RegionCode: "lab", RegionName: "Q1 test", Nodes: []*tailcfg.DERPNode{{Name: "1a", RegionID: 1, HostName: ip, IPv4: ip, IPv6: "none", STUNPort: -1, DERPPort: 3340, InsecureForTests: true}}}}}}
	fmt.Fprintln(os.Stderr, "private test control listening")
	err = http.ListenAndServe(ip+":3330", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/auth/") {
			if !control.CompleteAuth(r.URL.Path) {
				http.Error(w, "unknown test node", 404)
				return
			}
			fmt.Fprint(w, "approved test node")
			return
		}
		if strings.HasPrefix(r.UserAgent(), "Mozilla/") {
			http.NotFound(w, r)
			return
		}
		control.ServeHTTP(w, r)
	}))
	if err != nil {
		panic(err)
	}
}
