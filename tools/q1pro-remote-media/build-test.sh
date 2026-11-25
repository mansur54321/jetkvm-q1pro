#!/bin/sh
set -eu
: "${CC:?set the buildkit ARM cross compiler}"
out=${1:?output path for the ARM test binary}
export GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=1
go test -c -buildvcs=false -trimpath -tags q1medialab,netgo,timetzdata,nomsgpack \
    -ldflags="-s -w -extldflags '-Wl,--no-as-needed -lrockit -lrockchip_mpp -lrga -lstdc++ -lm -Wl,--allow-shlib-undefined'" \
    -o "$out" .
